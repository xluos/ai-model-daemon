package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xluos/ai-model-daemon/pkg/manifest"
	"github.com/xluos/ai-model-daemon/pkg/storage"
)

type OCROpts struct {
	Lang string
}

type OCRRuntime struct {
	mu          sync.Mutex
	proc        *ProcessHandle
	binDir      string
	loadedModel string
	port        int
	startedAt   time.Time
	opts        OCROpts
	inFlight    atomic.Int64

	crashCount  int
	lastCrashAt time.Time
}

func NewOCRRuntime(binDir string) *OCRRuntime {
	r := &OCRRuntime{binDir: binDir}
	r.proc = NewProcess(r.onCrash)
	return r
}

func (r *OCRRuntime) Kind() string { return "ocr" }

func (r *OCRRuntime) Ensure(modelID string, opts any) error {
	var ocrOpts OCROpts
	switch v := opts.(type) {
	case OCROpts:
		ocrOpts = v
	case nil:
	default:
		return fmt.Errorf("ocr runtime: unsupported opts type %T", opts)
	}

	current := r.LoadedModel()
	if current == modelID && r.IsReady() {
		return nil
	}
	if current != "" && current != modelID {
		return r.ModelSwitch(modelID, ocrOpts)
	}
	return r.Start(modelID, ocrOpts)
}

func (r *OCRRuntime) MaxParallel() int { return 1 }

type OCRStatus struct {
	State     ProcessState `json:"state"`
	ModelID   string       `json:"modelId,omitempty"`
	Port      int          `json:"port,omitempty"`
	StartedAt *time.Time   `json:"startedAt,omitempty"`
	InFlight  int64        `json:"inFlight"`
	Error     string       `json:"error,omitempty"`
}

func (r *OCRRuntime) StatusTyped() OCRStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := OCRStatus{
		State:    r.proc.State(),
		ModelID:  r.loadedModel,
		Port:     r.port,
		InFlight: r.inFlight.Load(),
	}
	if !r.startedAt.IsZero() {
		t := r.startedAt
		s.StartedAt = &t
	}
	if err := r.proc.Error(); err != nil {
		s.Error = err.Error()
	}
	return s
}

func (r *OCRRuntime) Status() any { return r.StatusTyped() }

func (r *OCRRuntime) LoadedModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadedModel
}

func (r *OCRRuntime) ProxyURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", r.port)
}

func (r *OCRRuntime) IsReady() bool {
	return r.proc.State() == StateReady
}

func (r *OCRRuntime) AcquireSlot()         { r.inFlight.Add(1) }
func (r *OCRRuntime) ReleaseSlot()         { r.inFlight.Add(-1) }
func (r *OCRRuntime) InFlightCount() int64 { return r.inFlight.Load() }
func (r *OCRRuntime) Logs() []string       { return r.proc.Logs() }

func (r *OCRRuntime) Start(modelID string, opts OCROpts) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.proc.State() == StateReady && r.loadedModel == modelID {
		return nil
	}

	if r.proc.State() != StateIdle && r.proc.State() != StateError {
		r.mu.Unlock()
		r.Stop()
		r.mu.Lock()
	}

	m := manifest.Find(modelID)
	if m == nil {
		return fmt.Errorf("model %q not found in manifest", modelID)
	}

	detPath, recPath, clsPath := resolveOCRModelPaths(m)
	if detPath == "" || recPath == "" {
		return fmt.Errorf("model %q: detection or recognition model not ready", modelID)
	}

	pythonPath, err := findPython()
	if err != nil {
		return err
	}

	scriptPath := filepath.Join(r.binDir, "ocr_server.py")
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("ocr_server.py not found at %s", scriptPath)
	}

	port, err := findFreePort()
	if err != nil {
		return fmt.Errorf("find free port: %w", err)
	}

	if opts.Lang == "" {
		opts.Lang = "ch"
	}

	args := []string{
		scriptPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--det", detPath,
		"--rec", recPath,
		"--lang", opts.Lang,
	}
	if clsPath != "" {
		args = append(args, "--cls", clsPath)
	}

	r.port = port
	r.opts = opts
	r.loadedModel = modelID
	r.startedAt = time.Now()

	cfg := ProcessConfig{
		Binary:         pythonPath,
		Args:           args,
		HealthURL:      fmt.Sprintf("http://127.0.0.1:%d/health", port),
		HealthTimeout:  60 * time.Second,
		HealthInterval: 500 * time.Millisecond,
		StopTimeout:    5 * time.Second,
	}

	r.mu.Unlock()
	err = r.proc.Start(cfg)
	r.mu.Lock()

	if err != nil {
		r.loadedModel = ""
		r.port = 0
		return err
	}
	return nil
}

func (r *OCRRuntime) Stop() error {
	r.mu.Lock()
	r.loadedModel = ""
	r.port = 0
	r.mu.Unlock()
	return r.proc.Stop(5 * time.Second)
}

func (r *OCRRuntime) ModelSwitch(modelID string, opts OCROpts) error {
	deadline := time.Now().Add(30 * time.Second)
	for r.inFlight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if err := r.Stop(); err != nil {
		return fmt.Errorf("stop current model: %w", err)
	}
	return r.Start(modelID, opts)
}

func (r *OCRRuntime) onCrash(err error) {
	r.mu.Lock()
	now := time.Now()
	if now.Sub(r.lastCrashAt) > 60*time.Second {
		r.crashCount = 0
	}
	r.crashCount++
	r.lastCrashAt = now
	breaker := r.crashCount >= 3
	r.mu.Unlock()

	if breaker {
		return
	}

	r.mu.Lock()
	modelID := r.loadedModel
	opts := r.opts
	r.mu.Unlock()

	if modelID != "" {
		backoff := time.Duration(1<<(r.crashCount-1)) * time.Second
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
		time.Sleep(backoff)
		r.Start(modelID, opts)
	}
}

// resolveOCRModelPaths resolves det/rec/cls model directories from manifest files.
// For tar archives, returns the extracted directory path.
func resolveOCRModelPaths(m *manifest.Model) (detPath, recPath, clsPath string) {
	for _, f := range m.Files {
		dir := resolveExtractedDir(m.ID, f)
		if dir == "" {
			continue
		}
		switch f.Role {
		case "det":
			detPath = dir
		case "rec":
			recPath = dir
		case "cls":
			clsPath = dir
		}
	}
	return
}

// resolveExtractedDir returns the extracted directory for a model file.
// For tar files, returns the extracted directory; for plain files, returns the file path.
func resolveExtractedDir(modelID string, f manifest.ModelFile) string {
	if !storage.IsFileReady(modelID, f.Filename, f.Bytes) {
		return ""
	}
	if dir := storage.ExtractedDirPath(modelID, f.Filename); dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return storage.ModelFilePath(modelID, f.Filename)
}

// findPython looks for a Python 3 interpreter.
func findPython() (string, error) {
	if p := os.Getenv("PYTHON_PATH"); p != "" {
		return p, nil
	}
	for _, name := range []string{"python3", "python"} {
		if p, err := findInPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("python3 not found; set PYTHON_PATH or ensure python3 is in PATH")
}

func findInPath(name string) (string, error) {
	path := os.Getenv("PATH")
	for _, dir := range strings.Split(path, string(os.PathListSeparator)) {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", name)
}
