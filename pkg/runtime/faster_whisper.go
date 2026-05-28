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
	"github.com/xluos/ai-model-daemon/pkg/storage"
)

type FasterWhisperOpts struct {
	Threads     int
	Device      string
	ComputeType string
}

type FasterWhisperRuntime struct {
	mu          sync.Mutex
	proc        *ProcessHandle
	binDir      string
	loadedModel string
	port        int
	startedAt   time.Time
	opts        FasterWhisperOpts
	inFlight    atomic.Int64

	crashCount  int
	lastCrashAt time.Time
}

func NewFasterWhisperRuntime(binDir string) *FasterWhisperRuntime {
	r := &FasterWhisperRuntime{binDir: binDir}
	r.proc = NewProcess(r.onCrash)
	return r
}

func (r *FasterWhisperRuntime) Kind() string { return "faster-whisper" }

func (r *FasterWhisperRuntime) Ensure(modelID string, opts any) error {
	var fwOpts FasterWhisperOpts
	switch v := opts.(type) {
	case FasterWhisperOpts:
		fwOpts = v
	case nil:
	default:
		return fmt.Errorf("faster-whisper runtime: unsupported opts type %T", opts)
	}

	current := r.LoadedModel()
	if current == modelID && r.IsReady() {
		return nil
	}
	if current != "" && current != modelID {
		return r.ModelSwitch(modelID, fwOpts)
	}
	return r.Start(modelID, fwOpts)
}

func (r *FasterWhisperRuntime) MaxParallel() int { return 1 }

type FasterWhisperStatus struct {
	State     ProcessState `json:"state"`
	ModelID   string       `json:"modelId,omitempty"`
	Port      int          `json:"port,omitempty"`
	StartedAt *time.Time   `json:"startedAt,omitempty"`
	InFlight  int64        `json:"inFlight"`
	Error     string       `json:"error,omitempty"`
}

func (r *FasterWhisperRuntime) StatusTyped() FasterWhisperStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := FasterWhisperStatus{
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

func (r *FasterWhisperRuntime) Status() any { return r.StatusTyped() }

func (r *FasterWhisperRuntime) LoadedModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadedModel
}

func (r *FasterWhisperRuntime) ProxyURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", r.port)
}

func (r *FasterWhisperRuntime) IsReady() bool {
	return r.proc.State() == StateReady
}

func (r *FasterWhisperRuntime) AcquireSlot()         { r.inFlight.Add(1) }
func (r *FasterWhisperRuntime) ReleaseSlot()         { r.inFlight.Add(-1) }
func (r *FasterWhisperRuntime) InFlightCount() int64 { return r.inFlight.Load() }
func (r *FasterWhisperRuntime) Logs() []string       { return r.proc.Logs() }

func (r *FasterWhisperRuntime) Start(modelID string, opts FasterWhisperOpts) error {
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

	modelPath := resolveFasterWhisperModelPath(m)
	if modelPath == "" {
		return fmt.Errorf("model %q files not ready", modelID)
	}

	pythonPath, err := findPython()
	if err != nil {
		return err
	}

	scriptPath := filepath.Join(r.binDir, "faster_whisper_server.py")
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("faster_whisper_server.py not found at %s", scriptPath)
	}

	port, err := findFreePort()
	if err != nil {
		return fmt.Errorf("find free port: %w", err)
	}

	if opts.Threads <= 0 {
		opts.Threads = 4
	}
	if opts.Device == "" {
		opts.Device = "auto"
	}
	if opts.ComputeType == "" {
		opts.ComputeType = "auto"
	}

	args := []string{
		scriptPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--model", modelPath,
		"--device", opts.Device,
		"--compute-type", opts.ComputeType,
		"--threads", strconv.Itoa(opts.Threads),
	}

	r.port = port
	r.opts = opts
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

func (r *FasterWhisperRuntime) Stop() error {
	r.mu.Lock()
	r.loadedModel = ""
	r.port = 0
	r.mu.Unlock()
	return r.proc.Stop(5 * time.Second)
}

func (r *FasterWhisperRuntime) ModelSwitch(modelID string, opts FasterWhisperOpts) error {
	deadline := time.Now().Add(30 * time.Second)
	for r.inFlight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if err := r.Stop(); err != nil {
		return fmt.Errorf("stop current model: %w", err)
	}
	return r.Start(modelID, opts)
}

func (r *FasterWhisperRuntime) onCrash(err error) {
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

// resolveFasterWhisperModelPath returns the model directory path for faster-whisper.
func resolveFasterWhisperModelPath(m *manifest.Model) string {
	modelDir := storage.ModelDir(m.ID)
	for _, f := range m.Files {
		if !storage.IsFileReady(m.ID, f.Filename, f.Bytes) {
			return ""
		}
	}
	return modelDir
}
