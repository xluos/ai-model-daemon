package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xluos/ai-model-daemon/pkg/manifest"
)

type RapidOCRRuntime struct {
	mu          sync.Mutex
	proc        *ProcessHandle
	binDir      string
	loadedModel string
	port        int
	startedAt   time.Time
	inFlight    atomic.Int64

	crashCount  int
	lastCrashAt time.Time
}

func NewRapidOCRRuntime(binDir string) *RapidOCRRuntime {
	r := &RapidOCRRuntime{binDir: binDir}
	r.proc = NewProcess(r.onCrash)
	return r
}

func (r *RapidOCRRuntime) Kind() string { return "rapidocr" }

func (r *RapidOCRRuntime) Ensure(modelID string, opts any) error {
	if opts != nil {
		return fmt.Errorf("rapidocr runtime: unsupported opts type %T", opts)
	}
	current := r.LoadedModel()
	if current == modelID && r.IsReady() {
		return nil
	}
	if current != "" && current != modelID {
		return r.ModelSwitch(modelID)
	}
	return r.Start(modelID)
}

func (r *RapidOCRRuntime) MaxParallel() int { return 1 }

func (r *RapidOCRRuntime) StatusTyped() OCRStatus {
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

func (r *RapidOCRRuntime) Status() any { return r.StatusTyped() }

func (r *RapidOCRRuntime) LoadedModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadedModel
}

func (r *RapidOCRRuntime) ProxyURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", r.port)
}

func (r *RapidOCRRuntime) IsReady() bool        { return r.proc.State() == StateReady }
func (r *RapidOCRRuntime) AcquireSlot()         { r.inFlight.Add(1) }
func (r *RapidOCRRuntime) ReleaseSlot()         { r.inFlight.Add(-1) }
func (r *RapidOCRRuntime) InFlightCount() int64 { return r.inFlight.Load() }
func (r *RapidOCRRuntime) Logs() []string       { return r.proc.Logs() }

func (r *RapidOCRRuntime) Start(modelID string) error {
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
	if m.RuntimeKind != "rapidocr" {
		return fmt.Errorf("model %q is not a RapidOCR model", modelID)
	}

	pythonPath, err := findPython()
	if err != nil {
		return err
	}

	scriptPath := filepath.Join(r.binDir, "rapidocr_server.py")
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("rapidocr_server.py not found at %s", scriptPath)
	}

	port, err := findFreePort()
	if err != nil {
		return fmt.Errorf("find free port: %w", err)
	}

	args := []string{
		scriptPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--model", modelID,
	}

	r.port = port
	r.loadedModel = modelID
	r.startedAt = time.Now()

	cfg := ProcessConfig{
		Binary:         pythonPath,
		Args:           args,
		HealthURL:      fmt.Sprintf("http://127.0.0.1:%d/health", port),
		HealthTimeout:  120 * time.Second,
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

func (r *RapidOCRRuntime) Stop() error {
	r.mu.Lock()
	r.loadedModel = ""
	r.port = 0
	r.mu.Unlock()
	return r.proc.Stop(5 * time.Second)
}

func (r *RapidOCRRuntime) ModelSwitch(modelID string) error {
	deadline := time.Now().Add(30 * time.Second)
	for r.inFlight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if err := r.Stop(); err != nil {
		return fmt.Errorf("stop current model: %w", err)
	}
	return r.Start(modelID)
}

func (r *RapidOCRRuntime) onCrash(err error) {
	r.mu.Lock()
	now := time.Now()
	if now.Sub(r.lastCrashAt) > 60*time.Second {
		r.crashCount = 0
	}
	r.crashCount++
	r.lastCrashAt = now
	breaker := r.crashCount >= 3
	modelID := r.loadedModel
	r.mu.Unlock()

	if breaker || modelID == "" {
		return
	}

	backoff := time.Duration(1<<(r.crashCount-1)) * time.Second
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}
	time.Sleep(backoff)
	r.Start(modelID)
}
