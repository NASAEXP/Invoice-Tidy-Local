package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type WorkerManager struct {
	port       int
	token      string
	cmd        *exec.Cmd
	client     *http.Client
	longClient *http.Client // longer timeout for model-loading requests
	running    bool
	status     string // starting, ready, busy, failed
	device     string
	engine     string
	model      string
	modelMode  string
}

type ModelStatus struct {
	OK       bool   `json:"ok"`
	Worker   string `json:"worker"`
	Light    string `json:"light"`
	Standard string `json:"standard"`
	Device   string `json:"device"`
	Engine   string `json:"engine"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
}

type PythonHealthResponse struct {
	OK            bool    `json:"ok"`
	Status        string  `json:"status"`
	Device        string  `json:"device"`
	Engine        string  `json:"engine"`
	Model         string  `json:"model"`
	UptimeSeconds float64 `json:"uptimeSeconds"`
	Queue         int     `json:"queue"`
}

type JobFieldRequest struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	Type         string `json:"type"`
	Required     bool   `json:"required"`
	Hint         string `json:"hint"`
	ExportColumn string `json:"exportColumn"`
}

type JobPostRequest struct {
	DocumentPath   string            `json:"documentPath"`
	DocumentID     string            `json:"documentId"`
	Mode           string            `json:"mode"`
	TemplateFields []JobFieldRequest `json:"templateFields"`
}

type JobPostResponse struct {
	OK     bool   `json:"ok"`
	ID     string `json:"id"`
	Status string `json:"status"`
}

type JobResultValue struct {
	Value      string                 `json:"value"`
	Confidence float64                `json:"confidence"`
	Source     string                 `json:"source"`
	Region     map[string]interface{} `json:"region"`
}

type JobPollResult struct {
	OK     bool                      `json:"ok"`
	Device string                    `json:"device"`
	Engine string                    `json:"engine"`
	Fields map[string]JobResultValue `json:"fields"`
	Tables TableResults              `json:"tables"`
	Errors []string                  `json:"errors"`
}

type TableResults map[string]interface{}

func (t *TableResults) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*t = TableResults{}
		return nil
	}

	switch trimmed[0] {
	case '{':
		var tables map[string]interface{}
		if err := json.Unmarshal(trimmed, &tables); err != nil {
			return err
		}
		*t = TableResults(tables)
		return nil
	case '[':
		var tableList []interface{}
		if err := json.Unmarshal(trimmed, &tableList); err != nil {
			return err
		}
		tables := make(TableResults, len(tableList))
		for i, table := range tableList {
			key := fmt.Sprintf("table_%d", i+1)
			if tableMap, ok := table.(map[string]interface{}); ok {
				if id, ok := tableMap["id"].(string); ok && strings.TrimSpace(id) != "" {
					key = strings.TrimSpace(id)
				}
			}
			if _, exists := tables[key]; exists {
				key = fmt.Sprintf("%s_%d", key, i+1)
			}
			tables[key] = table
		}
		*t = tables
		return nil
	default:
		return fmt.Errorf("unsupported tables JSON shape: %s", string(trimmed[:1]))
	}
}

type JobPollResponse struct {
	OK         bool           `json:"ok"`
	ID         string         `json:"id"`
	Status     string         `json:"status"`
	CreatedAt  float64        `json:"createdAt"`
	StartedAt  float64        `json:"startedAt"`
	FinishedAt float64        `json:"finishedAt"`
	Result     *JobPollResult `json:"result"`
}

func NewWorkerManager() *WorkerManager {
	return &WorkerManager{
		client:     &http.Client{Timeout: 120 * time.Second},
		longClient: &http.Client{Timeout: 300 * time.Second},
		status:     "starting",
		modelMode:  "auto",
	}
}

func applyPythonRuntimeEnv(cmd *exec.Cmd) {
	threads := strings.TrimSpace(os.Getenv("INVOICE_TIDY_PYTHON_THREADS"))
	if threads == "" {
		threads = "1"
	}

	env := os.Environ()
	overrides := map[string]string{
		"OPENBLAS_NUM_THREADS":   threads,
		"OMP_NUM_THREADS":        threads,
		"MKL_NUM_THREADS":        threads,
		"VECLIB_MAXIMUM_THREADS": threads,
		"NUMEXPR_NUM_THREADS":    threads,
		"NUMEXPR_MAX_THREADS":    threads,
		"TOKENIZERS_PARALLELISM": "false",
	}
	for key, value := range overrides {
		env = upsertEnv(env, key, value)
	}
	cmd.Env = env
}

func upsertEnv(env []string, key string, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func (w *WorkerManager) Start(ctx context.Context) error {
	// 1. Preflight Dependency Check
	hasDeps, err := w.checkDependencies()
	if !hasDeps {
		runtime.EventsEmit(ctx, "local:setupProgress", "Docling/FastAPI dependencies not verified. Initiating powershell installation...")
		if err := w.runSetupScript(ctx); err != nil {
			w.status = "failed"
			return fmt.Errorf("preflight setup failed: %v", err)
		}
	}

	// 2. Discover free port
	port, err := w.findFreePort()
	if err != nil {
		w.status = "failed"
		return fmt.Errorf("failed to discover free port: %v", err)
	}
	w.port = port

	// 3. Generate secure token
	w.token = w.generateSecureToken()

	// 4. Spawn background process
	if err := w.spawnProcess(port); err != nil {
		w.status = "failed"
		return fmt.Errorf("failed to spawn worker: %v", err)
	}

	w.running = true

	// 5. Start crash recovery loop and health checks
	go w.monitorLoop(ctx)

	return nil
}

func (w *WorkerManager) checkDependencies() (bool, error) {
	pyExe, err := w.getPythonPath()
	if err != nil {
		return false, err
	}

	cmd := exec.Command(pyExe, "-c", "import docling, torch, transformers, fastapi, uvicorn, pydantic, rapidocr, onnxruntime;")
	applyPythonRuntimeEnv(cmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}

	err = cmd.Run()
	return err == nil, err
}

func (w *WorkerManager) resolvePath(subPath string) string {
	if _, err := os.Stat(subPath); err == nil {
		return subPath
	}
	execPath, err := os.Executable()
	if err == nil {
		baseDir := filepath.Dir(execPath)
		p := filepath.Join(baseDir, subPath)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		p2 := filepath.Join(baseDir, "..", "..", subPath)
		if _, err := os.Stat(p2); err == nil {
			return p2
		}
	}
	return subPath
}

func (w *WorkerManager) getPythonPath() (string, error) {
	if forced := os.Getenv("INVOICE_TIDY_DOCLING_PYTHON"); forced != "" {
		return forced, nil
	}

	venvPySub := filepath.Join("local-tools", "docling-venv", "Scripts", "python.exe")
	venvPy := w.resolvePath(venvPySub)
	if _, err := os.Stat(venvPy); err == nil {
		return venvPy, nil
	}

	return "python", nil
}

func (w *WorkerManager) runSetupScript(ctx context.Context) error {
	runtime.EventsEmit(ctx, "local:setupProgress", "Running setup-docling.ps1. This will download PyTorch and Docling (~2GB download)...")

	scriptPathSub := filepath.Join("scripts", "setup-docling.ps1")
	scriptPath := w.resolvePath(scriptPathSub)
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("setup script not found: %s", scriptPath)
	}

	args := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath}
	if os.Getenv("INVOICE_TIDY_USE_CUDA") == "1" {
		args = append(args, "-Cuda")
	} else {
		args = append(args, "-CpuOnly")
	}

	// Run powershell setup
	cmd := exec.Command("powershell.exe", args...)
	applyPythonRuntimeEnv(cmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	// Read outputs in real-time and stream as events
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			text := scanner.Text()
			runtime.EventsEmit(ctx, "local:setupProgress", text)
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			text := scanner.Text()
			runtime.EventsEmit(ctx, "local:setupProgress", "ERROR: "+text)
		}
	}()

	err = cmd.Wait()
	if err != nil {
		runtime.EventsEmit(ctx, "local:setupProgress", "Setup failed with error: "+err.Error())
		return err
	}

	runtime.EventsEmit(ctx, "local:setupProgress", "Setup completed successfully!")
	return nil
}

func (w *WorkerManager) findFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

func (w *WorkerManager) generateSecureToken() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (w *WorkerManager) spawnProcess(port int) error {
	pyExe, err := w.getPythonPath()
	if err != nil {
		return err
	}

	workerPySub := filepath.Join("scripts", "document_parse_worker.py")
	workerPy := w.resolvePath(workerPySub)

	cmd := exec.Command(pyExe, workerPy, "--host", "127.0.0.1", "--port", fmt.Sprintf("%d", port), "--token", w.token)
	applyPythonRuntimeEnv(cmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}

	// Capture output in a persistent daemon.log file
	localToolsDir := w.resolvePath("local-tools")
	logPath := filepath.Join(localToolsDir, "daemon.log")
	if _, err := os.Stat(localToolsDir); err != nil {
		logPath = resolveDaemonLogPath()
	}
	if logPath != "" {
		os.MkdirAll(filepath.Dir(logPath), 0755)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		logFile.WriteString(fmt.Sprintf("\n--- DAEMON STARTUP AT %s (PORT %d) ---\n", time.Now().Format(time.RFC3339), port))
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return err
	}

	w.cmd = cmd
	return nil
}

func (w *WorkerManager) Shutdown(ctx context.Context) {
	w.running = false
	if w.cmd == nil {
		return
	}

	// Graceful shutdown via HTTP post
	url := fmt.Sprintf("http://127.0.0.1:%d/shutdown", w.port)
	req, err := http.NewRequest("POST", url, nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+w.token)
		resp, err := w.client.Do(req)
		if err == nil {
			resp.Body.Close()
			time.Sleep(300 * time.Millisecond) // Let it exit
		}
	}

	// Ensure process killed
	if w.cmd.Process != nil {
		w.cmd.Process.Kill()
	}
}

func (w *WorkerManager) monitorLoop(ctx context.Context) {
	consecutiveFailures := 0

	for w.running {
		time.Sleep(10 * time.Second)
		if !w.running {
			break
		}

		health, err := w.checkHealth()
		if err != nil {
			consecutiveFailures++
			if consecutiveFailures >= 2 {
				runtime.EventsEmit(ctx, "local:setupProgress", "FastAPI Daemon crashed or stopped responding. Re-spawning...")
				w.status = "starting"
				w.Shutdown(ctx)

				// Find new port & re-spawn
				port, err := w.findFreePort()
				if err == nil {
					w.port = port
					w.token = w.generateSecureToken()
					if err := w.spawnProcess(port); err == nil {
						// ponytail: Shutdown() above cleared w.running; restore it
						// or this loop exits and we never recover a 2nd crash.
						w.running = true
						consecutiveFailures = 0
						continue
					}
				}
				w.status = "failed"
			}
		} else {
			consecutiveFailures = 0
			w.status = health.Status
			w.device = health.Device
			w.engine = health.Engine
			w.model = health.Model
		}
	}
}

func (w *WorkerManager) checkHealth() (*PythonHealthResponse, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/health", w.port)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+w.token)
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status code: %d", resp.StatusCode)
	}

	var h PythonHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, err
	}

	return &h, nil
}

func (w *WorkerManager) GetModelStatus() (ModelStatus, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/models", w.port)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ModelStatus{OK: false, Worker: w.status, Mode: w.modelMode}, err
	}

	req.Header.Set("Authorization", "Bearer "+w.token)
	resp, err := w.client.Do(req)
	if err != nil {
		return ModelStatus{OK: false, Worker: w.status, Device: w.device, Engine: w.engine, Model: w.model, Mode: w.modelMode}, nil
	}
	defer resp.Body.Close()

	var s ModelStatus
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return ModelStatus{OK: false, Worker: w.status, Mode: w.modelMode}, err
	}
	s.Mode = w.modelMode
	return s, nil
}

func (w *WorkerManager) ParseDocument(docPath string, docID string, mode string, fields []TemplateField) (string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/jobs", w.port)

	jobFields := make([]JobFieldRequest, len(fields))
	for i, f := range fields {
		jobFields[i] = JobFieldRequest{
			Key:          f.FieldKey,
			Label:        f.Label,
			Type:         f.Type,
			Required:     f.Required,
			Hint:         f.Hint,
			ExportColumn: f.ExportColumn,
		}
	}

	reqPayload := JobPostRequest{
		DocumentPath:   docPath,
		DocumentID:     docID,
		Mode:           mode,
		TemplateFields: jobFields,
	}

	reqBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(reqBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.token)

	// Use long-timeout client for job submission since the first call
	// may trigger model loading which can take several minutes
	resp, err := w.longClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var postResp JobPostResponse
	if err := json.NewDecoder(resp.Body).Decode(&postResp); err != nil {
		return "", err
	}

	return postResp.ID, nil
}

func (w *WorkerManager) PollJob(jobID string) (*JobPollResponse, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/jobs/%s", w.port, jobID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+w.token)

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var pollResp JobPollResponse
	if err := json.NewDecoder(resp.Body).Decode(&pollResp); err != nil {
		return nil, err
	}

	return &pollResp, nil
}

func (w *WorkerManager) GetDaemonLogs() (string, error) {
	localToolsDir := w.resolvePath("local-tools")
	logPath := filepath.Join(localToolsDir, "daemon.log")
	if _, err := os.Stat(localToolsDir); err != nil {
		logPath = resolveDaemonLogPath()
	}

	file, err := os.Open(logPath)
	if err != nil {
		return fmt.Sprintf("No daemon log found at %s: %v", logPath, err), nil
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > 200 {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}
