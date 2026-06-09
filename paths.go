package main

import (
	"fmt"
	"os"
	"path/filepath"
)

type LocalPaths struct {
	SQLitePath    string `json:"sqlitePath"`
	DocumentsDir  string `json:"documentsDir"`
	DaemonLogPath string `json:"daemonLogPath"`
}

func resolveLocalAppDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user config directory: %w", err)
	}
	return filepath.Join(configDir, "invoice-tidy", "local"), nil
}

func GetLocalPaths() (LocalPaths, error) {
	appDir, err := resolveLocalAppDir()
	if err != nil {
		return LocalPaths{}, err
	}

	return LocalPaths{
		SQLitePath:    filepath.Join(appDir, "invoice-tidy-local.sqlite"),
		DocumentsDir:  filepath.Join(appDir, "documents"),
		DaemonLogPath: filepath.Join(appDir, "daemon.log"),
	}, nil
}

func resolveDaemonLogPath() string {
	paths, err := GetLocalPaths()
	if err != nil {
		return ""
	}
	_ = os.MkdirAll(filepath.Dir(paths.DaemonLogPath), 0755)
	return paths.DaemonLogPath
}
