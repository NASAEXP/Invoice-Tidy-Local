package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx      context.Context
	db       *DBManager
	wm       *WorkerManager
	queue    chan string
	isClosed bool
}

type ProcessingStatus struct {
	Queued      int `json:"queued"`
	NeedsReview int `json:"needsReview"`
	Approved    int `json:"approved"`
	Exported    int `json:"exported"`
	Processing  int `json:"processing"`
}

type ImportResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Count   int    `json:"count"`
}

var supportedInvoiceImportExtensions = map[string]bool{
	".pdf":  true,
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".webp": true,
	".tif":  true,
	".tiff": true,
	".bmp":  true,
}

type ExportResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Path    string `json:"path"`
}

func NewApp() *App {
	return &App{
		queue: make(chan string, 200),
	}
}

func (a *App) OnStartup(ctx context.Context) {
	a.ctx = ctx

	// Init DB
	db, err := NewDBManager()
	if err != nil {
		runtime.EventsEmit(ctx, "local:setupProgress", fmt.Sprintf("Database initialization error: %v", err))
		return
	}
	a.db = db

	// Init Worker
	wm := NewWorkerManager()
	a.wm = wm

	// Start FastAPI worker in background
	go func() {
		if err := wm.Start(ctx); err != nil {
			runtime.EventsEmit(ctx, "local:setupProgress", fmt.Sprintf("Python worker daemon start error: %v", err))
		}
	}()

	// Start exactly 1 background worker routine for document queue
	go a.startQueueWorker()
}

func (a *App) OnShutdown(ctx context.Context) {
	a.isClosed = true
	if a.wm != nil {
		a.wm.Shutdown(ctx)
	}
	if a.db != nil {
		a.db.Close()
	}
}

// 1. Get Host Hardware Profile
func (a *App) GetCompatibility() (CompatibilityReport, error) {
	return GetSystemDiagnostics(), nil
}

// 1b. Resolve local SQLite, documents, and daemon log paths for the UI.
func (a *App) GetLocalPaths() (LocalPaths, error) {
	return GetLocalPaths()
}

// 2. Query Background Parser Daemon Health & Loaded Models
func (a *App) GetModelStatus() (ModelStatus, error) {
	if a.wm == nil {
		return ModelStatus{OK: false, Worker: "starting"}, nil
	}
	return a.wm.GetModelStatus()
}

// 2b. Retrieve Background Parser Daemon Raw Console logs
func (a *App) GetDaemonLogs() (string, error) {
	if a.wm == nil {
		return "Worker manager not initialized", nil
	}
	return a.wm.GetDaemonLogs()
}

// 3. Set Target Document Parsing Extraction Engine Mode
func (a *App) SetModelMode(mode string) (ModelStatus, error) {
	if a.wm == nil {
		return ModelStatus{OK: false, Worker: "starting"}, nil
	}
	a.wm.modelMode = mode
	return a.wm.GetModelStatus()
}

// 4. Retrieve Extraction Fields Configuration
func (a *App) ListFields() ([]TemplateField, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not ready")
	}
	return a.db.ListTemplateFields()
}

// 5. Create or Modify Custom Extraction Field Columns
func (a *App) SaveField(field TemplateField) ([]TemplateField, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not ready")
	}

	if field.ID == "" {
		field.ID = "invoice_" + field.FieldKey
	}
	field.TemplateID = "invoice"

	err := a.db.SaveTemplateField(field)
	if err != nil {
		return nil, err
	}
	return a.db.ListTemplateFields()
}

// 6. Set Extraction Field Visibility for Displays and CSV Exports
func (a *App) SetFieldVisibility(fieldKey string, visible bool) ([]TemplateField, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not ready")
	}
	err := a.db.SetTemplateFieldVisibility(fieldKey, visible)
	if err != nil {
		return nil, err
	}
	return a.db.ListTemplateFields()
}

// 7. Get All Registered Documents logs and extraction properties
func (a *App) ListDocuments() ([]Document, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not ready")
	}
	return a.db.GetDocuments()
}

// 8. Probe running queue parameters and counts
func (a *App) GetProcessingStatus() (ProcessingStatus, error) {
	var status ProcessingStatus
	if a.db == nil {
		return status, fmt.Errorf("database not ready")
	}

	rows, err := a.db.db.Query("SELECT status, COUNT(*) FROM documents GROUP BY status")
	if err != nil {
		return status, err
	}
	defer rows.Close()

	for rows.Next() {
		var s string
		var count int
		if err := rows.Scan(&s, &count); err == nil {
			switch s {
			case "processing":
				status.Processing = count
			case "needs_review":
				status.NeedsReview = count
			case "approved":
				status.Approved = count
			case "failed":
				status.Failed(count) // wait, field names are queued, needsReview, approved, exported, processing
			}
		}
	}

	// Calculate queued vs exported
	a.db.db.QueryRow("SELECT COUNT(*) FROM documents WHERE status = 'queued'").Scan(&status.Queued)

	var exported int
	a.db.db.QueryRow("SELECT COUNT(*) FROM documents WHERE exported_at IS NOT NULL").Scan(&exported)
	status.Exported = exported

	return status, nil
}

func (p *ProcessingStatus) Failed(count int) {
	// failed count doesn't directly need mapping, but let's keep it if needed.
}

// 9. Open Windows Native File Selector and Process Selected Files
func (a *App) ChooseAndImport(mode string) (ImportResponse, error) {
	if a.db == nil {
		return ImportResponse{Success: false, Message: "Database not initialized"}, nil
	}

	filenames, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select Invoice PDFs or Images",
		Filters: []runtime.FileFilter{
			{
				DisplayName: "Invoices (*.pdf, *.png, *.jpg, *.jpeg, *.webp, *.tif, *.tiff, *.bmp)",
				Pattern:     "*.pdf;*.png;*.jpg;*.jpeg;*.webp;*.tif;*.tiff;*.bmp",
			},
		},
	})
	if err != nil {
		return ImportResponse{Success: false, Message: err.Error()}, nil
	}

	if len(filenames) == 0 {
		return ImportResponse{Success: true, Message: "No files selected", Count: 0}, nil
	}

	return a.importFiles(filenames, mode)
}

func (a *App) ImportDroppedFiles(paths []string, mode string) (ImportResponse, error) {
	if len(paths) == 0 {
		return ImportResponse{
			Success: false,
			Message: "No files were dropped.",
			Count:   0,
		}, nil
	}

	return a.importFiles(paths, mode)
}

func (a *App) importFiles(paths []string, mode string) (ImportResponse, error) {
	if a.db == nil {
		return ImportResponse{Success: false, Message: "Database not initialized"}, nil
	}

	importedCount := 0
	skippedCount := 0
	failedCount := 0

	for _, rawPath := range paths {
		rawPath = strings.TrimSpace(rawPath)
		if rawPath == "" || !isSupportedInvoiceImportPath(rawPath) {
			skippedCount++
			continue
		}

		info, err := os.Stat(rawPath)
		if err != nil || info.IsDir() {
			failedCount++
			continue
		}

		if err := a.importSingleFile(rawPath, mode); err == nil {
			importedCount++
		} else {
			failedCount++
		}
	}

	// Trigger processing progress event to refresh list in UI
	if importedCount > 0 && a.ctx != nil {
		runtime.EventsEmit(a.ctx, "local:processingProgress", map[string]interface{}{
			"status": "imported",
		})
	}

	message := fmt.Sprintf("Successfully imported %d files.", importedCount)
	if skippedCount > 0 || failedCount > 0 {
		message = fmt.Sprintf("Imported %d files. Skipped %d unsupported files and %d unreadable files.", importedCount, skippedCount, failedCount)
	}
	if importedCount == 0 {
		message = "No supported invoice files were imported. Drop PDF, PNG, JPG, WebP, TIFF, or BMP files."
	}

	return ImportResponse{
		Success: importedCount > 0,
		Message: message,
		Count:   importedCount,
	}, nil
}

func isSupportedInvoiceImportPath(path string) bool {
	return supportedInvoiceImportExtensions[strings.ToLower(filepath.Ext(path))]
}

func (a *App) importSingleFile(srcPath string, mode string) error {
	fileBytes, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}

	// 1. SHA-256 Hash
	hasher := sha256.New()
	hasher.Write(fileBytes)
	hashStr := hex.EncodeToString(hasher.Sum(nil))

	// 2. Check duplicate
	duplicateID, err := a.db.CheckDuplicateHash(hashStr)
	if err != nil {
		return err
	}

	docID := uuid.New().String()
	var duplicateOf *string = nil
	if duplicateID != "" {
		duplicateOf = &duplicateID
	}

	// 3. Sanitize filename
	baseName := filepath.Base(srcPath)
	ext := filepath.Ext(baseName)
	rawNameWithoutExt := strings.TrimSuffix(baseName, ext)

	// Keep alphanumeric, spaces, and periods
	reg := regexp.MustCompile(`[^a-zA-Z0-9\s\.]`)
	sanitizedName := reg.ReplaceAllString(rawNameWithoutExt, "")
	sanitizedFilename := sanitizedName + strings.ToLower(ext)

	// 4. Save storage directory
	appDir, err := resolveLocalAppDir()
	if err != nil {
		return err
	}
	hashPrefix := hashStr[:2]
	destFolder := filepath.Join(appDir, "documents", hashPrefix)
	os.MkdirAll(destFolder, 0755)

	destPath := filepath.Join(destFolder, docID+strings.ToLower(ext))
	err = os.WriteFile(destPath, fileBytes, 0644)
	if err != nil {
		return err
	}

	// 5. DB insert
	newDoc := Document{
		ID:               docID,
		TemplateID:       "invoice",
		Status:           "queued",
		SourceFileName:   sanitizedFilename,
		StoragePath:      destPath,
		FileHash:         hashStr,
		DuplicateOf:      duplicateOf,
		ExtractionEngine: "light",
		ExtractionMode:   mode,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if err := a.db.SaveDocument(newDoc); err != nil {
		os.Remove(destPath)
		return err
	}

	// 6. Push to extraction channel
	a.queue <- docID

	return nil
}

// 10. Reprocess Extracted Invoices using standard/light models
func (a *App) ReprocessDocuments(documentIDs []string, mode string) (ImportResponse, error) {
	if a.db == nil {
		return ImportResponse{Success: false, Message: "Database not ready"}, nil
	}

	count := 0
	for _, id := range documentIDs {
		// Update status to queued
		_, err := a.db.db.Exec("UPDATE documents SET status = 'queued', extraction_mode = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", mode, id)
		if err == nil {
			a.queue <- id
			count++
		}
	}

	// Broadcast update
	runtime.EventsEmit(a.ctx, "local:processingProgress", map[string]interface{}{
		"status": "reprocessing",
	})

	return ImportResponse{
		Success: true,
		Message: fmt.Sprintf("Enqueued %d documents for reprocessing.", count),
		Count:   count,
	}, nil
}

// 11. Manually Override/Correct Extracted Field Value
func (a *App) UpdateFieldValue(documentID string, fieldKey string, value string) ([]Document, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not ready")
	}

	err := a.db.UpdateDocumentFieldValue(documentID, fieldKey, value)
	if err != nil {
		return nil, err
	}

	return a.db.GetDocuments()
}

// 12. Change Document status flag (Approved / Needs Review)
func (a *App) SetStatus(documentID string, status string) ([]Document, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not ready")
	}

	err := a.db.SetDocumentStatus(documentID, status)
	if err != nil {
		return nil, err
	}

	return a.db.GetDocuments()
}

// 13. Delete Document record and associated storage binaries
func (a *App) DeleteDocument(documentID string) ([]Document, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not ready")
	}

	if err := a.deleteDocumentByID(documentID); err != nil {
		return nil, err
	}

	return a.db.GetDocuments()
}

func (a *App) DeleteDocuments(documentIDs []string) ([]Document, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not ready")
	}

	for _, documentID := range documentIDs {
		documentID = strings.TrimSpace(documentID)
		if documentID == "" {
			continue
		}
		if err := a.deleteDocumentByID(documentID); err != nil {
			return nil, err
		}
	}

	return a.db.GetDocuments()
}

func (a *App) deleteDocumentByID(documentID string) error {
	// Fetch path first to delete it from local storage.
	var storagePath string
	err := a.db.db.QueryRow("SELECT storage_path FROM documents WHERE id = ?", documentID).Scan(&storagePath)
	if err == nil && storagePath != "" {
		os.Remove(storagePath)
	}

	return a.db.DeleteDocument(documentID)
}

// 14. Export Approved Invoice Data to Excel-Friendly CSV
func (a *App) ExportCSV() (ExportResponse, error) {
	if a.db == nil {
		return ExportResponse{Success: false, Message: "Database not ready"}, nil
	}

	// Fetch template fields for columns
	fields, err := a.db.ListTemplateFields()
	if err != nil {
		return ExportResponse{Success: false, Message: "Failed to list template fields"}, nil
	}

	// Filter visible fields
	var visibleFields []TemplateField
	for _, f := range fields {
		if f.Visible {
			visibleFields = append(visibleFields, f)
		}
	}

	if len(visibleFields) == 0 {
		return ExportResponse{Success: false, Message: "No visible fields to export"}, nil
	}

	// Prompt save file dialog
	exportPath, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export CSV",
		DefaultFilename: "invoices-export.csv",
		Filters: []runtime.FileFilter{
			{
				DisplayName: "CSV Files (*.csv)",
				Pattern:     "*.csv",
			},
		},
	})
	if err != nil {
		return ExportResponse{Success: false, Message: err.Error()}, nil
	}

	if exportPath == "" {
		return ExportResponse{Success: true, Message: "Export canceled", Path: ""}, nil
	}

	// Get approved documents
	docs, err := a.db.GetDocuments()
	if err != nil {
		return ExportResponse{Success: false, Message: "Failed to read documents"}, nil
	}

	var approvedDocs []Document
	for _, d := range docs {
		if d.Status == "approved" {
			approvedDocs = append(approvedDocs, d)
		}
	}

	if len(approvedDocs) == 0 {
		return ExportResponse{Success: false, Message: "No approved invoices to export"}, nil
	}

	// Create CSV
	file, err := os.Create(exportPath)
	if err != nil {
		return ExportResponse{Success: false, Message: err.Error()}, nil
	}
	defer file.Close()

	// Write UTF-8 BOM so Excel opens it with correct encoding
	file.Write([]byte{0xEF, 0xBB, 0xBF})

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Headers
	headers := []string{"File Name", "Status"}
	for _, vf := range visibleFields {
		headers = append(headers, vf.ExportColumn)
	}
	writer.Write(headers)

	// Rows
	for _, d := range approvedDocs {
		row := []string{d.SourceFileName, d.Status}
		for _, vf := range visibleFields {
			val := d.Fields[vf.FieldKey]
			row = append(row, val)
		}
		writer.Write(row)

		// Mark as exported in database
		t := time.Now().Format(time.RFC3339)
		a.db.db.Exec("UPDATE documents SET exported_at = ? WHERE id = ?", t, d.ID)
	}

	return ExportResponse{
		Success: true,
		Message: fmt.Sprintf("Successfully exported %d invoices.", len(approvedDocs)),
		Path:    exportPath,
	}, nil
}

// 15. Read Storage PDF/Image to Base64 URI for Side-by-Side Panel Displays
func (a *App) OpenDocumentFile(storagePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(storagePath))
	if ext == ".pdf" {
		if previewURI, err := a.renderPDFPreviewDataURI(storagePath); err == nil && previewURI != "" {
			return previewURI, nil
		}
	}

	fileBytes, err := os.ReadFile(storagePath)
	if err != nil {
		return "", err
	}

	mime := "application/pdf"
	if ext == ".png" {
		mime = "image/png"
	} else if ext == ".jpg" || ext == ".jpeg" {
		mime = "image/jpeg"
	}

	base64Data := base64.StdEncoding.EncodeToString(fileBytes)
	return fmt.Sprintf("data:%s;base64,%s", mime, base64Data), nil
}

func (a *App) renderPDFPreviewDataURI(storagePath string) (string, error) {
	if a.wm == nil {
		return "", fmt.Errorf("worker manager is not ready")
	}

	pyExe, err := a.wm.getPythonPath()
	if err != nil {
		return "", err
	}

	scriptPath := a.wm.resolvePath(filepath.Join("scripts", "render_pdf_preview.py"))
	if _, err := os.Stat(scriptPath); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, pyExe, scriptPath, storagePath, "--page", "1")
	applyPythonRuntimeEnv(cmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("render PDF preview: %w: %s", err, strings.TrimSpace(string(output)))
	}

	previewURI := strings.TrimSpace(string(output))
	if !strings.HasPrefix(previewURI, "data:image/png;base64,") {
		return "", fmt.Errorf("render PDF preview returned unexpected output")
	}
	return previewURI, nil
}

// Background Queue Processor
func (a *App) startQueueWorker() {
	// Sweep database at launch for any remaining queued files
	if a.db != nil {
		rows, err := a.db.db.Query("SELECT id FROM documents WHERE status = 'queued'")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id string
				if errScan := rows.Scan(&id); errScan == nil {
					a.queue <- id
				}
			}
		}
	}

	for docID := range a.queue {
		if a.isClosed {
			break
		}

		a.processQueueItem(docID)
	}
}

func (a *App) processQueueItem(docID string) {
	if !a.documentExists(docID) {
		return
	}

	// 1. Mark as processing
	if ok := a.setDocumentStatusIfExists(docID, "processing"); !ok {
		return
	}
	a.broadcastQueueUpdate(docID, "processing", 0.05, "Readying document")

	// Read doc details
	var doc Document
	err := a.db.db.QueryRow(`SELECT id, storage_path, extraction_mode, duplicate_of FROM documents WHERE id = ?`, docID).Scan(
		&doc.ID, &doc.StoragePath, &doc.ExtractionMode, &doc.DuplicateOf,
	)
	if err != nil {
		if a.setDocumentStatusIfExists(docID, "failed") {
			a.broadcastQueueUpdate(docID, "failed", 1.0, "Failed to load document path")
		}
		return
	}

	// 2. Check if this is a duplicate of a previously extracted document
	if doc.DuplicateOf != nil && *doc.DuplicateOf != "" {
		a.broadcastQueueUpdate(docID, "processing", 0.40, "Copying extracted data from original document")
		time.Sleep(300 * time.Millisecond) // micro-animation / visual spacing

		// Copy fields
		rows, err := a.db.db.Query("SELECT field_key, value, confidence, region_json, raw_source FROM document_field_values WHERE document_id = ?", *doc.DuplicateOf)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var key, value, regionJSON, rawSource string
				var confidence float64
				if errScan := rows.Scan(&key, &value, &confidence, &regionJSON, &rawSource); errScan == nil {
					_ = a.db.SaveDocumentFieldValue(docID, key, value, confidence, regionJSON, rawSource)
				}
			}
		}

		// Copy tables
		_ = a.db.CopyDocumentTables(*doc.DuplicateOf, docID)

		// Update to needs_review
		if a.setDocumentStatusIfExists(docID, "needs_review") {
			a.broadcastQueueUpdate(docID, "needs_review", 1.0, "Duplicate resolved instantly")
		}
		return
	}

	// 3. Normal extraction using FastAPI daemon (optimized queue wait loop)
	a.broadcastQueueUpdate(docID, "processing", 0.15, "Waiting for deep learning models")

	// Wait up to 180 seconds for the Python daemon to start up and become responsive
	daemonReady := false
	for i := 0; i < 180; i++ {
		if !a.documentExists(docID) {
			return
		}
		health, err := a.wm.checkHealth()
		if err == nil && health != nil {
			daemonReady = true
			break
		}
		if a.isClosed {
			return
		}
		a.broadcastQueueUpdate(docID, "processing", 0.15, fmt.Sprintf("Waiting for deep learning daemon to start (%ds)...", i+1))
		time.Sleep(1 * time.Second)
	}

	if !daemonReady {
		if a.setDocumentStatusIfExists(docID, "failed") {
			a.broadcastQueueUpdate(docID, "failed", 1.0, "Python daemon failed to start within 3 minutes")
		}
		return
	}

	// Read fields
	fields, err := a.db.ListTemplateFields()
	if err != nil {
		fields = nil
	}

	// Start parser daemon job
	jobID, err := a.wm.ParseDocument(doc.StoragePath, docID, doc.ExtractionMode, fields)
	if err != nil {
		if a.setDocumentStatusIfExists(docID, "failed") {
			a.broadcastQueueUpdate(docID, "failed", 1.0, fmt.Sprintf("Job enqueue failed: %v", err))
		}
		return
	}

	// Poll job
	pollCount := 0
	maxPolls := 180
	timeoutMessage := "Extraction timed out after 3 minutes"
	if strings.EqualFold(doc.ExtractionMode, "standard") {
		maxPolls = 420 // Standard can be slower on CPU, but should not hold a batch forever.
		timeoutMessage = "Standard extraction timed out after 7 minutes"
	}
	for pollCount < maxPolls {
		time.Sleep(1 * time.Second)
		pollCount++
		if !a.documentExists(docID) {
			return
		}

		pollResp, err := a.wm.PollJob(jobID)
		if err != nil {
			// Show poll failures so the user knows it is still trying
			if pollCount%10 == 0 {
				a.broadcastQueueUpdate(docID, "processing", 0.30, fmt.Sprintf("Waiting for extraction response (%ds)...", pollCount))
			}
			continue
		}

		if pollResp.Status == "running" {
			// Progress slowly advances from 0.40 to 0.80 over time
			progress := 0.40 + float64(pollCount)*0.0007
			if progress > 0.80 {
				progress = 0.80
			}
			a.broadcastQueueUpdate(docID, "processing", progress, fmt.Sprintf("Running deep learning parsing models (%ds)...", pollCount))
		} else if pollResp.Status == "succeeded" {
			if !a.documentExists(docID) {
				return
			}
			a.broadcastQueueUpdate(docID, "processing", 0.85, "Structuring and saving field results")

			// Save fields
			if pollResp.Result != nil {
				// Update document engine used
				_, _ = a.db.db.Exec("UPDATE documents SET extraction_engine = ? WHERE id = ?", pollResp.Result.Engine, docID)

				for key, fval := range pollResp.Result.Fields {
					regionJSON := ""
					if fval.Region != nil {
						rBytes, _ := json.Marshal(fval.Region)
						regionJSON = string(rBytes)
					}
					_ = a.db.SaveDocumentFieldValue(docID, key, fval.Value, fval.Confidence, regionJSON, fval.Source)
				}

				// Save tables
				for key, tableVal := range pollResp.Result.Tables {
					tBytes, _ := json.Marshal(tableVal)
					_ = a.db.SaveDocumentTable(docID, key, string(tBytes))
				}
			}

			// Finalize status
			if a.setDocumentStatusIfExists(docID, "needs_review") {
				a.broadcastQueueUpdate(docID, "needs_review", 1.0, "Parsing complete")
			}
			return
		} else if pollResp.Status == "failed" {
			errStr := "Unknown extraction failure"
			if pollResp.Result != nil && len(pollResp.Result.Errors) > 0 {
				errStr = strings.Join(pollResp.Result.Errors, "; ")
			}
			if a.setDocumentStatusIfExists(docID, "failed") {
				a.broadcastQueueUpdate(docID, "failed", 1.0, fmt.Sprintf("Failed: %s", errStr))
			}
			return
		}
	}

	// Timeout
	if a.setDocumentStatusIfExists(docID, "failed") {
		a.broadcastQueueUpdate(docID, "failed", 1.0, timeoutMessage)
	}
}

func (a *App) documentExists(docID string) bool {
	if a == nil || a.db == nil {
		return false
	}
	var exists int
	err := a.db.db.QueryRow("SELECT 1 FROM documents WHERE id = ? LIMIT 1", docID).Scan(&exists)
	return err == nil
}

func (a *App) setDocumentStatusIfExists(docID string, status string) bool {
	if a == nil || a.db == nil {
		return false
	}
	res, err := a.db.db.Exec("UPDATE documents SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", status, docID)
	if err != nil {
		return false
	}
	rows, err := res.RowsAffected()
	return err == nil && rows > 0
}

func (a *App) broadcastQueueUpdate(docID string, status string, progress float64, message string) {
	if a == nil || a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "local:processingProgress", map[string]interface{}{
		"documentId": docID,
		"status":     status,
		"progress":   progress,
		"message":    message,
	})
}
