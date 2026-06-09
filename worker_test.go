package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWorkerManager(t *testing.T) {
	wm := NewWorkerManager()
	if wm == nil {
		t.Fatal("Expected WorkerManager instance, got nil")
	}
	if wm.status != "starting" {
		t.Errorf("Expected status 'starting', got '%s'", wm.status)
	}
	if wm.modelMode != "auto" {
		t.Errorf("Expected modelMode 'auto', got '%s'", wm.modelMode)
	}
	if wm.client == nil {
		t.Error("Expected http client to be initialized, got nil")
	}
}

func TestGetDaemonLogs(t *testing.T) {
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to read working directory: %v", err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to switch to temp dir: %v", err)
	}
	defer os.Chdir(originalWd)

	logDir := "local-tools"
	logPath := filepath.Join(logDir, "daemon.log")
	if err := os.Mkdir(logDir, 0755); err != nil {
		t.Fatalf("Failed to create temp local-tools: %v", err)
	}

	mockLogs := []string{
		"Starting up local neural engine",
		"Loading RapidOCR models",
		"ERROR: CUDA device not found, falling back to CPU",
		"Uvicorn running on http://127.0.0.1:8000",
	}
	err = os.WriteFile(logPath, []byte(strings.Join(mockLogs, "\n")), 0644)
	if err != nil {
		t.Fatalf("Failed to write mock log file: %v", err)
	}

	wm := NewWorkerManager()
	logs, err := wm.GetDaemonLogs()
	if err != nil {
		t.Fatalf("Expected no error reading mock logs, got: %v", err)
	}

	for _, line := range mockLogs {
		if !strings.Contains(logs, line) {
			t.Errorf("Expected logs to contain '%s', but got: %s", line, logs)
		}
	}
}

func TestJobPollResponseAcceptsDoclingTableArray(t *testing.T) {
	payload := []byte(`{
		"ok": true,
		"id": "doc-123",
		"status": "succeeded",
		"result": {
			"ok": true,
			"device": "cpu",
			"engine": "docling-layout-rapidocr",
			"fields": {},
			"tables": [
				{"id": "table-1", "data": {"rows": [["Description", "Total"], ["Paper", "$10.00"]]}},
				{"data": {"rows": [["Subtotal", "$10.00"]]}}
			],
			"errors": []
		}
	}`)

	var response JobPollResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("poll response should decode Docling table arrays: %v", err)
	}
	if response.Result == nil {
		t.Fatal("expected result to be decoded")
	}
	if len(response.Result.Tables) != 2 {
		t.Fatalf("expected 2 tables, got %d", len(response.Result.Tables))
	}
	if _, ok := response.Result.Tables["table-1"]; !ok {
		t.Fatalf("expected table id from worker payload to be preserved, got %#v", response.Result.Tables)
	}
	if _, ok := response.Result.Tables["table_2"]; !ok {
		t.Fatalf("expected missing table id to get a stable generated key, got %#v", response.Result.Tables)
	}
}

func TestJobFieldRequestIncludesExportColumn(t *testing.T) {
	payload, err := json.Marshal(JobFieldRequest{
		Key:          "invoice_number",
		Label:        "Invoice #",
		Type:         "text",
		Required:     true,
		Hint:         "Invoice ID, invoice number, receipt number",
		ExportColumn: "Invoice Number",
	})
	if err != nil {
		t.Fatalf("field request should marshal: %v", err)
	}
	if !strings.Contains(string(payload), `"exportColumn":"Invoice Number"`) {
		t.Fatalf("expected export column to be sent to Python worker, got %s", payload)
	}
}
