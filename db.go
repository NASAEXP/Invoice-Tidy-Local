package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Domain entities
type Setting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Template struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type TemplateField struct {
	ID           string    `json:"id"`
	TemplateID   string    `json:"templateId"`
	FieldKey     string    `json:"fieldKey"`
	Label        string    `json:"label"`
	Type         string    `json:"type"`
	Required     bool      `json:"required"`
	ExportColumn string    `json:"exportColumn"`
	Hint         string    `json:"hint"`
	Visible      bool      `json:"visible"`
	SortOrder    int       `json:"sortOrder"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Document struct {
	ID               string                 `json:"id"`
	TemplateID       string                 `json:"templateId"`
	Status           string                 `json:"status"`
	SourceFileName   string                 `json:"sourceFileName"`
	StoragePath      string                 `json:"storagePath"`
	FileHash         string                 `json:"fileHash"`
	DuplicateOf      *string                `json:"duplicateOf"`
	ExtractionEngine string                 `json:"extractionEngine"`
	ExtractionMode   string                 `json:"extractionMode"`
	CreatedAt        time.Time              `json:"createdAt"`
	UpdatedAt        time.Time              `json:"updatedAt"`
	ApprovedAt       *time.Time             `json:"approvedAt"`
	ExportedAt       *time.Time             `json:"exportedAt"`
	Fields           map[string]string      `json:"fields,omitempty"` // populated during API list
	FieldDetails     []DocumentFieldValue   `json:"fieldDetails,omitempty"`
	Tables           map[string]interface{} `json:"tables,omitempty"`
}

type DocumentFieldValue struct {
	DocumentID string    `json:"documentId"`
	FieldKey   string    `json:"fieldKey"`
	Value      string    `json:"value"`
	Confidence float64   `json:"confidence"`
	RegionJSON string    `json:"regionJson"`
	RawSource  string    `json:"rawSource"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type DocumentTable struct {
	DocumentID string    `json:"documentId"`
	TableKey   string    `json:"tableKey"`
	RowsJSON   string    `json:"rowsJson"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type AuditEvent struct {
	ID         string    `json:"id"`
	DocumentID *string   `json:"documentId"`
	FieldName  *string   `json:"fieldName"`
	OldValue   *string   `json:"oldValue"`
	NewValue   *string   `json:"newValue"`
	CreatedAt  time.Time `json:"createdAt"`
}

type DBManager struct {
	db     *sql.DB
	dbPath string
}

func createDocumentsTableSQL(tableName string, ifNotExists bool) string {
	existsClause := ""
	if ifNotExists {
		existsClause = "IF NOT EXISTS "
	}
	return fmt.Sprintf(`CREATE TABLE %s%s (
		id TEXT PRIMARY KEY,
		template_id TEXT NOT NULL REFERENCES templates(id),
		status TEXT NOT NULL DEFAULT 'needs_review' CHECK(status IN ('queued', 'processing', 'needs_review', 'approved', 'failed')),
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
	);`, existsClause, tableName)
}

func NewDBManager() (*DBManager, error) {
	appDir, err := resolveLocalAppDir()
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(appDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create app data directory: %v", err)
	}

	docsDir := filepath.Join(appDir, "documents")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create documents directory: %v", err)
	}

	dbPath := filepath.Join(appDir, "invoice-tidy-local.sqlite")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %v", err)
	}

	// Optimize SQLite
	_, err = db.Exec("PRAGMA journal_mode = WAL;")
	if err != nil {
		return nil, fmt.Errorf("failed to set journal mode: %v", err)
	}
	_, err = db.Exec("PRAGMA foreign_keys = ON;")
	if err != nil {
		return nil, fmt.Errorf("failed to enable foreign keys: %v", err)
	}

	mgr := &DBManager{
		db:     db,
		dbPath: dbPath,
	}

	if err := mgr.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %v", err)
	}

	return mgr, nil
}

func (m *DBManager) Close() {
	if m.db != nil {
		m.db.Close()
	}
}

func (m *DBManager) migrate() error {
	// Create settings
	_, err := m.db.Exec(`CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`)
	if err != nil {
		return err
	}

	// Create templates
	_, err = m.db.Exec(`CREATE TABLE IF NOT EXISTS templates (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`)
	if err != nil {
		return err
	}

	// Create template fields
	_, err = m.db.Exec(`CREATE TABLE IF NOT EXISTS template_fields (
		id TEXT PRIMARY KEY,
		template_id TEXT NOT NULL REFERENCES templates(id) ON DELETE CASCADE,
		field_key TEXT NOT NULL,
		label TEXT NOT NULL,
		type TEXT NOT NULL CHECK(type IN ('text', 'date', 'money', 'number', 'percentage')),
		required INTEGER NOT NULL DEFAULT 0,
		export_column TEXT NOT NULL,
		hint TEXT,
		visible INTEGER NOT NULL DEFAULT 1,
		sort_order INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(template_id, field_key)
	);`)
	if err != nil {
		return err
	}

	// Create documents
	_, err = m.db.Exec(createDocumentsTableSQL("documents", true))
	if err != nil {
		return err
	}
	if err := m.ensureQueuedStatusAllowed(); err != nil {
		return err
	}

	// Create document field values
	_, err = m.db.Exec(`CREATE TABLE IF NOT EXISTS document_field_values (
		document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
		field_key TEXT NOT NULL,
		value TEXT,
		confidence REAL,
		region_json TEXT,
		raw_source TEXT,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY(document_id, field_key)
	);`)
	if err != nil {
		return err
	}

	// Create document tables
	_, err = m.db.Exec(`CREATE TABLE IF NOT EXISTS document_tables (
		document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
		table_key TEXT NOT NULL,
		rows_json TEXT NOT NULL DEFAULT '[]',
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY(document_id, table_key)
	);`)
	if err != nil {
		return err
	}

	// Create audit events
	_, err = m.db.Exec(`CREATE TABLE IF NOT EXISTS audit_events (
		id TEXT PRIMARY KEY,
		document_id TEXT REFERENCES documents(id) ON DELETE CASCADE,
		field_name TEXT,
		old_value TEXT,
		new_value TEXT,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`)
	if err != nil {
		return err
	}

	// Create indexes
	_, err = m.db.Exec(`CREATE INDEX IF NOT EXISTS idx_documents_status_created ON documents(status, created_at DESC);`)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(`CREATE INDEX IF NOT EXISTS idx_documents_hash ON documents(file_hash);`)
	if err != nil {
		return err
	}

	// Sweep documents stuck in processing -> failed:interrupted
	_, err = m.db.Exec(`UPDATE documents SET status = 'failed', updated_at = CURRENT_TIMESTAMP WHERE status = 'processing';`)
	if err != nil {
		return err
	}

	// Seed default template
	var count int
	err = m.db.QueryRow("SELECT COUNT(*) FROM templates").Scan(&count)
	if err != nil {
		return err
	}

	if count == 0 {
		_, err = m.db.Exec(`INSERT INTO templates (id, name, kind) VALUES ('invoice', 'Invoices', 'invoice');`)
		if err != nil {
			return err
		}

		seeds := []struct {
			key       string
			label     string
			fieldType string
			required  int
			col       string
			hint      string
			order     int
		}{
			{"vendor_name", "Vendor", "text", 1, "Vendor", "Vendor or supplier name", 0},
			{"invoice_number", "Invoice #", "text", 1, "Invoice Number", "Invoice ID, invoice number, receipt number", 1},
			{"invoice_date", "Invoice date", "date", 1, "Invoice Date", "Invoice issue date", 2},
			{"due_date", "Due date", "date", 0, "Due Date", "Payment due date", 3},
			{"currency", "Currency", "text", 0, "Currency", "Currency code or symbol", 4},
			{"subtotal", "Subtotal", "money", 0, "Subtotal", "Subtotal before tax", 5},
			{"tax", "Tax", "money", 0, "Tax", "Tax amount", 6},
			{"total", "Total", "money", 1, "Total", "Final invoice total", 7},
		}

		for _, s := range seeds {
			id := "invoice_" + s.key
			_, err = m.db.Exec(`INSERT INTO template_fields 
				(id, template_id, field_key, label, type, required, export_column, hint, sort_order) 
				VALUES (?, 'invoice', ?, ?, ?, ?, ?, ?, ?);`,
				id, s.key, s.label, s.fieldType, s.required, s.col, s.hint, s.order)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (m *DBManager) ensureQueuedStatusAllowed() error {
	var schema string
	err := m.db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'documents'").Scan(&schema)
	if err != nil {
		return err
	}
	if strings.Contains(schema, "'queued'") {
		return nil
	}

	if _, err := m.db.Exec("PRAGMA foreign_keys = OFF;"); err != nil {
		return err
	}
	defer m.db.Exec("PRAGMA foreign_keys = ON;")

	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(createDocumentsTableSQL("documents_new", false)); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO documents_new
		(id, template_id, status, source_file_name, storage_path, file_hash, duplicate_of, extraction_engine, extraction_mode, created_at, updated_at, approved_at, exported_at)
		SELECT id, template_id, status, source_file_name, storage_path, file_hash, duplicate_of, extraction_engine, extraction_mode, created_at, updated_at, approved_at, exported_at
		FROM documents;`); err != nil {
		return err
	}
	if _, err := tx.Exec("DROP TABLE documents;"); err != nil {
		return err
	}
	if _, err := tx.Exec("ALTER TABLE documents_new RENAME TO documents;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	_, err = m.db.Exec("PRAGMA foreign_keys = ON;")
	return err
}

// Data methods
func (m *DBManager) GetSetting(key string, defaultValue string) string {
	var val string
	err := m.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&val)
	if err != nil {
		return defaultValue
	}
	return val
}

func (m *DBManager) SetSetting(key string, value string) error {
	_, err := m.db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?);", key, value)
	return err
}

func (m *DBManager) GetDocuments() ([]Document, error) {
	rows, err := m.db.Query(`SELECT id, template_id, status, source_file_name, storage_path, file_hash, duplicate_of, extraction_engine, extraction_mode, created_at, updated_at, approved_at, exported_at FROM documents ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []Document
	for rows.Next() {
		var doc Document
		var createdStr, updatedStr string
		var approvedStr, exportedStr *string
		err = rows.Scan(
			&doc.ID, &doc.TemplateID, &doc.Status, &doc.SourceFileName, &doc.StoragePath,
			&doc.FileHash, &doc.DuplicateOf, &doc.ExtractionEngine, &doc.ExtractionMode,
			&createdStr, &updatedStr, &approvedStr, &exportedStr,
		)
		if err != nil {
			return nil, err
		}

		doc.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		if doc.CreatedAt.IsZero() {
			doc.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdStr)
		}
		doc.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
		if doc.UpdatedAt.IsZero() {
			doc.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedStr)
		}
		if approvedStr != nil {
			t, _ := time.Parse(time.RFC3339, *approvedStr)
			if t.IsZero() {
				t, _ = time.Parse("2006-01-02 15:04:05", *approvedStr)
			}
			doc.ApprovedAt = &t
		}
		if exportedStr != nil {
			t, _ := time.Parse(time.RFC3339, *exportedStr)
			if t.IsZero() {
				t, _ = time.Parse("2006-01-02 15:04:05", *exportedStr)
			}
			doc.ExportedAt = &t
		}

		// Load fields
		fieldsMap, details, err := m.GetDocumentFields(doc.ID)
		if err == nil {
			doc.Fields = fieldsMap
			doc.FieldDetails = details
		}

		// Load tables
		tablesMap, err := m.GetDocumentTables(doc.ID)
		if err == nil {
			doc.Tables = tablesMap
		}

		docs = append(docs, doc)
	}

	return docs, nil
}

func (m *DBManager) GetDocumentFields(docID string) (map[string]string, []DocumentFieldValue, error) {
	rows, err := m.db.Query("SELECT field_key, value, confidence, region_json, raw_source, updated_at FROM document_field_values WHERE document_id = ?", docID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	fields := make(map[string]string)
	var details []DocumentFieldValue

	for rows.Next() {
		var f DocumentFieldValue
		f.DocumentID = docID
		var updatedStr string
		err = rows.Scan(&f.FieldKey, &f.Value, &f.Confidence, &f.RegionJSON, &f.RawSource, &updatedStr)
		if err != nil {
			return nil, nil, err
		}
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
		if f.UpdatedAt.IsZero() {
			f.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedStr)
		}

		fields[f.FieldKey] = f.Value
		details = append(details, f)
	}

	return fields, details, nil
}

func (m *DBManager) GetDocumentTables(docID string) (map[string]interface{}, error) {
	rows, err := m.db.Query("SELECT table_key, rows_json FROM document_tables WHERE document_id = ?", docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make(map[string]interface{})
	for rows.Next() {
		var key, rowsJSON string
		err = rows.Scan(&key, &rowsJSON)
		if err != nil {
			return nil, err
		}
		var parsedRows interface{}
		if err := json.Unmarshal([]byte(rowsJSON), &parsedRows); err == nil {
			tables[key] = parsedRows
		} else {
			tables[key] = []interface{}{}
		}
	}
	return tables, nil
}

func (m *DBManager) SaveDocumentFieldValue(docID string, fieldKey string, value string, confidence float64, regionJSON string, rawSource string) error {
	_, err := m.db.Exec(`INSERT OR REPLACE INTO document_field_values 
		(document_id, field_key, value, confidence, region_json, raw_source, updated_at) 
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP);`,
		docID, fieldKey, value, confidence, regionJSON, rawSource)
	return err
}

func (m *DBManager) SaveDocumentTable(docID string, tableKey string, rowsJSON string) error {
	_, err := m.db.Exec(`INSERT OR REPLACE INTO document_tables 
		(document_id, table_key, rows_json, updated_at) 
		VALUES (?, ?, ?, CURRENT_TIMESTAMP);`,
		docID, tableKey, rowsJSON)
	return err
}

func (m *DBManager) CopyDocumentTables(sourceDocID string, destinationDocID string) error {
	rows, err := m.db.Query("SELECT table_key, rows_json FROM document_tables WHERE document_id = ?", sourceDocID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var key, rowsJSON string
		if err := rows.Scan(&key, &rowsJSON); err != nil {
			return err
		}
		if err := m.SaveDocumentTable(destinationDocID, key, rowsJSON); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (m *DBManager) UpdateDocumentFieldValue(docID string, fieldKey string, value string) error {
	// Fetch old value first for audit trail
	var oldValue string
	err := m.db.QueryRow("SELECT value FROM document_field_values WHERE document_id = ? AND field_key = ?", docID, fieldKey).Scan(&oldValue)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	// Update or insert
	_, err = m.db.Exec(`INSERT INTO document_field_values (document_id, field_key, value, confidence, updated_at)
		VALUES (?, ?, ?, 1.0, CURRENT_TIMESTAMP)
		ON CONFLICT(document_id, field_key) DO UPDATE SET value = ?, confidence = 1.0, updated_at = CURRENT_TIMESTAMP;`,
		docID, fieldKey, value, value)
	if err != nil {
		return err
	}

	// Insert audit event
	auditID := fmt.Sprintf("audit_%d", time.Now().UnixNano())
	_, err = m.db.Exec(`INSERT INTO audit_events (id, document_id, field_name, old_value, new_value) VALUES (?, ?, ?, ?, ?);`,
		auditID, docID, fieldKey, oldValue, value)
	return err
}

func (m *DBManager) SaveDocument(doc Document) error {
	approvedStr := ""
	if doc.ApprovedAt != nil {
		approvedStr = doc.ApprovedAt.Format(time.RFC3339)
	}
	exportedStr := ""
	if doc.ExportedAt != nil {
		exportedStr = doc.ExportedAt.Format(time.RFC3339)
	}

	_, err := m.db.Exec(`INSERT OR REPLACE INTO documents 
		(id, template_id, status, source_file_name, storage_path, file_hash, duplicate_of, extraction_engine, extraction_mode, created_at, updated_at, approved_at, exported_at) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, 
		CASE WHEN ? = '' THEN NULL ELSE ? END, 
		CASE WHEN ? = '' THEN NULL ELSE ? END);`,
		doc.ID, doc.TemplateID, doc.Status, doc.SourceFileName, doc.StoragePath,
		doc.FileHash, doc.DuplicateOf, doc.ExtractionEngine, doc.ExtractionMode,
		doc.CreatedAt.Format(time.RFC3339), approvedStr, approvedStr, exportedStr, exportedStr)
	return err
}

func (m *DBManager) CheckDuplicateHash(hash string) (string, error) {
	var duplicateID string
	err := m.db.QueryRow("SELECT id FROM documents WHERE file_hash = ? AND (duplicate_of IS NULL) LIMIT 1", hash).Scan(&duplicateID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return duplicateID, err
}

func (m *DBManager) SetDocumentStatus(docID string, status string) error {
	var approvedAt *string
	if status == "approved" {
		t := time.Now().Format(time.RFC3339)
		approvedAt = &t
	}
	_, err := m.db.Exec(`UPDATE documents SET status = ?, approved_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;`, status, approvedAt, docID)
	return err
}

func (m *DBManager) DeleteDocument(docID string) error {
	// Explicit child cleanup in one transaction. ON DELETE CASCADE only fires
	// when foreign_keys is enabled on the serving connection, which isn't
	// guaranteed across the database/sql pool (the PRAGMA is per-connection).
	// Deleting children explicitly means no orphan field/table/audit rows leak,
	// regardless of which pooled connection runs the delete.
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		"DELETE FROM document_field_values WHERE document_id = ?;",
		"DELETE FROM document_tables WHERE document_id = ?;",
		"DELETE FROM audit_events WHERE document_id = ?;",
		"UPDATE documents SET duplicate_of = NULL WHERE duplicate_of = ?;",
		"DELETE FROM documents WHERE id = ?;",
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt, docID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *DBManager) ListTemplateFields() ([]TemplateField, error) {
	rows, err := m.db.Query("SELECT id, template_id, field_key, label, type, required, export_column, hint, visible, sort_order, created_at, updated_at FROM template_fields ORDER BY sort_order ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fields []TemplateField
	for rows.Next() {
		var f TemplateField
		var reqVal, visVal int
		var createdStr, updatedStr string
		err = rows.Scan(&f.ID, &f.TemplateID, &f.FieldKey, &f.Label, &f.Type, &reqVal, &f.ExportColumn, &f.Hint, &visVal, &f.SortOrder, &createdStr, &updatedStr)
		if err != nil {
			return nil, err
		}
		f.Required = reqVal > 0
		f.Visible = visVal > 0
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		if f.CreatedAt.IsZero() {
			f.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdStr)
		}
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
		if f.UpdatedAt.IsZero() {
			f.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedStr)
		}

		fields = append(fields, f)
	}
	return fields, nil
}

func (m *DBManager) SaveTemplateField(f TemplateField) error {
	reqVal := 0
	if f.Required {
		reqVal = 1
	}
	visVal := 0
	if f.Visible {
		visVal = 1
	}

	_, err := m.db.Exec(`INSERT INTO template_fields 
		(id, template_id, field_key, label, type, required, export_column, hint, visible, sort_order, created_at, updated_at) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(template_id, field_key) DO UPDATE SET 
		label = excluded.label, 
		type = excluded.type, 
		required = excluded.required, 
		export_column = excluded.export_column, 
		hint = excluded.hint, 
		visible = excluded.visible, 
		sort_order = excluded.sort_order, 
		updated_at = CURRENT_TIMESTAMP;`,
		f.ID, f.TemplateID, f.FieldKey, f.Label, f.Type, reqVal, f.ExportColumn, f.Hint, visVal, f.SortOrder)
	return err
}

func (m *DBManager) SetTemplateFieldVisibility(fieldKey string, visible bool) error {
	visVal := 0
	if visible {
		visVal = 1
	}
	_, err := m.db.Exec("UPDATE template_fields SET visible = ?, updated_at = CURRENT_TIMESTAMP WHERE field_key = ?;", visVal, fieldKey)
	return err
}
