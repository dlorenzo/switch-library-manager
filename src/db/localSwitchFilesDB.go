package db

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/firebat20/switch-library-manager/fileio"
	"github.com/firebat20/switch-library-manager/settings"
	"github.com/firebat20/switch-library-manager/switchfs"
	"go.uber.org/zap"
)

var (
	versionRegex = regexp.MustCompile(`\[[vV]?(?P<version>[0-9]{1,10})]`)
	titleIdRegex = regexp.MustCompile(`\[(?P<titleId>[0-9A-Fa-f]{16})]`)
)

const (
	DB_TABLE_FILE_SCAN_METADATA = "deep-scan"
	DB_TABLE_LOCAL_LIBRARY      = "local-library"
)

const (
	REASON_UNSUPPORTED_TYPE = iota
	REASON_DUPLICATE
	REASON_OLD_UPDATE
	REASON_UNRECOGNISED
	REASON_MALFORMED_FILE
	REASON_MISSING_BASE
)

const (
	// progressEmitInterval caps how often per-file progress messages are
	// emitted. In GUI mode every update is an IPC round-trip into Electron;
	// sending one per file makes large scans measurably slower and floods the
	// UI. Final/summary updates are always sent regardless of the throttle.
	progressEmitInterval = 100 * time.Millisecond

	// maxScanWorkers caps the deep-scan worker pool. Metadata extraction is a
	// mix of small seeky reads and AES decryption; beyond ~8 workers the
	// returns diminish and heavily parallel seeks can regress on spinning
	// disks.
	maxScanWorkers = 8
)

// progressThrottle rate-limits progress updates. Safe for concurrent use.
type progressThrottle struct {
	lastEmit int64 // unix nanos of the last emitted update
}

// ok reports whether an update may be emitted now, and if so claims the slot.
func (t *progressThrottle) ok() bool {
	now := time.Now().UnixNano()
	last := atomic.LoadInt64(&t.lastEmit)
	if now-last < int64(progressEmitInterval) {
		return false
	}
	return atomic.CompareAndSwapInt64(&t.lastEmit, last, now)
}

type LocalSwitchDBManager struct {
	db *PersistentDB
}

func NewLocalSwitchDBManager(baseFolder string) (*LocalSwitchDBManager, error) {
	db, err := NewPersistentDB(baseFolder)
	if err != nil {
		return nil, err
	}
	return &LocalSwitchDBManager{db: db}, nil
}

func (ldb *LocalSwitchDBManager) Close() {
	ldb.db.Close()
}

type ExtendedFileInfo struct {
	FileName   string
	BaseFolder string
	Size       int64
	IsDir      bool
}

type SwitchFileInfo struct {
	ExtendedInfo ExtendedFileInfo
	Metadata     *switchfs.ContentMetaAttributes
}

type SwitchGameFiles struct {
	File         SwitchFileInfo
	BaseExist    bool
	Updates      map[int]SwitchFileInfo
	Dlc          map[string]SwitchFileInfo
	MultiContent bool
	LatestUpdate int
	IsSplit      bool
}

type SkippedFile struct {
	ReasonCode     int
	ReasonText     string
	AdditionalInfo string
}

type LocalSwitchFilesDB struct {
	TitlesMap map[string]*SwitchGameFiles
	Skipped   map[ExtendedFileInfo]SkippedFile
	NumFiles  int
}

func (ldb *LocalSwitchDBManager) CreateLocalSwitchFilesDB(folders []string,
	progress ProgressUpdater, recursive bool, ignoreCache bool) (*LocalSwitchFilesDB, error) {

	titles := map[string]*SwitchGameFiles{}
	skipped := map[ExtendedFileInfo]SkippedFile{}
	files := []ExtendedFileInfo{}

	if !ignoreCache {
		if err := ldb.db.GetEntry(DB_TABLE_LOCAL_LIBRARY, "files", &files); err != nil {
			zap.S().Warnf("ignoring corrupt files cache: %v", err)
			files = []ExtendedFileInfo{}
		}
		if err := ldb.db.GetEntry(DB_TABLE_LOCAL_LIBRARY, "skipped", &skipped); err != nil {
			zap.S().Warnf("ignoring corrupt skipped cache: %v", err)
			skipped = map[ExtendedFileInfo]SkippedFile{}
		}
		if err := ldb.db.GetEntry(DB_TABLE_LOCAL_LIBRARY, "titles", &titles); err != nil {
			zap.S().Warnf("ignoring corrupt titles cache: %v", err)
			titles = map[string]*SwitchGameFiles{}
		}
		if titles == nil {
			titles = map[string]*SwitchGameFiles{}
		}
		if skipped == nil {
			skipped = map[ExtendedFileInfo]SkippedFile{}
		}
		if files == nil {
			files = []ExtendedFileInfo{}
		}
	}

	if len(titles) == 0 {

		for i, folder := range folders {
			err := scanFolder(folder, recursive, &files, progress)
			if progress != nil {
				progress.UpdateProgress(i+1, len(folders)+1, "Scanning files in "+folder)
			}
			if err != nil {
				zap.S().Warnf("failed to scan folder [%v]: %v", folder, err)
				continue
			}
		}

		ldb.processLocalFiles(files, progress, titles, skipped)

		// Persist all three cache keys in a single bolt transaction (one
		// fsync) instead of three separate Update transactions.
		if err := ldb.db.AddEntries(DB_TABLE_LOCAL_LIBRARY, map[string]interface{}{
			"files":   files,
			"skipped": skipped,
			"titles":  titles,
		}); err != nil {
			zap.S().Warnf("failed to persist local library cache: %v", err)
		}
	}

	if progress != nil {
		progress.UpdateProgress(len(files), len(files), "Complete")
	}

	return &LocalSwitchFilesDB{TitlesMap: titles, Skipped: skipped, NumFiles: len(files)}, nil
}

func scanFolder(folder string, recursive bool, files *[]ExtendedFileInfo, progress ProgressUpdater) error {
	var throttle progressThrottle

	// WalkDir avoids the per-entry lstat that filepath.Walk performs, and in
	// non-recursive mode we prune subdirectories instead of walking the whole
	// tree and filtering afterwards.
	return filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			zap.S().Error("Error while scanning folders", err)
			if path == folder {
				// The root itself is unreadable - surface that to the caller.
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if path == folder {
			return nil
		}

		if d.IsDir() {
			if !recursive {
				return fs.SkipDir
			}
			return nil
		}

		name := d.Name()
		if runtime.GOOS == "darwin" && strings.EqualFold(name, ".ds_store") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			zap.S().Error("Error while reading file info", err)
			return nil
		}

		if progress != nil && throttle.ok() {
			progress.UpdateProgress(-1, -1, "Scanning "+name)
		}

		base := path[0 : len(path)-len(name)]
		*files = append(*files, ExtendedFileInfo{FileName: name, BaseFolder: base, Size: info.Size(), IsDir: false})

		return nil
	})
}

func (ldb *LocalSwitchDBManager) ClearScanData() error {
	return ldb.db.ClearTable(DB_TABLE_FILE_SCAN_METADATA)
}

// scanCandidate is a file that passed the cheap filters and needs metadata
// extraction.
type scanCandidate struct {
	file     ExtendedFileInfo
	filePath string
	isSplit  bool
}

// metadataResult carries everything a worker learned about one file, so the
// (sequential) classification phase can apply it without any shared state in
// the workers.
type metadataResult struct {
	contentMap map[string]*switchfs.ContentMetaAttributes
	skip       *SkippedFile // non-nil => record file as skipped (may coexist with a contentMap from the filename fallback)
	err        error        // non-nil => metadata could not be determined at all
	fileKey    string
	fresh      bool // true when contentMap came from a deep read and should be cached
}

func (ldb *LocalSwitchDBManager) processLocalFiles(files []ExtendedFileInfo,
	progress ProgressUpdater,
	titles map[string]*SwitchGameFiles,
	skipped map[ExtendedFileInfo]SkippedFile) {

	newMetadata := make(map[string]interface{})
	appSettings := settings.ReadSettings("") // use empty path, as it will use existing settings instance
	ignoreFileTypes := map[string]struct{}{}
	for _, ext := range appSettings.IgnoreFileTypes {
		if strings.HasPrefix(ext, ".") {
			ignoreFileTypes[strings.ToLower(ext)] = struct{}{}
		} else {
			ignoreFileTypes["."+strings.ToLower(ext)] = struct{}{}
		}
	}
	if runtime.GOOS == "darwin" {
		ignoreFileTypes[".ds_store"] = struct{}{}
	}

	// ---- Phase 0: cheap filtering (sequential, no I/O) ----
	candidates := make([]scanCandidate, 0, len(files))
	for _, file := range files {
		if file.IsDir {
			continue
		}

		if runtime.GOOS == "darwin" && strings.EqualFold(file.FileName, ".ds_store") {
			continue
		}

		if strings.HasPrefix(file.FileName, "_") {
			continue
		}

		fileName := strings.ToLower(file.FileName)
		isSplit := false

		// Guard: names shorter than 2 chars would panic on the slice below.
		if len(fileName) >= 2 {
			if partNum, err := strconv.Atoi(fileName[len(fileName)-2:]); err == nil {
				if partNum == 0 {
					isSplit = true
				} else {
					continue
				}
			}
		}

		// only handle XCI/XCZ and NSP/NSZ files
		fileExtension := filepath.Ext(fileName)
		if !isSplit && fileExtension != ".xci" && fileExtension != ".xcz" && fileExtension != ".nsp" && fileExtension != ".nsz" {
			if _, ok := ignoreFileTypes[fileExtension]; !ok {
				skipped[file] = SkippedFile{ReasonCode: REASON_UNSUPPORTED_TYPE, ReasonText: "File type is not supported"}
			}
			continue
		}

		candidates = append(candidates, scanCandidate{
			file:     file,
			filePath: filepath.Join(file.BaseFolder, file.FileName),
			isSplit:  isSplit,
		})
	}

	// ---- Phase 1: metadata extraction (parallel) ----
	// Preload the entire deep-scan cache bucket in a single read transaction
	// instead of one bolt View+gob decode per file, and hand the raw bytes to
	// the workers (read-only, so no locking needed).
	cachedRaw, err := ldb.db.GetRawTable(DB_TABLE_FILE_SCAN_METADATA)
	if err != nil {
		zap.S().Warnf("failed to preload metadata cache, falling back to full scan: %v", err)
		cachedRaw = map[string][]byte{}
	}

	results := make([]metadataResult, len(candidates))
	total := len(candidates)

	if total > 0 {
		workers := runtime.NumCPU()
		if workers > maxScanWorkers {
			workers = maxScanWorkers
		}
		if workers > total {
			workers = total
		}
		if workers < 1 {
			workers = 1
		}

		var (
			wg       sync.WaitGroup
			jobs     = make(chan int)
			done     int64
			throttle progressThrottle
			progMu   sync.Mutex
		)

		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					c := candidates[i]
					results[i] = readGameMetadata(c.file, c.filePath, cachedRaw)

					n := atomic.AddInt64(&done, 1)
					if progress != nil && (int(n) == total || throttle.ok()) {
						progMu.Lock()
						progress.UpdateProgress(int(n), total, "Processing: "+c.file.FileName)
						progMu.Unlock()
					}
				}
			}()
		}

		for i := range candidates {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
	}

	// ---- Phase 2: classification (sequential, original file order) ----
	// Running this phase in the same deterministic order as the old
	// single-threaded loop preserves all "keep existing / keep compressed"
	// dedup tie-breaking behavior exactly.
	for i := range candidates {
		file := candidates[i].file
		isSplit := candidates[i].isSplit
		res := results[i]

		if res.skip != nil {
			skipped[file] = *res.skip
		}
		if res.fresh && res.contentMap != nil {
			newMetadata[res.fileKey] = res.contentMap
		}
		if res.err != nil {
			if _, ok := skipped[file]; !ok {
				skipped[file] = SkippedFile{ReasonText: "Unable to determine Title ID / Version: " + res.err.Error(), ReasonCode: REASON_UNRECOGNISED}
			}
			continue
		}

		contentMap := res.contentMap

		// Ensure base games are processed before updates and DLC
		// This fixes the issue where a multi-content XCI file processes an update first,
		// and incorrectly thinks the base file doesn't exist yet, flagging the XCI as an old update.
		var baseMetadata []*switchfs.ContentMetaAttributes
		var otherMetadata []*switchfs.ContentMetaAttributes
		for _, metadata := range contentMap {
			if strings.HasSuffix(metadata.TitleId, "000") {
				baseMetadata = append(baseMetadata, metadata)
			} else {
				otherMetadata = append(otherMetadata, metadata)
			}
		}

		hasBase := len(baseMetadata) > 0
		orderedMetadata := append(baseMetadata, otherMetadata...)

		for _, metadata := range orderedMetadata {

			id := strings.ToLower(metadata.TitleId)
			if len(id) != 16 {
				continue
			}
			if _, err := strconv.ParseUint(id, 16, 64); err != nil {
				continue
			}
			idPrefix := id[0 : len(id)-3]
			if !(strings.HasSuffix(id, "000") || strings.HasSuffix(id, "800")) {
				intVar, err := strconv.ParseUint(id[len(id)-4:len(id)-3], 16, 64)
				if err != nil || intVar == 0 {
					continue
				}
				h := fmt.Sprintf("%x", intVar-1)
				idPrefix = id[0:len(id)-4] + h
			}

			multiContent := len(contentMap) > 1
			switchTitle := &SwitchGameFiles{
				MultiContent: multiContent,
				Updates:      map[int]SwitchFileInfo{},
				Dlc:          map[string]SwitchFileInfo{},
				BaseExist:    false,
				IsSplit:      isSplit,
				LatestUpdate: 0,
			}
			if t, ok := titles[idPrefix]; ok {
				switchTitle = t
			}
			titles[idPrefix] = switchTitle

			//process Updates
			if strings.HasSuffix(metadata.TitleId, "800") {
				metadata.Type = "Update"

				if update, ok := switchTitle.Updates[metadata.Version]; ok {
					if appSettings.OrganizeOptions.PrioritizeCompressed && isCompressed(file.FileName) && !isCompressed(update.ExtendedInfo.FileName) {
						skipped[update.ExtendedInfo] = SkippedFile{ReasonCode: REASON_DUPLICATE, ReasonText: "Duplicate update file. Keeping compressed version.\nOld: " + filepath.Join(update.ExtendedInfo.BaseFolder, update.ExtendedInfo.FileName) + "\nNew: " + filepath.Join(file.BaseFolder, file.FileName)}
						zap.S().Warnf("-->Duplicate update file found. Keeping compressed version [%v] over [%v]", file.FileName, update.ExtendedInfo.FileName)
						delete(switchTitle.Updates, update.Metadata.Version)
					} else {
						if !hasBase {
							skipped[file] = SkippedFile{ReasonCode: REASON_DUPLICATE, ReasonText: "Duplicate update file. Keeping existing version.\nExisting: " + filepath.Join(update.ExtendedInfo.BaseFolder, update.ExtendedInfo.FileName) + "\nDuplicate: " + filepath.Join(file.BaseFolder, file.FileName)}
						}
						zap.S().Warnf("-->Duplicate update file found. Keeping existing version [%v] over [%v]", update.ExtendedInfo.FileName, file.FileName)
						continue
					}
				}
				switchTitle.Updates[metadata.Version] = SwitchFileInfo{ExtendedInfo: file, Metadata: metadata}
				if metadata.Version > switchTitle.LatestUpdate {
					if switchTitle.LatestUpdate != 0 {
						oldUpdate := switchTitle.Updates[switchTitle.LatestUpdate]
						if switchTitle.BaseExist &&
							oldUpdate.ExtendedInfo.BaseFolder == switchTitle.File.ExtendedInfo.BaseFolder &&
							oldUpdate.ExtendedInfo.FileName == switchTitle.File.ExtendedInfo.FileName {
							delete(switchTitle.Updates, switchTitle.LatestUpdate)
						} else {
							skipped[oldUpdate.ExtendedInfo] = SkippedFile{ReasonCode: REASON_OLD_UPDATE, ReasonText: "Old update file. A newer update exists locally.\nNew: " + filepath.Join(file.BaseFolder, file.FileName) + "\nOld: " + filepath.Join(oldUpdate.ExtendedInfo.BaseFolder, oldUpdate.ExtendedInfo.FileName)}
						}
					}
					switchTitle.LatestUpdate = metadata.Version
				} else if metadata.Version < switchTitle.LatestUpdate {
					// Flag only if this file is not also considered the base game
					if !hasBase && (!switchTitle.BaseExist || file.BaseFolder != switchTitle.File.ExtendedInfo.BaseFolder || file.FileName != switchTitle.File.ExtendedInfo.FileName) {
						newerUpdate := switchTitle.Updates[switchTitle.LatestUpdate]
						skipped[file] = SkippedFile{ReasonCode: REASON_OLD_UPDATE, ReasonText: "Old update file. A newer update exists locally.\nNew: " + filepath.Join(newerUpdate.ExtendedInfo.BaseFolder, newerUpdate.ExtendedInfo.FileName) + "\nOld: " + filepath.Join(file.BaseFolder, file.FileName)}
					}
				}
				continue
			}

			//process base
			if strings.HasSuffix(metadata.TitleId, "000") {
				metadata.Type = "Base"
				if switchTitle.BaseExist {
					if appSettings.OrganizeOptions.PrioritizeCompressed && isCompressed(file.FileName) && !isCompressed(switchTitle.File.ExtendedInfo.FileName) {
						skipped[switchTitle.File.ExtendedInfo] = SkippedFile{ReasonCode: REASON_DUPLICATE, ReasonText: "Duplicate base file. Keeping compressed version.\nOld: " + filepath.Join(switchTitle.File.ExtendedInfo.BaseFolder, switchTitle.File.ExtendedInfo.FileName) + "\nNew: " + filepath.Join(file.BaseFolder, file.FileName)}
						zap.S().Warnf("-->Duplicate base file found. Keeping compressed version [%v] over [%v]", file.FileName, switchTitle.File.ExtendedInfo.FileName)
					} else {
						skipped[file] = SkippedFile{ReasonCode: REASON_DUPLICATE, ReasonText: "Duplicate base file. Keeping existing version.\nExisting: " + filepath.Join(switchTitle.File.ExtendedInfo.BaseFolder, switchTitle.File.ExtendedInfo.FileName) + "\nDuplicate: " + filepath.Join(file.BaseFolder, file.FileName)}
						zap.S().Warnf("-->Duplicate base file found. Keeping existing version [%v] over [%v]", switchTitle.File.ExtendedInfo.FileName, file.FileName)
						continue
					}
				}
				switchTitle.File = SwitchFileInfo{ExtendedInfo: file, Metadata: metadata}
				switchTitle.BaseExist = true

				continue
			}

			if dlc, ok := switchTitle.Dlc[metadata.TitleId]; ok {
				if metadata.Version < dlc.Metadata.Version {
					if !hasBase {
						skipped[file] = SkippedFile{ReasonCode: REASON_OLD_UPDATE, ReasonText: "Old DLC file. A newer version exists locally.\nNew: " + filepath.Join(dlc.ExtendedInfo.BaseFolder, dlc.ExtendedInfo.FileName) + "\nOld: " + filepath.Join(file.BaseFolder, file.FileName)}
					}
					zap.S().Warnf("-->Old DLC file found [%v] and [%v]", file.FileName, dlc.ExtendedInfo.FileName)
					continue
				} else if metadata.Version == dlc.Metadata.Version {
					if appSettings.OrganizeOptions.PrioritizeCompressed && isCompressed(file.FileName) && !isCompressed(dlc.ExtendedInfo.FileName) {
						skipped[dlc.ExtendedInfo] = SkippedFile{ReasonCode: REASON_DUPLICATE, ReasonText: "Duplicate DLC file. Keeping compressed version.\nOld: " + filepath.Join(dlc.ExtendedInfo.BaseFolder, dlc.ExtendedInfo.FileName) + "\nNew: " + filepath.Join(file.BaseFolder, file.FileName)}
						zap.S().Warnf("-->Duplicate DLC found. Keeping compressed version [%v] over [%v]", file.FileName, dlc.ExtendedInfo.FileName)
						delete(switchTitle.Dlc, dlc.Metadata.TitleId)
					} else {
						if !hasBase {
							skipped[file] = SkippedFile{ReasonCode: REASON_DUPLICATE, ReasonText: "Duplicate DLC file. Keeping existing version.\nExisting: " + filepath.Join(dlc.ExtendedInfo.BaseFolder, dlc.ExtendedInfo.FileName) + "\nDuplicate: " + filepath.Join(file.BaseFolder, file.FileName)}
						}
						zap.S().Warnf("-->Duplicate DLC found. Keeping existing version [%v] over [%v]", dlc.ExtendedInfo.FileName, file.FileName)
						continue
					}
				}
			}
			//not an update, and not main TitleAttributes, so treat it as a DLC
			metadata.Type = "DLC"
			switchTitle.Dlc[metadata.TitleId] = SwitchFileInfo{ExtendedInfo: file, Metadata: metadata}
		}
	}

	if len(newMetadata) > 0 {
		err := ldb.db.AddEntries(DB_TABLE_FILE_SCAN_METADATA, newMetadata)
		if err != nil {
			zap.S().Warnf("failed to batch save metadata: %v", err)
		}
	}
}

// readGameMetadata extracts (or loads from the preloaded cache) the content
// metadata for a single file. It is called concurrently from the worker pool,
// so it must not touch any shared mutable state: cachedRaw is read-only, and
// all findings are returned in the metadataResult for the sequential
// classification phase to apply.
func readGameMetadata(file ExtendedFileInfo,
	filePath string,
	cachedRaw map[string][]byte) (res metadataResult) {

	// Note: strconv.FormatInt avoids the int() truncation the old key had on
	// 32-bit builds; on 64-bit builds it produces the identical string, so
	// existing deep-scan caches stay valid.
	res.fileKey = filePath + "|" + file.FileName + "|" + strconv.FormatInt(file.Size, 10)

	// Defense in depth: the switchfs parsers read many offsets/sizes straight
	// from (potentially malformed or crafted) files. A bounds panic in any of
	// them must not take down the whole scan - convert it into a skip instead.
	defer func() {
		if r := recover(); r != nil {
			zap.S().Errorf("[file:%v] recovered from panic while reading metadata: %v", file.FileName, r)
			res.contentMap = nil
			res.fresh = false
			res.skip = &SkippedFile{ReasonCode: REASON_MALFORMED_FILE, ReasonText: fmt.Sprintf("Failed to read file [Reason: %v]", r)}
			res.err = fmt.Errorf("recovered from panic: %v", r)
		}
	}()

	var metadata map[string]*switchfs.ContentMetaAttributes = nil
	keys, _ := settings.SwitchKeys()

	if keys != nil && keys.GetKey("header_key") != "" {
		if raw, ok := cachedRaw[res.fileKey]; ok {
			cached := map[string]*switchfs.ContentMetaAttributes{}
			if derr := gob.NewDecoder(bytes.NewReader(raw)).Decode(&cached); derr == nil {
				res.contentMap = cached
				return res
			} else {
				zap.S().Warnf("[file:%v] failed to decode cached metadata, re-scanning [reason: %v]", file.FileName, derr)
			}
		}

		fileName := strings.ToLower(file.FileName)
		var err error
		if strings.HasSuffix(fileName, "nsp") ||
			strings.HasSuffix(fileName, "nsz") {
			metadata, err = switchfs.ReadNspMetadata(filePath)
			if err != nil {
				res.skip = &SkippedFile{ReasonCode: REASON_MALFORMED_FILE, ReasonText: fmt.Sprintf("Failed to read NSP [Reason: %v]", err)}
				zap.S().Errorf("[file:%v] failed to read NSP [reason: %v]\n", file.FileName, err)
			}
		} else if strings.HasSuffix(fileName, "xci") ||
			strings.HasSuffix(fileName, "xcz") {
			metadata, err = switchfs.ReadXciMetadata(filePath)
			if err != nil {
				res.skip = &SkippedFile{ReasonCode: REASON_MALFORMED_FILE, ReasonText: fmt.Sprintf("Failed to read XCI [Reason: %v]", err)}
				zap.S().Errorf("[file:%v] failed to read file [reason: %v]\n", file.FileName, err)
			}
		} else if strings.HasSuffix(fileName, "00") {
			metadata, err = fileio.ReadSplitFileMetadata(filePath)
			if err != nil {
				res.skip = &SkippedFile{ReasonCode: REASON_MALFORMED_FILE, ReasonText: fmt.Sprintf("Failed to read split files [Reason: %v]", err)}
				zap.S().Errorf("[file:%v] failed to read NSP [reason: %v]\n", file.FileName, err)
			}
		}
	}

	if metadata != nil {
		res.contentMap = metadata
		res.fresh = true
		return res
	}

	//fallback to parse data from filename

	//parse title id
	titleId, _ := parseTitleIdFromFileName(file.FileName)
	version, _ := parseVersionFromFileName(file.FileName)

	if titleId == nil || version == nil {
		res.err = errors.New("unable to determine titileId / version")
		return res
	}
	res.contentMap = map[string]*switchfs.ContentMetaAttributes{
		*titleId: {TitleId: *titleId, Version: *version},
	}

	return res
}

func parseVersionFromFileName(fileName string) (*int, error) {
	res := versionRegex.FindStringSubmatch(fileName)
	if len(res) != 2 {
		return nil, errors.New("failed to parse name - no version id found")
	}
	ver, err := strconv.Atoi(res[1])
	if err != nil {
		return nil, errors.New("failed to parse name - no version id found")
	}
	return &ver, nil
}

func parseTitleIdFromFileName(fileName string) (*string, error) {
	res := titleIdRegex.FindStringSubmatch(fileName)

	if len(res) != 2 {
		return nil, errors.New("failed to parse name - no title id found")
	}
	titleId := strings.ToLower(res[1])
	return &titleId, nil
}

func ParseTitleNameFromFileName(fileName string) string {
	ind := strings.Index(fileName, "[")
	if ind != -1 {
		return fileName[:ind]
	}
	return fileName
}

func isCompressed(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.HasSuffix(lower, ".xcz") || strings.HasSuffix(lower, ".nsz")
}
