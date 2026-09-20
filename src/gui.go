package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asticode/go-astikit"
	"github.com/firebat20/go-astilectron"
	bootstrap "github.com/firebat20/go-astilectron-bootstrap"
	"github.com/firebat20/switch-library-manager/db"
	"github.com/firebat20/switch-library-manager/process"
	"github.com/firebat20/switch-library-manager/settings"
	"go.uber.org/zap"
)

type Pair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Type  int    `json:"type"`
}

type LocalLibraryData struct {
	LibraryData []LibraryTemplateData `json:"library_data"`
	Issues      []Pair                `json:"issues"`
	NumFiles    int                   `json:"num_files"`
}

type SwitchTitle struct {
	Name        string `json:"name"`
	TitleId     string `json:"titleId"`
	Icon        string `json:"icon"`
	Region      string `json:"region"`
	ReleaseDate string `json:"release_date"`
}

type LibraryTemplateData struct {
	Id      int    `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Dlc     string `json:"dlc"`
	TitleId string `json:"titleId"`
	Path    string `json:"path"`
	Icon    string `json:"icon"`
	Update  int    `json:"update"`
	Region  string `json:"region"`
	Type    string `json:"type"`
}

type ProgressUpdate struct {
	Curr    int    `json:"curr"`
	Total   int    `json:"total"`
	Message string `json:"message"`
}

type State struct {
	sync.Mutex
	switchDB *db.SwitchTitlesDB
	localDB  *db.LocalSwitchFilesDB
	window   *astilectron.Window
}

type Message struct {
	Name    string `json:"name"`
	Payload string `json:"payload"`
}

type GUI struct {
	state          State
	baseFolder     string
	localDbManager *db.LocalSwitchDBManager
	sugarLogger    *zap.SugaredLogger
}

func CreateGUI(baseFolder string, sugarLogger *zap.SugaredLogger) *GUI {
	return &GUI{state: State{}, baseFolder: baseFolder, sugarLogger: sugarLogger}
}
func (g *GUI) Start() {

	localDbManager, err := db.NewLocalSwitchDBManager(g.baseFolder)
	if err != nil {
		g.sugarLogger.Error("Failed to create local files db\n", err)
		return
	}

	settings.InitSwitchKeys(g.baseFolder)

	g.localDbManager = localDbManager
	defer localDbManager.Close()

	settingsObj := settings.ReadSettings(g.baseFolder)

	// Run bootstrap
	if err := bootstrap.Run(bootstrap.Options{
		Asset:    Asset,
		AssetDir: AssetDir,
		AstilectronOptions: astilectron.Options{
			AppName:            "Switch Library Manager (" + settings.SLM_VERSION + ")",
			AcceptTCPTimeout:   time.Duration(5) * time.Second,
			AppIconDarwinPath:  "resources/icon.icns",
			AppIconDefaultPath: "resources/icon.png",
			SingleInstance:     true,
		},
		Debug:         false,
		Logger:        log.New(log.Writer(), log.Prefix(), log.Flags()),
		RestoreAssets: RestoreAssets,
		Windows: []*bootstrap.Window{{
			Homepage: "app.html",
			Adapter: func(w *astilectron.Window) {
				g.state.window = w
				g.state.window.OnMessage(g.handleMessage)
			},
			Options: &astilectron.WindowOptions{
				AlwaysOnTop:     astikit.BoolPtr(true),
				BackgroundColor: astikit.StrPtr("#333"),
				Center:          astikit.BoolPtr(true),
				Height:          astikit.IntPtr(settingsObj.WindowHeight),
				Width:           astikit.IntPtr(settingsObj.WindowWidth),
				// SECURITY NOTE: EnableRemoteModule is set to false. All renderer
				// interactions (dialogs, file explorer actions, window state)
				// are handled via Astilectron IPC messages to Go.
				WebPreferences: &astilectron.WebPreferences{EnableRemoteModule: astikit.BoolPtr(false)},
			},
		}},
	}); err != nil {
		g.sugarLogger.Error(fmt.Errorf("running bootstrap failed: %w", err))
		log.Fatal(err)
	}
}

func (g *GUI) sendToUI(name, payload string) {
	g.state.Lock()
	w := g.state.window
	g.state.Unlock()
	if w == nil {
		g.sugarLogger.Warnf("cannot send %s: window not ready", name)
		return
	}
	w.SendMessage(Message{Name: name, Payload: payload}, func(m *astilectron.EventMessage) {})
}

func (g *GUI) handleMessage(m *astilectron.EventMessage) interface{} {
	var retValue string
	msg := Message{}
	err := m.Unmarshal(&msg)

	if err != nil {
		g.sugarLogger.Error("Failed to parse client message", err)
		return ""
	}

	g.sugarLogger.Debugf("Received message from client [%v]", msg)

	switch msg.Name {
	case "organize":
		g.organizeLibrary()
	case "isKeysFileAvailable":
		keys, _ := settings.SwitchKeys()
		retValue = strconv.FormatBool(keys != nil && keys.GetKey("header_key") != "")
	case "loadSettings":
		retValue = g.loadSettings()

		g.state.Lock()
		w := g.state.window
		g.state.Unlock()
		if w != nil {
			w.SetAlwaysOnTop(false)
		}
	case "saveSettings":
		err = g.saveSettings(msg.Payload)
		if err != nil {
			g.sugarLogger.Error(err)
			g.sendToUI("error", err.Error())
			return ""
		}
	case "missingGames":
		missingGames := g.getMissingGames()
		msgBytes, err := json.Marshal(missingGames)
		if err != nil {
			g.sugarLogger.Error(err)
			g.sendToUI("error", err.Error())
			return ""
		}
		g.sendToUI("missingGames", string(msgBytes))
	case "checkMaximized":
		settingsObj := settings.ReadSettings(g.baseFolder)
		retValue = strconv.FormatBool(settingsObj.WindowMaximized)
	case "updateLocalLibrary":
		ignoreCache, _ := strconv.ParseBool(msg.Payload)
		localDB, err := g.buildLocalDB(g.localDbManager, ignoreCache)
		if err != nil {
			g.sugarLogger.Error(err)
			g.sendToUI("error", err.Error())
			return ""
		}
		response := LocalLibraryData{}
		libraryData := []LibraryTemplateData{}
		issues := []Pair{}
		for k, v := range localDB.TitlesMap {
			if v.BaseExist {
				version := ""
				name := ""
				if v.File.Metadata != nil && v.File.Metadata.Ncap != nil {
					version = v.File.Metadata.Ncap.DisplayVersion
					name = v.File.Metadata.Ncap.TitleName["AmericanEnglish"].Title
				}

				if v.Updates != nil && len(v.Updates) != 0 {
					// v.LatestUpdate may not be a key in the map; indexing a
					// missing key yields a zero-value SwitchFileInfo whose
					// Metadata is nil, so dereferencing .Metadata.Ncap panics.
					// Look the entry up explicitly and nil-check every hop.
					if latest, ok := v.Updates[v.LatestUpdate]; ok &&
						latest.Metadata != nil && latest.Metadata.Ncap != nil {
						version = latest.Metadata.Ncap.DisplayVersion
					} else {
						version = ""
					}
				}
				var title *db.SwitchTitle
				var titleOk bool
				g.state.Lock()
				if g.state.switchDB != nil && g.state.switchDB.TitlesMap != nil {
					title, titleOk = g.state.switchDB.TitlesMap[k]
				}
				g.state.Unlock()
				if titleOk && title != nil {
					if title.Attributes.Name != "" {
						name = title.Attributes.Name
					}
					libraryData = append(libraryData,
						LibraryTemplateData{
							Icon:    title.Attributes.IconUrl,
							Name:    name,
							TitleId: title.Attributes.Id,
							Update:  v.LatestUpdate,
							Version: version,
							Region:  title.Attributes.Region,
							Type:    getType(v),
							Path:    filepath.Join(v.File.ExtendedInfo.BaseFolder, v.File.ExtendedInfo.FileName),
						})
				} else {
					if name == "" {
						name = db.ParseTitleNameFromFileName(v.File.ExtendedInfo.FileName)
					}
					titleId := ""
					if v.File.Metadata != nil {
						titleId = v.File.Metadata.TitleId
					}
					libraryData = append(libraryData,
						LibraryTemplateData{
							Name:    name,
							Update:  v.LatestUpdate,
							Version: version,
							Type:    getType(v),
							TitleId: titleId,
							Path:    filepath.Join(v.File.ExtendedInfo.BaseFolder, v.File.ExtendedInfo.FileName),
						})
				}

			} else {
				for _, update := range v.Updates {
					issues = append(issues, Pair{Key: filepath.Join(update.ExtendedInfo.BaseFolder, update.ExtendedInfo.FileName), Value: "Base file is missing", Type: db.REASON_MISSING_BASE})
				}
				for _, dlc := range v.Dlc {
					issues = append(issues, Pair{Key: filepath.Join(dlc.ExtendedInfo.BaseFolder, dlc.ExtendedInfo.FileName), Value: "Base file is missing", Type: db.REASON_MISSING_BASE})
				}
			}
		}
		for k, v := range localDB.Skipped {
			issues = append(issues, Pair{Key: filepath.Join(k.BaseFolder, k.FileName), Value: v.ReasonText, Type: v.ReasonCode})
		}

		response.LibraryData = libraryData
		response.NumFiles = localDB.NumFiles
		response.Issues = issues
		msgBytes, err := json.Marshal(response)
		if err != nil {
			g.sugarLogger.Error(err)
			g.sendToUI("error", err.Error())
			return ""
		}
		g.sendToUI("libraryLoaded", string(msgBytes))
	case "updateDB":
		g.state.Lock()
		needsBuild := g.state.switchDB == nil
		g.state.Unlock()
		if needsBuild {
			switchDb, err := g.buildSwitchDb()
			if err != nil {
				g.sugarLogger.Error(err)
				g.sendToUI("error", err.Error())
				return ""
			}
			g.state.Lock()
			g.state.switchDB = switchDb
			g.state.Unlock()
		}
	case "hardRescan":
		_ = g.localDbManager.ClearScanData()
		g.sendToUI("rescan", "")
	case "missingUpdates":
		retValue = g.getMissingUpdates()
	case "showItemInFolder":
		openItemInFolder(msg.Payload)
	case "maximizeWindow":
		g.state.Lock()
		w := g.state.window
		g.state.Unlock()
		if w != nil {
			_ = w.Maximize()
		}
	case "missingDlc":
		retValue = g.getMissingDLC()
	case "checkUpdate":
		newUpdate, err := settings.CheckForUpdates()
		if err != nil {
			g.sugarLogger.Error(err)
			if !strings.Contains(err.Error(), "dial tcp") {
				g.sendToUI("error", err.Error())
			}
		}
		retValue = strconv.FormatBool(newUpdate)
	}

	g.sugarLogger.Debugf("Server response [%v]", retValue)

	return retValue
}

func openItemInFolder(targetPath string) {
	if targetPath == "" {
		return
	}
	clean := filepath.Clean(targetPath)
	if !filepath.IsAbs(clean) {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", "/select,", clean)
	case "darwin":
		cmd = exec.Command("open", "-R", clean)
	default:
		cmd = exec.Command("xdg-open", filepath.Dir(clean))
	}
	_ = cmd.Run()
}

func getType(gameFile *db.SwitchGameFiles) string {
	if gameFile.IsSplit {
		return "split"
	}
	if gameFile.MultiContent {
		return "multi-content"
	}
	ext := filepath.Ext(gameFile.File.ExtendedInfo.FileName)
	if len(ext) > 1 {
		return ext[1:]
	}
	return ""
}

func (g *GUI) saveSettings(settingsJson string) error {
	s := settings.AppSettings{}
	if err := json.Unmarshal([]byte(settingsJson), &s); err != nil {
		return err
	}
	if s.GuiPagingSize <= 0 {
		s.GuiPagingSize = 100
	}
	if s.TitlesJsonUrl == "" {
		s.TitlesJsonUrl = settings.DEFAULT_TITLES_JSON_URL
	}
	if s.VersionsJsonUrl == "" {
		s.VersionsJsonUrl = settings.DEFAULT_VERSIONS_JSON_URL
	}
	if s.WindowWidth <= 0 {
		s.WindowWidth = 1200
	}
	if s.WindowHeight <= 0 {
		s.WindowHeight = 600
	}
	settings.SaveSettings(&s, g.baseFolder)
	return nil
}

func (g *GUI) getMissingDLC() string {
	g.state.Lock()
	localDB := g.state.localDB
	switchDB := g.state.switchDB
	g.state.Unlock()
	if localDB == nil || localDB.TitlesMap == nil || switchDB == nil || switchDB.TitlesMap == nil {
		return "[]"
	}
	settingsObj := settings.ReadSettings(g.baseFolder)
	ignoreIds := map[string]struct{}{}
	for _, id := range settingsObj.IgnoreDLCTitleIds {
		ignoreIds[strings.ToLower(id)] = struct{}{}
	}
	missingDLC := process.ScanForMissingDLC(localDB.TitlesMap, switchDB.TitlesMap, ignoreIds)
	values := make([]process.IncompleteTitle, 0, len(missingDLC))
	for _, missingUpdate := range missingDLC {
		values = append(values, missingUpdate)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Attributes.Id < values[j].Attributes.Id })

	msg, err := json.Marshal(values)
	if err != nil {
		g.sugarLogger.Error(err)
		return "[]"
	}
	return string(msg)
}

func (g *GUI) getMissingUpdates() string {
	g.state.Lock()
	localDB := g.state.localDB
	switchDB := g.state.switchDB
	g.state.Unlock()
	if localDB == nil || localDB.TitlesMap == nil || switchDB == nil || switchDB.TitlesMap == nil {
		return "[]"
	}
	settingsObj := settings.ReadSettings(g.baseFolder)
	ignoreIds := map[string]struct{}{}
	for _, id := range settingsObj.IgnoreUpdateTitleIds {
		ignoreIds[strings.ToLower(id)] = struct{}{}
	}
	missingUpdates := process.ScanForMissingUpdates(localDB.TitlesMap, switchDB.TitlesMap, ignoreIds, settingsObj.IgnoreDLCUpdates)
	values := make([]process.IncompleteTitle, 0, len(missingUpdates))
	for _, missingUpdate := range missingUpdates {
		values = append(values, missingUpdate)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Attributes.Id < values[j].Attributes.Id })

	msg, err := json.Marshal(values)
	if err != nil {
		g.sugarLogger.Error(err)
		return "[]"
	}
	return string(msg)
}

func (g *GUI) loadSettings() string {
	return settings.ReadSettingsAsJSON(g.baseFolder)
}

func (g *GUI) buildSwitchDb() (*db.SwitchTitlesDB, error) {
	settingsObj := settings.ReadSettings(g.baseFolder)
	//1. load the titles JSON object
	g.UpdateProgress(1, 4, "Downloading titles.json")
	filename := filepath.Join(g.baseFolder, settings.TITLE_JSON_FILENAME)
	titleFile, titlesEtag, err := db.LoadAndUpdateFile(settingsObj.TitlesJsonUrl, filename, settingsObj.TitlesEtag)
	if err != nil {
		if titleFile != nil {
			titleFile.Close()
		}
		return nil, fmt.Errorf("failed to download switch titles [reason:%w]", err)
	}
	settingsObj.TitlesEtag = titlesEtag

	g.UpdateProgress(2, 4, "Downloading versions.json")
	filename = filepath.Join(g.baseFolder, settings.VERSIONS_JSON_FILENAME)
	versionsFile, versionsEtag, err := db.LoadAndUpdateFile(settingsObj.VersionsJsonUrl, filename, settingsObj.VersionsEtag)
	if err != nil {
		if titleFile != nil {
			titleFile.Close()
		}
		if versionsFile != nil {
			versionsFile.Close()
		}
		return nil, fmt.Errorf("failed to download switch updates [reason:%w]", err)
	}
	settingsObj.VersionsEtag = versionsEtag

	settings.SaveSettings(settingsObj, g.baseFolder)

	g.UpdateProgress(3, 4, "Processing switch titles and updates")
	switchTitleDB, err := db.CreateSwitchTitleDB(titleFile, versionsFile)
	if err != nil {
		g.UpdateProgress(4, 4, "Failed")
	} else {
		g.UpdateProgress(4, 4, "Finishing up...")
	}
	return switchTitleDB, err
}

func (g *GUI) buildLocalDB(localDbManager *db.LocalSwitchDBManager, ignoreCache bool) (*db.LocalSwitchFilesDB, error) {
	cfg := settings.ReadSettings(g.baseFolder)
	folderToScan := cfg.Folder
	recursiveMode := cfg.ScanRecursively

	scanFolders := append([]string{}, cfg.ScanFolders...)
	if folderToScan != "" {
		scanFolders = append(scanFolders, folderToScan)
	}
	localDB, err := localDbManager.CreateLocalSwitchFilesDB(scanFolders, g, recursiveMode, ignoreCache)
	g.state.Lock()
	g.state.localDB = localDB
	g.state.Unlock()
	return localDB, err
}

func (g *GUI) organizeLibrary() {
	g.state.Lock()
	localDB := g.state.localDB
	switchDB := g.state.switchDB
	g.state.Unlock()
	if localDB == nil {
		zap.S().Error("local library is not loaded, cannot organize")
		g.sendToUI("error", "Local library is not loaded. Please scan your library first.")
		return
	}
	folderToScan := settings.ReadSettings(g.baseFolder).Folder
	options := settings.ReadSettings(g.baseFolder).OrganizeOptions
	if !process.IsOptionsValid(options) {
		zap.S().Error("the organize options in settings.json are not valid, please check that the template contains file/folder name")
		g.sendToUI("error", "the organize options in settings.json are not valid, please check that the template contains file/folder name")
		return
	}
	if settings.ReadSettings(g.baseFolder).OrganizeOptions.DeleteOldUpdateFiles {
		// Clean within the library folder being organized, not the app's
		// install directory (g.baseFolder).
		process.DeleteOldUpdates(folderToScan, localDB, g)
	}
	process.OrganizeByFolders(folderToScan, localDB, switchDB, g)
}

func (g *GUI) UpdateProgress(curr int, total int, message string) {
	if curr < 0 {
		curr = 0
	}
	if total <= 0 {
		total = 1
	}
	if curr > total {
		curr = total
	}
	progressMessage := ProgressUpdate{curr, total, message}
	g.sugarLogger.Debugf("%v (%v/%v)", message, curr, total)
	msg, err := json.Marshal(progressMessage)
	if err != nil {
		g.sugarLogger.Error(err)
		return
	}

	g.state.Lock()
	w := g.state.window
	g.state.Unlock()
	if w == nil {
		return
	}
	w.SendMessage(Message{Name: "updateProgress", Payload: string(msg)}, func(m *astilectron.EventMessage) {})
}

func (g *GUI) getMissingGames() []SwitchTitle {
	result := []SwitchTitle{}
	g.state.Lock()
	switchDB := g.state.switchDB
	localDB := g.state.localDB
	g.state.Unlock()
	if switchDB == nil || switchDB.TitlesMap == nil || localDB == nil || localDB.TitlesMap == nil {
		return result
	}
	options := settings.ReadSettings(g.baseFolder)
	for k, v := range switchDB.TitlesMap {
		if _, ok := localDB.TitlesMap[k]; ok {
			continue
		}
		if v.Attributes.Name == "" || v.Attributes.Id == "" {
			continue
		}

		if options.HideDemoGames && v.Attributes.IsDemo {
			continue
		}

		result = append(result, SwitchTitle{
			TitleId:     v.Attributes.Id,
			Name:        v.Attributes.Name,
			Icon:        v.Attributes.BannerUrl,
			Region:      v.Attributes.Region,
			ReleaseDate: v.Attributes.ParsedReleaseDate,
		})
	}
	return result

}
