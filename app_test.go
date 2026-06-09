package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSupportedInvoiceImportPath(t *testing.T) {
	supported := []string{
		"invoice.pdf",
		"scan.PNG",
		"receipt.JPEG",
		"bill.webp",
		"multipage.TIFF",
		"photo.bmp",
	}
	for _, path := range supported {
		if !isSupportedInvoiceImportPath(path) {
			t.Fatalf("expected %s to be accepted", path)
		}
	}

	unsupported := []string{
		"notes.txt",
		"spreadsheet.xlsx",
		"archive.zip",
		"folder",
	}
	for _, path := range unsupported {
		if isSupportedInvoiceImportPath(path) {
			t.Fatalf("expected %s to be rejected", path)
		}
	}
}

func TestImportDroppedFilesQueuesSupportedDocuments(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())

	mgr, err := NewDBManager()
	if err != nil {
		t.Fatalf("NewDBManager failed: %v", err)
	}
	defer mgr.Close()

	sourceDir := t.TempDir()
	pdfPath := filepath.Join(sourceDir, "invoice.PDF")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.7\nfake invoice\n"), 0644); err != nil {
		t.Fatalf("failed to write fake pdf: %v", err)
	}
	txtPath := filepath.Join(sourceDir, "notes.txt")
	if err := os.WriteFile(txtPath, []byte("not an invoice"), 0644); err != nil {
		t.Fatalf("failed to write unsupported file: %v", err)
	}

	app := &App{db: mgr, queue: make(chan string, 1)}
	resp, err := app.ImportDroppedFiles([]string{pdfPath, txtPath}, "light")
	if err != nil {
		t.Fatalf("ImportDroppedFiles returned error: %v", err)
	}
	if !resp.Success || resp.Count != 1 {
		t.Fatalf("expected one imported file, got success=%v count=%d message=%q", resp.Success, resp.Count, resp.Message)
	}

	select {
	case <-app.queue:
	default:
		t.Fatal("expected imported document to be queued for extraction")
	}

	docs, err := mgr.GetDocuments()
	if err != nil {
		t.Fatalf("GetDocuments failed: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected one saved document, got %d", len(docs))
	}
	if docs[0].ExtractionMode != "light" {
		t.Fatalf("expected extraction mode light, got %q", docs[0].ExtractionMode)
	}
}

func TestDeleteDocumentsRemovesSelectedStorageFiles(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())

	mgr, err := NewDBManager()
	if err != nil {
		t.Fatalf("NewDBManager failed: %v", err)
	}
	defer mgr.Close()

	root := t.TempDir()
	firstPath := filepath.Join(root, "first.pdf")
	secondPath := filepath.Join(root, "second.pdf")
	thirdPath := filepath.Join(root, "third.pdf")
	for _, path := range []string{firstPath, secondPath, thirdPath} {
		if err := os.WriteFile(path, []byte("invoice"), 0644); err != nil {
			t.Fatalf("failed to create test storage file: %v", err)
		}
	}

	docs := []Document{
		testDocumentForDelete("delete-1", "hash-1", firstPath),
		testDocumentForDelete("delete-2", "hash-2", secondPath),
		testDocumentForDelete("keep-1", "hash-3", thirdPath),
	}
	for _, doc := range docs {
		if err := mgr.SaveDocument(doc); err != nil {
			t.Fatalf("SaveDocument failed: %v", err)
		}
	}

	app := &App{db: mgr}
	remaining, err := app.DeleteDocuments([]string{"delete-1", "delete-2"})
	if err != nil {
		t.Fatalf("DeleteDocuments failed: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "keep-1" {
		t.Fatalf("expected only keep-1 to remain, got %+v", remaining)
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("expected first storage file to be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(secondPath); !os.IsNotExist(err) {
		t.Fatalf("expected second storage file to be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(thirdPath); err != nil {
		t.Fatalf("expected unselected storage file to remain, stat err=%v", err)
	}
}

func TestProcessQueueItemIgnoresDeletedQueuedDocument(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())

	mgr, err := NewDBManager()
	if err != nil {
		t.Fatalf("NewDBManager failed: %v", err)
	}
	defer mgr.Close()

	root := t.TempDir()
	storagePath := filepath.Join(root, "deleted-before-processing.pdf")
	if err := os.WriteFile(storagePath, []byte("invoice"), 0644); err != nil {
		t.Fatalf("failed to create test storage file: %v", err)
	}

	doc := testDocumentForDelete("delete-before-processing", "hash-delete-before-processing", storagePath)
	doc.Status = "queued"
	if err := mgr.SaveDocument(doc); err != nil {
		t.Fatalf("SaveDocument failed: %v", err)
	}

	app := &App{db: mgr}
	if err := app.deleteDocumentByID(doc.ID); err != nil {
		t.Fatalf("deleteDocumentByID failed: %v", err)
	}

	app.processQueueItem(doc.ID)

	if app.documentExists(doc.ID) {
		t.Fatalf("deleted document should not be recreated or marked failed")
	}
}

func TestSetDocumentStatusIfExistsReturnsFalseForDeletedDocument(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())

	mgr, err := NewDBManager()
	if err != nil {
		t.Fatalf("NewDBManager failed: %v", err)
	}
	defer mgr.Close()

	app := &App{db: mgr}
	if app.setDocumentStatusIfExists("missing-document", "failed") {
		t.Fatal("expected missing document status update to be ignored")
	}
}

func testDocumentForDelete(id string, hash string, storagePath string) Document {
	doc := testQueuedDocument(filepath.Dir(storagePath))
	doc.ID = id
	doc.FileHash = hash
	doc.StoragePath = storagePath
	doc.SourceFileName = filepath.Base(storagePath)
	return doc
}
