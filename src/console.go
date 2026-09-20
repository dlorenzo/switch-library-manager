package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/firebat20/switch-library-manager/console"
	"github.com/firebat20/switch-library-manager/db"
	"github.com/firebat20/switch-library-manager/process"
	"github.com/firebat20/switch-library-manager/settings"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/schollz/progressbar/v3"
	"go.uber.org/zap"
)

var (
	progressBar *progressbar.ProgressBar
)

type Console struct {
	baseFolder   string
	sugarLogger  *zap.SugaredLogger
	consoleFlags *console.ConsoleFlags
}

func CreateConsole(baseFolder string, sugarLogger *zap.SugaredLogger, consoleFlags *console.ConsoleFlags) *Console {
	return &Console{baseFolder: baseFolder, sugarLogger: sugarLogger, consoleFlags: consoleFlags}
}

func (c *Console) Start() {
	settingsObj := settings.ReadSettings(c.baseFolder)

	// 0. prepare csv export folder
	csvOutput := ""
	if c.consoleFlags.ExportCsv.IsSet() {
		csvOutput = c.consoleFlags.ExportCsv.String()

		if _, err := os.Stat(csvOutput); os.IsNotExist(err) {
			err = os.MkdirAll(csvOutput, 0755)
			if err != nil {
				fmt.Printf("Failed to create folder for csv export %v - %v\n", csvOutput, err)
				zap.S().Errorf("Failed to create folder for csv export %v - %v\n", csvOutput, err)
			}
		}
	}

	// Kick off the self-update check in the background so it overlaps with
	// the titles/versions downloads below instead of serially blocking
	// startup for up to 30s on a slow endpoint. The result is collected once
	// the downloads finish.
	type updateCheckResult struct {
		newUpdate bool
		err       error
	}
	updateCheckCh := make(chan updateCheckResult, 1)
	go func() {
		newUpdate, err := settings.CheckForUpdates()
		updateCheckCh <- updateCheckResult{newUpdate: newUpdate, err: err}
	}()

	//1. load the titles JSON object
	fmt.Println("Downloading latest switch titles json file")
	progressBar = progressbar.New(2)

	filename := filepath.Join(c.baseFolder, settings.TITLE_JSON_FILENAME)
	titleFile, titlesEtag, err := db.LoadAndUpdateFile(settingsObj.TitlesJsonUrl, filename, settingsObj.TitlesEtag)
	if err != nil {
		fmt.Printf("title json file doesn't exist\n")
		return
	}
	settingsObj.TitlesEtag = titlesEtag
	progressBar.Add(1)
	//2. load the versions JSON object
	filename = filepath.Join(c.baseFolder, settings.VERSIONS_JSON_FILENAME)
	versionsFile, versionsEtag, err := db.LoadAndUpdateFile(settingsObj.VersionsJsonUrl, filename, settingsObj.VersionsEtag)
	if err != nil {
		fmt.Printf("version json file doesn't exist\n")
		return
	}
	settingsObj.VersionsEtag = versionsEtag
	progressBar.Add(1)
	progressBar.Finish()

	updateCheck := <-updateCheckCh
	if updateCheck.err != nil {
		zap.S().Debugf("update check failed: %v", updateCheck.err)
	}
	if updateCheck.newUpdate {
		fmt.Printf("\n=== New version available, download from Github ===\n")
	}

	//3. update the config file with new etag
	settings.SaveSettings(settingsObj, c.baseFolder)

	//4. create switch title db
	titlesDB, err := db.CreateSwitchTitleDB(titleFile, versionsFile)
	// This error was previously unchecked: a malformed titles/versions json
	// left titlesDB nil, which panicked further down (len(titlesDB.TitlesMap)).
	if err != nil {
		fmt.Printf("\nfailed to process the titles/versions json files - %v\n", err)
		zap.S().Errorf("failed to create switch title db: %v", err)
		return
	}

	//5. read local files
	folderToScan := settingsObj.Folder
	if c.consoleFlags.NspFolder.IsSet() && c.consoleFlags.NspFolder.String() != "" {
		folderToScan = c.consoleFlags.NspFolder.String()
	}

	if folderToScan == "" {
		fmt.Printf("\n\nNo folder to scan was defined, please edit settings.json with the folder path\n")
		return
	}
	fmt.Printf("\n\nScanning folder [%v]", folderToScan)
	progressBar = progressbar.New(2000)
	// The old message here interpolated the stale `err` from the titles-db
	// step above, which printed a misleading (or nil) reason. Report the keys
	// error itself instead.
	keys, keysErr := settings.InitSwitchKeys(c.baseFolder)
	if keys == nil || keys.GetKey("header_key") == "" {
		fmt.Printf("\n!!NOTE!!: keys file was not found, deep scan is disabled, library will be based on file tags.")
		if keysErr != nil {
			fmt.Printf("\n %v", keysErr)
		}
		fmt.Println()
	}

	recursiveMode := settingsObj.ScanRecursively
	if c.consoleFlags.Recursive.IsSet() {
		recursiveMode = c.consoleFlags.Recursive.Bool()
	}

	localDbManager, err := db.NewLocalSwitchDBManager(c.baseFolder)
	if err != nil {
		fmt.Printf("failed to create local files db :%v\n", err)
		return
	}
	defer localDbManager.Close()

	scanFolders := append([]string{}, settingsObj.ScanFolders...)
	scanFolders = append(scanFolders, folderToScan)

	localDB, err := localDbManager.CreateLocalSwitchFilesDB(scanFolders, c, recursiveMode, true)
	if err != nil {
		fmt.Printf("\nfailed to process local folder\n %v", err)
		return
	}
	progressBar.Finish()

	// Guard the percentage: an empty titles DB previously produced NaN/+Inf.
	if len(titlesDB.TitlesMap) > 0 {
		p := (float32(len(localDB.TitlesMap)) / float32(len(titlesDB.TitlesMap))) * 100
		fmt.Printf("Local library completion status: %.2f%% (have %d titles, out of %d titles)\n", p, len(localDB.TitlesMap), len(titlesDB.TitlesMap))
	} else {
		fmt.Printf("Local library contains %d titles (titles DB is empty, completion status unavailable)\n", len(localDB.TitlesMap))
	}

	issuesCsvFile := ""
	if csvOutput != "" {
		issuesCsvFile = filepath.Join(csvOutput, "issues.csv")
	}
	c.processIssues(localDB, issuesCsvFile)

	if settingsObj.OrganizeOptions.DeleteOldUpdateFiles {
		progressBar = progressbar.New(2000)
		fmt.Printf("\nDeleting old updates\n")
		// Clean within the library folder being organized, not the app's
		// install directory (c.baseFolder).
		process.DeleteOldUpdates(folderToScan, localDB, c)
		progressBar.Finish()
	}

	if settingsObj.OrganizeOptions.RenameFiles || settingsObj.OrganizeOptions.CreateFolderPerGame {
		progressBar = progressbar.New(2000)
		fmt.Printf("\nStarting library organization\n")
		process.OrganizeByFolders(folderToScan, localDB, titlesDB, c)
		progressBar.Finish()
	}

	if settingsObj.CheckForMissingUpdates {
		fmt.Printf("\nChecking for missing updates\n")

		missingUpdatesCsvFile := ""
		if csvOutput != "" {
			missingUpdatesCsvFile = filepath.Join(csvOutput, "missing_updates.csv")
		}

		c.processMissingUpdates(localDB, titlesDB, settingsObj, missingUpdatesCsvFile)
	}

	if settingsObj.CheckForMissingDLC {
		fmt.Printf("\nChecking for missing DLC\n")

		missingDlcCsvFile := ""
		if csvOutput != "" {
			missingDlcCsvFile = filepath.Join(csvOutput, "missing_dlc.csv")
		}

		c.processMissingDLC(localDB, titlesDB, missingDlcCsvFile)
	}

	fmt.Printf("Completed")
}

func (c *Console) processIssues(localDB *db.LocalSwitchFilesDB, csvOutput string) {
	if len(localDB.Skipped) != 0 {
		fmt.Print("\nSkipped files:\n\n")
	} else {
		return
	}

	csv := CreateCsvFile(csvOutput, []string{"Skipped file", "Reason", "Reason_Code"})

	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleColoredBright)
	t.AppendHeader(table.Row{"#", "Skipped file", "Reason"})
	i := 0
	for k, v := range localDB.Skipped {
		csv.Write([]string{filepath.Join(k.BaseFolder, k.FileName), v.ReasonText, strconv.Itoa(v.ReasonCode)})

		t.AppendRow([]interface{}{i, filepath.Join(k.BaseFolder, k.FileName), v})
		i++
	}
	t.AppendFooter(table.Row{"Total", len(localDB.Skipped)})
	t.Render()

	csv.Close()
}

func (c *Console) processMissingUpdates(localDB *db.LocalSwitchFilesDB, titlesDB *db.SwitchTitlesDB, settingsObj *settings.AppSettings, csvOutput string) {
	ignoreIds := map[string]struct{}{}
	for _, id := range settingsObj.IgnoreUpdateTitleIds {
		ignoreIds[strings.ToLower(id)] = struct{}{}
	}

	incompleteTitles := process.ScanForMissingUpdates(localDB.TitlesMap, titlesDB.TitlesMap, ignoreIds, settingsObj.IgnoreDLCUpdates)
	if len(incompleteTitles) != 0 {
		fmt.Print("\nFound available updates:\n\n")
	} else {
		fmt.Print("\nAll NSP's are up to date!\n\n")
		return
	}

	csv := CreateCsvFile(csvOutput, []string{"Title", "TitleId", "Local version", "Latest Version", "Update Date"})

	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleColoredBright)
	t.AppendHeader(table.Row{"#", "Title", "TitleId", "Local version", "Latest Version", "Update Date"})
	i := 0
	for _, v := range incompleteTitles {
		csv.Write([]string{v.Attributes.Name, v.Attributes.Id, strconv.Itoa(v.LocalUpdate), strconv.Itoa(v.LatestUpdate), v.LatestUpdateDate})

		t.AppendRow([]interface{}{i, v.Attributes.Name, v.Attributes.Id, v.LocalUpdate, v.LatestUpdate, v.LatestUpdateDate})
		i++
	}
	t.AppendFooter(table.Row{"Total", len(incompleteTitles)})
	t.Render()

	csv.Close()
}

func (c *Console) processMissingDLC(localDB *db.LocalSwitchFilesDB, titlesDB *db.SwitchTitlesDB, csvOutput string) {
	settingsObj := settings.ReadSettings(c.baseFolder)
	ignoreIds := map[string]struct{}{}
	for _, id := range settingsObj.IgnoreDLCTitleIds {
		ignoreIds[strings.ToLower(id)] = struct{}{}
	}
	incompleteTitles := process.ScanForMissingDLC(localDB.TitlesMap, titlesDB.TitlesMap, ignoreIds)
	if len(incompleteTitles) != 0 {
		fmt.Print("\nFound missing DLCS:\n\n")
	} else {
		fmt.Print("\nYou have all the DLCS!\n\n")
		return
	}

	csv := CreateCsvFile(csvOutput, []string{"Title", "TitleId", "Dlc (titleId - Name)"})

	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleColoredBright)
	t.AppendHeader(table.Row{"#", "Title", "TitleId", "Missing DLCs (titleId - Name)"})
	i := 0
	for _, v := range incompleteTitles {
		for _, dlc := range v.MissingDLC {
			csv.Write([]string{v.Attributes.Name, v.Attributes.Id, dlc})
		}

		t.AppendRow([]interface{}{i, v.Attributes.Name, v.Attributes.Id, strings.Join(v.MissingDLC, "\n")})
		i++
	}
	t.AppendFooter(table.Row{"Total", len(incompleteTitles)})
	t.Render()

	csv.Close()
}

func (c *Console) UpdateProgress(curr int, total int, message string) {
	if progressBar == nil {
		return
	}
	if total <= 0 {
		return
	}
	if curr < 0 {
		curr = 0
	}
	if curr > total {
		curr = total
	}
	progressBar.ChangeMax(total)
	progressBar.Set(curr)
}

type CsvFile struct {
	Writer *csv.Writer
	File   *os.File
}

func CreateCsvFile(output string, header []string) *CsvFile {
	if output != "" {
		file, err := os.Create(output)
		if err != nil {
			fmt.Printf("Failed to create CSV file %v - %v\n", output, err)
			zap.S().Errorf("Failed to create CSV file %v - %v\n", output, err)
			return nil
		}
		writer := csv.NewWriter(file)

		if err := writer.Write(header); err != nil {
			zap.S().Errorf("Failed to write CSV header %v - %v", output, err)
			file.Close()
			return nil
		}

		instance := &CsvFile{Writer: writer, File: file}
		return instance
	} else {
		return nil
	}
}

func (csv *CsvFile) Close() {
	if csv != nil {
		if csv.Writer != nil {
			csv.Writer.Flush()
			if err := csv.Writer.Error(); err != nil {
				zap.S().Errorf("Failed to flush CSV: %v", err)
			}
		}
		if csv.File != nil {
			if err := csv.File.Close(); err != nil {
				zap.S().Errorf("Failed to close CSV: %v", err)
			}
		}
	}
}

func sanitizeCSVField(s string) string {
	if s != "" && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@') {
		return "'" + s
	}
	return s
}

func (csv *CsvFile) Write(row []string) {
	if csv != nil && csv.Writer != nil {
		safe := make([]string, len(row))
		for i, v := range row {
			safe[i] = sanitizeCSVField(v)
		}
		if err := csv.Writer.Write(safe); err != nil {
			zap.S().Errorf("Failed to write CSV row: %v", err)
		}
	}
}
