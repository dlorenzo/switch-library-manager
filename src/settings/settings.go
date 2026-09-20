package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hashicorp/go-version"
	"go.uber.org/zap"
)

var (
	settingsInstance *AppSettings
	settingsMu       sync.RWMutex
)

const (
	SETTINGS_FILENAME         = "settings.json"
	TITLE_JSON_FILENAME       = "titles.json"
	VERSIONS_JSON_FILENAME    = "versions.json"
	SLM_VERSION               = "1.13.0"
	DEFAULT_TITLES_JSON_URL   = "https://tinfoil.io/repo/db/titles.json"
	DEFAULT_VERSIONS_JSON_URL = "https://raw.githubusercontent.com/blawar/titledb/master/versions.json"
	SLM_VERSION_URL           = "https://raw.githubusercontent.com/firebat20/switch-library-manager/master/version.json"
)

const (
	TEMPLATE_TITLE_ID    = "TITLE_ID"
	TEMPLATE_TITLE_NAME  = "TITLE_NAME"
	TEMPLATE_DLC_NAME    = "DLC_NAME"
	TEMPLATE_VERSION     = "VERSION"
	TEMPLATE_REGION      = "REGION"
	TEMPLATE_VERSION_TXT = "VERSION_TXT"
	TEMPLATE_TYPE        = "TYPE"
)

type OrganizeOptions struct {
	CreateFolderPerGame        bool   `json:"create_folder_per_game"`
	DlcFolder                  string `json:"dlc_folder"`
	UpdatesFolder              string `json:"updates_folder"`
	RenameFiles                bool   `json:"rename_files"`
	DeleteEmptyFolders         bool   `json:"delete_empty_folders"`
	DeleteOldUpdateFiles       bool   `json:"delete_old_update_files"`
	FolderNameTemplate         string `json:"folder_name_template"`
	SwitchSafeFileNames        bool   `json:"switch_safe_file_names"`
	FileNameTemplate           string `json:"file_name_template"`
	ProcessWhenMissingBaseGame bool   `json:"process_when_missing_base_game"`
	PrioritizeCompressed       bool   `json:"prioritize_compressed"`
}

type AppSettings struct {
	VersionsJsonUrl        string          `json:"versions_json_url"`
	VersionsEtag           string          `json:"versions_etag"`
	TitlesJsonUrl          string          `json:"titles_json_url"`
	TitlesEtag             string          `json:"titles_etag"`
	Prodkeys               string          `json:"prod_keys"`
	Folder                 string          `json:"folder"`
	ScanFolders            []string        `json:"scan_folders"`
	GUI                    bool            `json:"gui"`
	Debug                  bool            `json:"debug"`
	CheckForMissingUpdates bool            `json:"check_for_missing_updates"`
	CheckForMissingDLC     bool            `json:"check_for_missing_dlc"`
	HideMissingGames       bool            `json:"hide_missing_games"`
	HideDemoGames          bool            `json:"hide_demo_games"`
	OrganizeOptions        OrganizeOptions `json:"organize_options"`
	ScanRecursively        bool            `json:"scan_recursively"`
	GuiPagingSize          int             `json:"gui_page_size"`
	DarkMode               bool            `json:"dark_mode"`
	WindowWidth            int             `json:"window_width,omitempty"`
	WindowHeight           int             `json:"window_height,omitempty"`
	WindowMaximized        bool            `json:"window_maximized,omitempty"`
	IgnoreDLCUpdates       bool            `json:"ignore_dlc_updates"`
	IgnoreDLCTitleIds      []string        `json:"ignore_dlc_title_ids"`
	IgnoreUpdateTitleIds   []string        `json:"ignore_update_title_ids"`
	IgnoreFileTypes        []string        `json:"ignore_file_types"`
}

func ReadSettingsAsJSON(baseFolder string) string {
	settingsPath := filepath.Join(baseFolder, SETTINGS_FILENAME)
	if _, err := os.Stat(settingsPath); err != nil {
		saveDefaultSettings(baseFolder)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		zap.S().Warnf("failed to read settings file: %v", err)
		return "{}"
	}
	return string(data)
}

func cloneSettings(s *AppSettings) *AppSettings {
	if s == nil {
		return nil
	}
	c := *s
	c.ScanFolders = append([]string(nil), s.ScanFolders...)
	c.IgnoreDLCTitleIds = append([]string(nil), s.IgnoreDLCTitleIds...)
	c.IgnoreUpdateTitleIds = append([]string(nil), s.IgnoreUpdateTitleIds...)
	c.IgnoreFileTypes = append([]string(nil), s.IgnoreFileTypes...)
	return &c
}

func ReadSettings(baseFolder string) *AppSettings {
	settingsMu.RLock()
	if settingsInstance != nil {
		c := cloneSettings(settingsInstance)
		settingsMu.RUnlock()
		return c
	}
	settingsMu.RUnlock()

	settingsMu.Lock()
	defer settingsMu.Unlock()
	if settingsInstance != nil {
		return cloneSettings(settingsInstance)
	}
	if _, err := os.Stat(filepath.Join(baseFolder, SETTINGS_FILENAME)); err == nil {
		data, err := os.ReadFile(filepath.Join(baseFolder, SETTINGS_FILENAME))
		if err != nil {
			zap.S().Warnf("Missing or corrupted config file, creating a new one: %v", err)
			return saveDefaultSettingsLocked(baseFolder)
		}
		decoded := &AppSettings{}
		if err := json.Unmarshal(data, decoded); err != nil {
			zap.S().Warnf("Corrupted settings.json, recreating defaults: %v", err)
			return saveDefaultSettingsLocked(baseFolder)
		}
		settingsInstance = verifySettings(baseFolder, decoded)
		return cloneSettings(settingsInstance)
	}
	return saveDefaultSettingsLocked(baseFolder)
}

func verifySettings(baseFolder string, settings *AppSettings) *AppSettings {
	if settings.WindowWidth == 0 {
		settings.WindowWidth = 1200
	}
	if settings.WindowHeight == 0 {
		settings.WindowHeight = 600
	}

	// check so titles json url is set, if not revert to default
	if settings.TitlesJsonUrl == "" {
		settings.TitlesJsonUrl = DEFAULT_TITLES_JSON_URL
	}
	// check so version json url is set, if not revert to default
	if settings.VersionsJsonUrl == "" {
		settings.VersionsJsonUrl = DEFAULT_VERSIONS_JSON_URL
	}

	// check if the title json file exists, if it does not, clear ETAG so a fresh copy is downloaded
	if _, err := os.Stat(filepath.Join(baseFolder, TITLE_JSON_FILENAME)); err != nil {
		settings.TitlesEtag = ""
	}
	// check if the version json file exists, if it does not, clear ETAG so a fresh copy is downloaded
	if _, err := os.Stat(filepath.Join(baseFolder, VERSIONS_JSON_FILENAME)); err != nil {
		settings.VersionsEtag = ""
	}

	return settings
}

func saveDefaultSettings(baseFolder string) *AppSettings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return saveDefaultSettingsLocked(baseFolder)
}

func saveDefaultSettingsLocked(baseFolder string) *AppSettings {
	settingsInstance = &AppSettings{
		TitlesJsonUrl:          DEFAULT_TITLES_JSON_URL,
		TitlesEtag:             "",
		VersionsJsonUrl:        DEFAULT_VERSIONS_JSON_URL,
		VersionsEtag:           "",
		Folder:                 "",
		Prodkeys:               "",
		ScanFolders:            []string{},
		IgnoreUpdateTitleIds:   []string{},
		IgnoreDLCTitleIds:      []string{},
		IgnoreDLCUpdates:       false,
		IgnoreFileTypes:        []string{},
		GUI:                    true,
		GuiPagingSize:          100,
		CheckForMissingUpdates: true,
		CheckForMissingDLC:     true,
		HideMissingGames:       false,
		HideDemoGames:          false,
		ScanRecursively:        true,
		WindowWidth:            1200,
		WindowHeight:           600,
		WindowMaximized:        false,
		Debug:                  false,
		OrganizeOptions: OrganizeOptions{
			RenameFiles:         false,
			CreateFolderPerGame: false,
			DlcFolder:           "",
			UpdatesFolder:       "",
			FolderNameTemplate:  fmt.Sprintf("{%v}", TEMPLATE_TITLE_NAME),
			FileNameTemplate: fmt.Sprintf("{%v} ({%v})[{%v}][v{%v}]", TEMPLATE_TITLE_NAME, TEMPLATE_DLC_NAME,
				TEMPLATE_TITLE_ID, TEMPLATE_VERSION),
			DeleteEmptyFolders:         false,
			SwitchSafeFileNames:        true,
			DeleteOldUpdateFiles:       false,
			ProcessWhenMissingBaseGame: false,
			PrioritizeCompressed:       true,
		},
		DarkMode: true,
	}
	return saveSettingsLocked(settingsInstance, baseFolder)
}

func SaveSettings(settings *AppSettings, baseFolder string) *AppSettings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return saveSettingsLocked(settings, baseFolder)
}

func saveSettingsLocked(settings *AppSettings, baseFolder string) *AppSettings {
	data, err := json.MarshalIndent(settings, "", " ")
	if err != nil {
		zap.S().Errorf("failed to marshal settings: %v", err)
		return settings
	}
	// Atomic write: temp + rename so a crash never leaves half-written JSON.
	tmp, err := os.CreateTemp(baseFolder, "settings-*.tmp")
	if err != nil {
		zap.S().Errorf("failed to write settings file: %v", err)
		return settings
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		zap.S().Errorf("failed to write settings file: %v", err)
		tmp.Close()
		os.Remove(tmpName)
		return settings
	}
	if err := tmp.Close(); err != nil {
		zap.S().Errorf("failed to write settings file: %v", err)
		os.Remove(tmpName)
		return settings
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		zap.S().Warnf("failed to chmod settings file: %v", err)
	}
	if err := os.Rename(tmpName, filepath.Join(baseFolder, SETTINGS_FILENAME)); err != nil {
		zap.S().Errorf("failed to write settings file: %v", err)
		os.Remove(tmpName)
		return settings
	}
	settingsInstance = cloneSettings(settings)
	return cloneSettings(settings)
}

func CheckForUpdates() (bool, error) {

	localVer := SLM_VERSION

	// Bare http.Get uses no timeout at all, so a hung server would block the
	// update check (and, in console mode, startup) indefinitely. Use an
	// explicit client with a total-request timeout instead.
	client := http.Client{Timeout: 30 * time.Second}
	res, err := client.Get(SLM_VERSION_URL)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 400 {
		return false, errors.New("got a non 200 response - " + res.Status)
	}

	// The version file is tiny; cap the read so a hostile endpoint can't force
	// us to buffer an unbounded response into memory.
	body, err := io.ReadAll(io.LimitReader(res.Body, 1*1024*1024))
	if err != nil {
		return false, err
	}

	remoteValues := map[string]string{}
	err = json.Unmarshal(body, &remoteValues)
	if err != nil {
		return false, err
	}

	remoteVer := remoteValues["version"]

	vRemote, err := version.NewVersion(remoteVer)
	if err != nil {
		return false, err
	}
	vLocal, err := version.NewVersion(localVer)
	if err != nil {
		return false, err
	}

	if vRemote.GreaterThan(vLocal) {
		return true, nil
	}

	return false, nil
}
