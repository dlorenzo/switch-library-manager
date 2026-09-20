package process

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/firebat20/switch-library-manager/db"
	"github.com/firebat20/switch-library-manager/settings"
	"go.uber.org/zap"
	"robpike.io/nihongo"
)

var (
	folderIllegalCharsRegex = regexp.MustCompile(`[/\\?%*:;=|"<>]`)
	nonAscii                = regexp.MustCompile("[a-zA-Z0-9áéíóú@#%&',.\\s-\\[\\]\\(\\)\\+]")
	cjk                     = regexp.MustCompile("[\u2f70-\u2FA1\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\uff66-\uff9f\\p{Katakana}\\p{Hiragana}\\p{Hangul}]")
)

// DeleteOldUpdates removes duplicate/obsolete update files identified during
// the scan. baseFolder must be the *library* folder being organized (the same
// folder passed to OrganizeByFolders): the subsequent empty-folder pruning
// walks it, and pointing it at the application's install directory would prune
// directories next to the executable instead.
func DeleteOldUpdates(baseFolder string, localDB *db.LocalSwitchFilesDB, updateProgress db.ProgressUpdater) {
	i := 0
	for k, v := range localDB.Skipped {
		switch v.ReasonCode {
		case db.REASON_DUPLICATE:
			fileToRemove := filepath.Join(k.BaseFolder, k.FileName)
			if updateProgress != nil {
				updateProgress.UpdateProgress(0, 0, "Deleting "+fileToRemove)
			}
			zap.S().Infof("Deleting file: %v \n", fileToRemove)
			err := os.Remove(fileToRemove)
			if err != nil {
				zap.S().Errorf("Failed to delete file  %v  [%v]\n", fileToRemove, err)
				continue
			}
			i++
		case db.REASON_OLD_UPDATE:
			fileToRemove := filepath.Join(k.BaseFolder, k.FileName)
			if updateProgress != nil {
				updateProgress.UpdateProgress(0, 0, "Deleting "+fileToRemove)
			}
			zap.S().Infof("Deleting file: %v \n", fileToRemove)
			err := os.Remove(fileToRemove)
			if err != nil {
				zap.S().Errorf("Failed to delete file  %v  [%v]\n", fileToRemove, err)
				continue
			}
			i++
		}

	}

	if i != 0 && settings.ReadSettings(baseFolder).OrganizeOptions.DeleteEmptyFolders {
		if updateProgress != nil {
			updateProgress.UpdateProgress(i, i+1, "Deleting empty folders...")
		}
		err := deleteEmptyFolders(baseFolder)
		if err != nil {
			zap.S().Errorf("Failed to delete empty folders [%v]\n", err)
		}
		if updateProgress != nil {
			updateProgress.UpdateProgress(i+1, i+1, "Deleting empty folders...")
		}
	}
}

func OrganizeByFolders(baseFolder string,
	localDB *db.LocalSwitchFilesDB,
	titlesDB *db.SwitchTitlesDB,
	updateProgress db.ProgressUpdater) {

	//validate template rules
	logger := zap.S()
	options := settings.ReadSettings(baseFolder).OrganizeOptions
	if !IsOptionsValid(options) {
		logger.Error("the organize options in settings.json are not valid, please check that the template contains file/folder name")
		return
	}
	i := 0
	tasksSize := len(localDB.TitlesMap) + 2
	for k, v := range localDB.TitlesMap {
		i++
		if !v.BaseExist && !options.ProcessWhenMissingBaseGame {
			continue
		}

		if updateProgress != nil {
			updateProgress.UpdateProgress(i, tasksSize, k)
		}

		var title *db.SwitchTitle
		var titleExist bool
		if titlesDB != nil && titlesDB.TitlesMap != nil {
			title, titleExist = titlesDB.TitlesMap[k]
		}
		titleName := getTitleName(title, v)

		templateData := map[string]string{}

		if titleExist {
			templateData[settings.TEMPLATE_TITLE_ID] = title.Attributes.Id
		} else if v.File.Metadata != nil {
			templateData[settings.TEMPLATE_TITLE_ID] = v.File.Metadata.TitleId
		}

		templateData[settings.TEMPLATE_TITLE_NAME] = titleName
		templateData[settings.TEMPLATE_VERSION_TXT] = ""

		if titleExist {
			templateData[settings.TEMPLATE_REGION] = title.Attributes.Region
		}

		if v.MultiContent && len(v.Updates) > 0 {
			var latestUpdate = 0
			for update := range v.Updates {
				if update > latestUpdate {
					latestUpdate = update
				}
			}
			templateData[settings.TEMPLATE_VERSION] = strconv.Itoa(latestUpdate)

			if latestUpdate > 0 && v.Updates[latestUpdate].Metadata != nil && v.Updates[latestUpdate].Metadata.Ncap != nil {
				templateData[settings.TEMPLATE_VERSION_TXT] = v.Updates[latestUpdate].Metadata.Ncap.DisplayVersion
			}
		} else {
			templateData[settings.TEMPLATE_VERSION] = "0"

			if v.File.Metadata != nil && v.File.Metadata.Ncap != nil {
				templateData[settings.TEMPLATE_VERSION_TXT] = v.File.Metadata.Ncap.DisplayVersion
			}
		}

		var destinationPath = v.File.ExtendedInfo.BaseFolder
		if destinationPath == "" {
			destinationPath = baseFolder
		}

		//create folder if needed
		if options.CreateFolderPerGame {
			folderToCreate := getFolderName(options, templateData)
			destinationPath = filepath.Join(baseFolder, folderToCreate)
			if err := createFolder(destinationPath, logger); err != nil {
				continue
			}
		}

		if v.IsSplit {
			//in case of a split file, we only rename the folder and then move all the split
			//files with the new folder
			files, err := os.ReadDir(v.File.ExtendedInfo.BaseFolder)
			if err != nil {
				continue
			}

			baseName := v.File.ExtendedInfo.FileName
			idx := len(baseName) - 1
			for idx >= 0 && baseName[idx] >= '0' && baseName[idx] <= '9' {
				idx--
			}
			prefix := baseName[:idx+1]

			for _, file := range files {
				if file.IsDir() {
					continue
				}
				name := file.Name()
				if isSplitPart(name, prefix) {
					from := filepath.Join(v.File.ExtendedInfo.BaseFolder, name)
					to := filepath.Join(destinationPath, name)
					err := moveFile(from, to)
					if err != nil {
						logger.Errorf("Failed to move file [%v]\n", err)
						continue
					}
				}
			}
			continue

		}

		var (
			from string
			to   string
			err  error
		)

		//process base title
		if v.BaseExist {
			templateData[settings.TEMPLATE_TYPE] = "BASE"
			from = filepath.Join(v.File.ExtendedInfo.BaseFolder, v.File.ExtendedInfo.FileName)
			to = filepath.Join(destinationPath, getFileName(options, v.File.ExtendedInfo.FileName, templateData, 0))
			err = moveFile(from, to)
			if err != nil {
				logger.Errorf("Failed to move file [%v]\n", err)
				continue
			}
		}

		//process updates
		for update, updateInfo := range v.Updates {
			// if the current title is multi content and the update is contained in the main file, skip
			if v.MultiContent && v.BaseExist && v.File.ExtendedInfo == updateInfo.ExtendedInfo {
				logger.Infof("Skipping organizing %v update %v, reason: Update is multi-part with main file", titleName, update)
				continue
			}

			if updateInfo.Metadata != nil {
				templateData[settings.TEMPLATE_TITLE_ID] = updateInfo.Metadata.TitleId
			}
			templateData[settings.TEMPLATE_VERSION] = strconv.Itoa(update)
			templateData[settings.TEMPLATE_TYPE] = "UPD"
			// Metadata itself can be nil here; only the TitleId assignment above
			// was guarded. Check Metadata before reaching into Ncap.
			if updateInfo.Metadata != nil && updateInfo.Metadata.Ncap != nil {
				templateData[settings.TEMPLATE_VERSION_TXT] = updateInfo.Metadata.Ncap.DisplayVersion
			} else {
				templateData[settings.TEMPLATE_VERSION_TXT] = ""
			}

			from = filepath.Join(updateInfo.ExtendedInfo.BaseFolder, updateInfo.ExtendedInfo.FileName)
			if options.CreateFolderPerGame {
				if options.UpdatesFolder != "" {
					targetDir, err := resolveSubDir(destinationPath, options.UpdatesFolder)
					if err != nil {
						logger.Errorf("Invalid updates folder [%v]: %v", options.UpdatesFolder, err)
						continue
					}
					if err := createFolder(targetDir, logger); err != nil {
						continue
					}
					to = filepath.Join(targetDir, getFileName(options, updateInfo.ExtendedInfo.FileName, templateData, 0))
				} else {
					to = filepath.Join(destinationPath, getFileName(options, updateInfo.ExtendedInfo.FileName, templateData, 0))
				}
			} else {
				if options.UpdatesFolder != "" {
					targetDir, err := resolveSubDir(baseFolder, options.UpdatesFolder)
					if err != nil {
						logger.Errorf("Invalid updates folder [%v]: %v", options.UpdatesFolder, err)
						continue
					}
					if err := createFolder(targetDir, logger); err != nil {
						continue
					}
					to = filepath.Join(targetDir, getFileName(options, updateInfo.ExtendedInfo.FileName, templateData, 0))
				} else {
					to = filepath.Join(updateInfo.ExtendedInfo.BaseFolder, getFileName(options, updateInfo.ExtendedInfo.FileName, templateData, 0))
				}
			}
			err := moveFile(from, to)
			if err != nil {
				logger.Errorf("Failed to move file [%v]\n", err)
				continue
			}
		}

		//process DLC
		existingDlcs := map[string]string{}
		for id, dlc := range v.Dlc {
			// if the current title is multi content and the dlc is contained in the main file, skip
			if v.MultiContent && v.BaseExist && v.File.ExtendedInfo == dlc.ExtendedInfo {
				logger.Infof("Skipping organizing %v dlc %v, reason: DLC is multi-part with main file", titleName, dlc)
				continue
			}

			if dlc.Metadata != nil {
				templateData[settings.TEMPLATE_VERSION] = strconv.Itoa(dlc.Metadata.Version)
			}
			templateData[settings.TEMPLATE_TYPE] = "DLC"
			templateData[settings.TEMPLATE_TITLE_ID] = id
			templateData[settings.TEMPLATE_DLC_NAME] = getDlcName(title, dlc)
			from = filepath.Join(dlc.ExtendedInfo.BaseFolder, dlc.ExtendedInfo.FileName)

			dlcNameTry := 0
			to = ""
			for {
				if options.CreateFolderPerGame {
					if options.DlcFolder != "" {
						targetDir, err := resolveSubDir(destinationPath, options.DlcFolder)
						if err != nil {
							logger.Errorf("Invalid DLC folder [%v]: %v", options.DlcFolder, err)
							break
						}
						if err := createFolder(targetDir, logger); err != nil {
							break
						}
						to = filepath.Join(targetDir, getFileName(options, dlc.ExtendedInfo.FileName, templateData, dlcNameTry))
					} else {
						to = filepath.Join(destinationPath, getFileName(options, dlc.ExtendedInfo.FileName, templateData, dlcNameTry))
					}
				} else {
					if options.DlcFolder != "" {
						targetDir, err := resolveSubDir(baseFolder, options.DlcFolder)
						if err != nil {
							logger.Errorf("Invalid DLC folder [%v]: %v", options.DlcFolder, err)
							break
						}
						if err := createFolder(targetDir, logger); err != nil {
							break
						}
						to = filepath.Join(targetDir, getFileName(options, dlc.ExtendedInfo.FileName, templateData, dlcNameTry))
					} else {
						to = filepath.Join(dlc.ExtendedInfo.BaseFolder, getFileName(options, dlc.ExtendedInfo.FileName, templateData, dlcNameTry))
					}
				}

				// Check if this dlc will generate the same destination name as a
				// previously-placed dlc. If the name is free, or it already maps to
				// THIS dlc's id, we can use it. Otherwise it's a different dlc that
				// happens to render to the same name - bump the numeric suffix and
				// try again so we don't clobber the other file.
				existingID, exists := existingDlcs[to]
				if !exists || existingID == id {
					break
				}

				dlcNameTry++
				if dlcNameTry > 100 {
					logger.Errorf("Too many DLC name collisions for %v", id)
					to = ""
					break
				}
			}
			if to == "" {
				continue
			}
			existingDlcs[to] = id

			err = moveFile(from, to)
			if err != nil {
				logger.Errorf("Failed to move file [%v]\n", err)
				continue
			}
		}
	}

	if options.DeleteEmptyFolders {
		if updateProgress != nil {
			i += 1
			updateProgress.UpdateProgress(i, tasksSize, "Deleting empty folders...")
		}
		err := deleteEmptyFolders(baseFolder)
		if err != nil {
			zap.S().Errorf("Failed to delete empty folders [%v]\n", err)
		}
		if updateProgress != nil {
			i += 1
			updateProgress.UpdateProgress(i, tasksSize, "Done")
		}
	} else {
		if updateProgress != nil {
			i += 2
			updateProgress.UpdateProgress(i, tasksSize, "Done")
		}
	}
}

func IsOptionsValid(options settings.OrganizeOptions) bool {
	if options.RenameFiles {
		if options.FileNameTemplate == "" {
			zap.S().Error("file name template cannot be empty")
			return false
		}
		if !strings.Contains(options.FileNameTemplate, settings.TEMPLATE_TITLE_NAME) &&
			!strings.Contains(options.FileNameTemplate, settings.TEMPLATE_TITLE_ID) {
			zap.S().Error("file name template needs to contain one of the following - titleId or title name")
			return false
		}

	}

	if options.CreateFolderPerGame {
		if options.FolderNameTemplate == "" {
			zap.S().Error("folder name template cannot be empty")
			return false
		}
		if !strings.Contains(options.FolderNameTemplate, settings.TEMPLATE_TITLE_NAME) &&
			!strings.Contains(options.FolderNameTemplate, settings.TEMPLATE_TITLE_ID) {
			zap.S().Error("folder name template needs to contain one of the following - titleId or title name")
			return false
		}
	}
	return true
}

func getDlcName(switchTitle *db.SwitchTitle, file db.SwitchFileInfo) string {
	if switchTitle == nil || file.Metadata == nil {
		return ""
	}
	if dlcAttributes, ok := switchTitle.Dlc[file.Metadata.TitleId]; ok {
		name := dlcAttributes.Name
		name = strings.ReplaceAll(name, "\n", " ")
		name = strings.ReplaceAll(name, "\r", " ")
		return name
	}
	return ""
}

func getTitleName(switchTitle *db.SwitchTitle, v *db.SwitchGameFiles) string {
	// Check if switchTitle is not nil and contains a name
	if switchTitle != nil && switchTitle.Attributes.Name != "" {
		res := cjk.FindAllString(switchTitle.Attributes.Name, -1)
		if len(res) == 0 {
			return sanitizeTitleName(switchTitle.Attributes.Name)
		}
	}

	// Check if v and its Metadata are present
	if v != nil && v.File.Metadata != nil && v.File.Metadata.Ncap != nil {
		// Check if the title name exists
		name := v.File.Metadata.Ncap.TitleName["AmericanEnglish"].Title
		if name != "" {
			return sanitizeTitleName(name)
		}
	}

	// For non-eShop games, get the name from the file
	if v != nil {
		return sanitizeTitleName(db.ParseTitleNameFromFileName(v.File.ExtendedInfo.FileName))
	}

	// Default return if no valid name is found
	return "Unknown Title"
}

func sanitizeTitleName(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7F {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	if s == "" {
		return "Unknown Title"
	}
	return s
}

// resolveSubDir joins a user-configured subfolder onto base and ensures the
// result stays inside base (rejects absolute paths and ../../ escapes).
func resolveSubDir(base, sub string) (string, error) {
	if sub == "" {
		return base, nil
	}
	if filepath.IsAbs(sub) {
		return "", errors.New("absolute subfolder not allowed: " + sub)
	}
	joined := filepath.Join(base, sub)
	rel, err := filepath.Rel(base, joined)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("subfolder escapes library root: " + sub)
	}
	return joined, nil
}

func getFolderName(options settings.OrganizeOptions, templateData map[string]string) string {

	return applyTemplate(templateData, options.SwitchSafeFileNames, options.FolderNameTemplate, 0)
}

func getFileName(options settings.OrganizeOptions, originalName string, templateData map[string]string, nameTry int) string {
	if !options.RenameFiles || strings.Contains(strings.ToLower(originalName), "[nr]") {
		return originalName
	}
	ext := path.Ext(originalName)
	result := applyTemplate(templateData, options.SwitchSafeFileNames, options.FileNameTemplate, nameTry)
	return result + ext
}

func moveFile(from string, to string) error {
	if from == to {
		return nil
	}

	// os.Rename silently overwrites an existing destination on most platforms.
	// Before moving, make sure we are not about to clobber a *different* file.
	// os.SameFile handles case-insensitive filesystems and hardlinks, so a
	// rename that only changes casing of the same underlying file still works.
	if toInfo, err := os.Stat(to); err == nil {
		if fromInfo, ferr := os.Stat(from); ferr == nil && !os.SameFile(fromInfo, toInfo) {
			zap.S().Warnf("Refusing to overwrite existing file: %v", to)
			return errors.New("destination already exists: " + to)
		}
	}

	err := os.Rename(from, to)
	if err == nil {
		return nil
	}

	// Fallback to copy and delete if rename fails (e.g. cross-device link error across partitions/drives)
	return copyAndDelete(from, to)
}

func copyAndDelete(from string, to string) error {
	srcFile, err := os.Open(from)
	if err != nil {
		return err
	}

	srcInfo, err := srcFile.Stat()
	if err != nil {
		srcFile.Close()
		return err
	}

	dstFile, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, srcInfo.Mode())
	if err != nil {
		srcFile.Close()
		return err
	}

	_, err = io.Copy(dstFile, srcFile)
	syncErr := dstFile.Sync()
	closeErr := dstFile.Close()
	// Windows locks open files: release the source handle before removing it.
	srcCloseErr := srcFile.Close()
	if err != nil {
		_ = os.Remove(to)
		return err
	}
	if syncErr != nil {
		_ = os.Remove(to)
		return syncErr
	}
	if closeErr != nil {
		_ = os.Remove(to)
		return closeErr
	}
	if srcCloseErr != nil {
		return srcCloseErr
	}

	return os.Remove(from)
}

func applyTemplate(templateData map[string]string, useSafeNames bool, template string, nameTry int) string {
	result := strings.Replace(template, "{"+settings.TEMPLATE_TITLE_NAME+"}", templateData[settings.TEMPLATE_TITLE_NAME], 1)
	result = strings.Replace(result, "{"+settings.TEMPLATE_TITLE_ID+"}", strings.ToUpper(templateData[settings.TEMPLATE_TITLE_ID]), 1)
	result = strings.Replace(result, "{"+settings.TEMPLATE_VERSION+"}", templateData[settings.TEMPLATE_VERSION], 1)
	result = strings.Replace(result, "{"+settings.TEMPLATE_TYPE+"}", templateData[settings.TEMPLATE_TYPE], 1)
	result = strings.Replace(result, "{"+settings.TEMPLATE_VERSION_TXT+"}", templateData[settings.TEMPLATE_VERSION_TXT], 1)
	result = strings.Replace(result, "{"+settings.TEMPLATE_REGION+"}", templateData[settings.TEMPLATE_REGION], 1)

	//remove title name from dlc name
	dlcName := strings.Replace(templateData[settings.TEMPLATE_DLC_NAME], templateData[settings.TEMPLATE_TITLE_NAME], "", 1)
	dlcName = strings.TrimSpace(dlcName)
	dlcName = strings.TrimPrefix(dlcName, "-")
	dlcName = strings.TrimSpace(dlcName)

	result = strings.Replace(result, "{"+settings.TEMPLATE_DLC_NAME+"}", dlcName, 1)
	result = strings.ReplaceAll(result, "[]", "")
	result = strings.ReplaceAll(result, "()", "")
	result = strings.ReplaceAll(result, "<>", "")

	result = strings.TrimSuffix(result, ".")

	if nameTry > 0 {
		result = result + "(" + strconv.Itoa(nameTry) + ")"
	}

	if useSafeNames {
		result = nihongo.RomajiString(result)

		// handle known characters that have safe variants
		result = strings.ReplaceAll(result, "ō", "o")

		safe := nonAscii.FindAllString(result, -1)
		result = strings.Join(safe, "")
	}

	space := regexp.MustCompile(`\s+`)
	result = space.ReplaceAllString(result, " ")

	result = strings.TrimSpace(result)
	result = folderIllegalCharsRegex.ReplaceAllString(result, "")

	// The illegal-chars regex above does not strip '.', so a crafted title name
	// (from titles.json or NACP metadata) could still be "." or ".." and escape
	// the library root once joined. Neutralize any pure dot-segments.
	if result == "." || result == ".." {
		return ""
	}

	return result
}

func createFolder(path string, logger *zap.SugaredLogger) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// 0755 rather than os.ModePerm (0777): the created library folders
		// should not be world-writable. MkdirAll also creates any missing
		// parent directories, unlike Mkdir.
		err = os.MkdirAll(path, 0755)
		if err != nil {
			logger.Errorf("Failed to create folder %v - %v\n", path, err)
			return err
		}
	}
	return nil
}

// deleteEmptyFolders removes empty directories under path (never path itself),
// cascading upwards: a directory whose only contents were empty directories is
// removed as well.
//
// The previous implementation walked the whole tree to collect directories and
// then issued a separate os.ReadDir per directory to test emptiness - two full
// listings of every directory. This version counts each directory's children
// during a single WalkDir pass and maintains those counts as it deletes, so no
// re-listing is needed at all.
func deleteEmptyFolders(path string) error {
	path = filepath.Clean(path)
	childCount := map[string]int{}
	var dirs []string

	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			zap.S().Error("Error while scanning for empty folders", err)
			return nil
		}
		if p == path {
			return nil
		}
		childCount[filepath.Dir(p)]++
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Sort directories by path length in descending order to process deepest first (bottom-up)
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})

	for _, dir := range dirs {
		if childCount[dir] != 0 {
			continue
		}
		zap.S().Infof("\nDeleting empty folder [%v]", dir)
		if rmErr := os.Remove(dir); rmErr != nil {
			zap.S().Error("Error while deleting empty folder", rmErr)
			continue
		}
		// The parent just lost one child; it may now be empty and will be
		// visited later in this loop (parents sort after their children).
		childCount[filepath.Dir(dir)]--
	}
	return nil
}

func isSplitPart(fileName string, prefix string) bool {
	if !strings.HasPrefix(fileName, prefix) {
		return false
	}
	suffix := fileName[len(prefix):]
	if len(suffix) == 0 {
		return false
	}
	for i := 0; i < len(suffix); i++ {
		if suffix[i] < '0' || suffix[i] > '9' {
			return false
		}
	}
	return true
}
