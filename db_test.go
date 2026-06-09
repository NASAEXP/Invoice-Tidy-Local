package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveDocumentAllowsQueuedStatus(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())

	mgr, err := NewDBManager()
	if err != nil {
		t.Fatalf("NewDBManager failed: %v", err)
	}
	defer mgr.Close()

	err = mgr.SaveDocument(testQueuedDocument(t.TempDir()))
	if err != nil {
		t.Fatalf("SaveDocument should allow queued status: %v", err)
	}
}

func TestMigrateUpgradesDocumentsStatusConstraint(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)

	appDir := filepath.Join(appData, "invoice-tidy", "local")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatalf("failed to create app dir: %v", err)
	}

	dbPath := filepath.Join(appDir, "invoice-tidy-local.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open temp sqlite db: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE documents (
		id TEXT PRIMARY KEY,
		template_id TEXT NOT NULL REFERENCES templates(id),
		status TEXT NOT NULL DEFAULT 'needs_review' CHECK(status IN ('processing', 'needs_review', 'approved', 'failed')),
		source_file_name TEXT NOT NULL,
		storage_path TEXT NOT NULL,
		file_hash TEXT NOT NULL,
		duplicate_of TEXT REFERENCES documents(id) ON DELETE SET NULL,
		extraction_engine TEXT NOT NULL DEFAULT 'light',
		extraction_mode TEXT NOT NULL DEFAULT 'auto',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		approved_at TEXT,
		exported_at TEXT
	);`)
	if err != nil {
		db.Close()
		t.Fatalf("failed to create legacy documents table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("failed to close temp sqlite db: %v", err)
	}

	mgr, err := NewDBManager()
	if err != nil {
		t.Fatalf("NewDBManager should migrate legacy documents table: %v", err)
	}
	defer mgr.Close()

	var schema string
	if err := mgr.db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'documents'").Scan(&schema); err != nil {
		t.Fatalf("failed to read migrated schema: %v", err)
	}
	if !strings.Contains(schema, "'queued'") {
		t.Fatalf("migrated documents table should allow queued status, schema: %s", schema)
	}

	if err := mgr.SaveDocument(testQueuedDocument(appDir)); err != nil {
		t.Fatalf("SaveDocument should allow queued after migration: %v", err)
	}
}

func TestDuplicateProcessingCopiesTables(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)

	mgr, err := NewDBManager()
	if err != nil {
		t.Fatalf("NewDBManager failed: %v", err)
	}
	defer mgr.Close()

	original := testQueuedDocument(appData)
	original.ID = "original-doc"
	original.Status = "needs_review"
	original.FileHash = "hash-original"
	if err := mgr.SaveDocument(original); err != nil {
		t.Fatalf("failed to save original document: %v", err)
	}
	if err := mgr.SaveDocumentTable(original.ID, "line_items", `[{"description":"Paper","amount":"10.00"}]`); err != nil {
		t.Fatalf("failed to save original table: %v", err)
	}

	duplicateOf := original.ID
	duplicate := testQueuedDocument(appData)
	duplicate.ID = "duplicate-doc"
	duplicate.FileHash = "hash-duplicate"
	duplicate.DuplicateOf = &duplicateOf
	if err := mgr.SaveDocument(duplicate); err != nil {
		t.Fatalf("failed to save duplicate document: %v", err)
	}

	if err := mgr.CopyDocumentTables(original.ID, duplicate.ID); err != nil {
		t.Fatalf("failed to copy duplicate tables: %v", err)
	}

	tables, err := mgr.GetDocumentTables(duplicate.ID)
	if err != nil {
		t.Fatalf("failed to read duplicate tables: %v", err)
	}
	if _, ok := tables["line_items"]; !ok {
		t.Fatalf("expected duplicate to copy line_items table, got %#v", tables)
	}
}

func testQueuedDocument(root string) Document {
	return Document{
		ID:               "test-doc",
		TemplateID:       "invoice",
		Status:           "queued",
		SourceFileName:   "invoice.pdf",
		StoragePath:      filepath.Join(root, "invoice.pdf"),
		FileHash:         "abc123",
		ExtractionEngine: "light",
		ExtractionMode:   "light",
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
}
