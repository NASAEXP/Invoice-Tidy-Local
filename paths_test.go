package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGetLocalPaths(t *testing.T) {
	paths, err := GetLocalPaths()
	if err != nil {
		t.Fatalf("GetLocalPaths() error: %v", err)
	}

	if !strings.HasSuffix(paths.SQLitePath, "invoice-tidy-local.sqlite") {
		t.Fatalf("unexpected sqlite path: %s", paths.SQLitePath)
	}
	if filepath.Base(paths.DocumentsDir) != "documents" {
		t.Fatalf("unexpected documents dir: %s", paths.DocumentsDir)
	}
	if filepath.Base(paths.DaemonLogPath) != "daemon.log" {
		t.Fatalf("unexpected daemon log path: %s", paths.DaemonLogPath)
	}
}
