package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/firebat20/switch-library-manager/console"
	"github.com/firebat20/switch-library-manager/settings"
	"go.uber.org/zap"
)

func main() {
	exePath, err := os.Executable()
	if err != nil {
		fmt.Println("failed to get executable directory, please ensure app has sufficient permissions. aborting")
		return
	}

	workingFolder := filepath.Dir(exePath)

	if runtime.GOOS == "darwin" {
		if idx := strings.Index(workingFolder, ".app/"); idx >= 0 {
			sepIndex := strings.LastIndex(workingFolder[:idx], string(os.PathSeparator))
			if sepIndex > 0 {
				workingFolder = workingFolder[:sepIndex]
			}
		} else if strings.HasSuffix(workingFolder, ".app") {
			sepIndex := strings.LastIndex(filepath.Dir(workingFolder), string(os.PathSeparator))
			if sepIndex > 0 {
				workingFolder = workingFolder[:sepIndex]
			}
		}
	}

	appSettings := settings.ReadSettings(workingFolder)

	logger := createLogger(workingFolder, appSettings.Debug)

	defer logger.Sync() // flushes buffer, if any
	sugar := logger.Sugar()

	sugar.Info("[SLM starts]")
	sugar.Infof("[Executable: %v]", exePath)
	sugar.Infof("[Working directory: %v]", workingFolder)

	if AssetDir == nil {
		appSettings.GUI = false
	} else if files, err := AssetDir("resources/app"); err != nil || len(files) == 0 {
		appSettings.GUI = false
	}

	console.InitializeFlags()
	console.LogFlags(sugar)

	consoleFlags := console.GetFlagsValues()
	useGUI := appSettings.GUI
	if consoleFlags.Mode.IsSet() {
		mode := consoleFlags.Mode.String()
		if mode == "console" {
			useGUI = false
		} else if mode == "gui" {
			useGUI = true
		}
	}

	if useGUI {
		CreateGUI(workingFolder, sugar).Start()
	} else {
		console.FixConsoleOutput()
		CreateConsole(workingFolder, sugar, consoleFlags).Start()
	}
}

func createLogger(workingFolder string, debug bool) *zap.Logger {
	config := zap.NewDevelopmentConfig()
	if debug {
		config.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	} else {
		config.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}
	logPath := filepath.Join(workingFolder, "slm.log")
	// delete old file
	os.Remove(logPath)

	if runtime.GOOS == "windows" {
		zap.RegisterSink("winfile", func(u *url.URL) (zap.Sink, error) {
			// Remove leading slash left by url.Parse()
			p := u.Path
			if len(p) > 0 && p[0] == '/' {
				p = p[1:]
			}
			if p == "" {
				return nil, fmt.Errorf("invalid log path")
			}
			return os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
		})
		logPath = "winfile:///" + filepath.ToSlash(logPath)
	}

	config.OutputPaths = []string{logPath}
	config.ErrorOutputPaths = []string{"stderr"}
	logger, err := config.Build()
	if err != nil {
		fmt.Printf("failed to create logger - %v\n", err)
		os.Exit(1)
	}
	zap.ReplaceGlobals(logger)
	return logger
}
