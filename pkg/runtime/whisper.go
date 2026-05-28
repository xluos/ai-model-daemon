package runtime

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xluos/ai-model-daemon/pkg/manifest"
	"github.com/xluos/ai-model-daemon/pkg/storage"
)

type WhisperOpts struct {
	Threads int
}

type WhisperRuntime struct {
	mu          sync.Mutex
	proc        *ProcessHandle
	binMgr      *BinaryManager
	loadedModel string
	port        int
	startedAt   time.Time
	opts        WhisperOpts
	inFlight    atomic.Int64

	crashCount  int
	lastCrashAt time.Time
}

func NewWhisperRuntime(binMgr *BinaryManager) *WhisperRuntime {
	r := &WhisperRuntime{
		binMgr: binMgr,
	}
	r.proc = NewProcess(r.onCrash)
	return r
}

type WhisperStatus struct {
	State     ProcessState `json:"state"`
	ModelID   string       `json:"modelId,omitempty"`
	Port      int          `json:"port,omitempty"`
	StartedAt *time.Time   `json:"startedAt,omitempty"`
	InFlight  int64        `json:"inFlight"`
	Error     string       `json:"error,omitempty"`
}

func (r *WhisperRuntime) Status() WhisperStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := WhisperStatus{
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

func (r *WhisperRuntime) LoadedModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadedModel
}

func (r *WhisperRuntime) Port() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.port
}

func (r *WhisperRuntime) ProxyURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", r.port)
}

func (r *WhisperRuntime) IsReady() bool {
	return r.proc.State() == StateReady
}

func (r *WhisperRuntime) AcquireSlot() {
	r.inFlight.Add(1)
}

func (r *WhisperRuntime) ReleaseSlot() {
	r.inFlight.Add(-1)
}

func (r *WhisperRuntime) InFlightCount() int64 {
	return r.inFlight.Load()
}

func (r *WhisperRuntime) Logs() []string {
	return r.proc.Logs()
}

func (r *WhisperRuntime) Start(modelID string, opts WhisperOpts) error {
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

	binaryPath, err := r.binMgr.Resolve(BinaryWhisperServer)
	if err != nil {
		return err
	}

	m := manifest.Find(modelID)
	if m == nil {
		return fmt.Errorf("model %q not found in manifest", modelID)
	}

	modelPath := resolveWhisperModelPath(m)
	if modelPath == "" {
		return fmt.Errorf("model %q files not ready", modelID)
	}

	port, err := findFreePort()
	if err != nil {
		return fmt.Errorf("find free port: %w", err)
	}

	if opts.Threads <= 0 {
		opts.Threads = 4
	}

	args := []string{
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--model", modelPath,
		"--threads", strconv.Itoa(opts.Threads),
	}

	r.port = port
	r.opts = opts
	r.loadedModel = modelID
	r.startedAt = time.Now()

	cfg := ProcessConfig{
		Binary:         binaryPath,
		Args:           args,
		Env:            libraryPathEnv(binaryPath),
		HealthURL:      fmt.Sprintf("http://127.0.0.1:%d/health", port),
		HealthTimeout:  30 * time.Second,
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

func (r *WhisperRuntime) Stop() error {
	r.mu.Lock()
	r.loadedModel = ""
	r.port = 0
	r.mu.Unlock()
	return r.proc.Stop(5 * time.Second)
}

func (r *WhisperRuntime) ModelSwitch(modelID string, opts WhisperOpts) error {
	deadline := time.Now().Add(30 * time.Second)
	for r.inFlight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}

	if err := r.Stop(); err != nil {
		return fmt.Errorf("stop current model: %w", err)
	}

	return r.Start(modelID, opts)
}

func (r *WhisperRuntime) onCrash(err error) {
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

func resolveWhisperModelPath(m *manifest.Model) string {
	for _, f := range m.Files {
		if storage.IsFileReady(m.ID, f.Filename, f.Bytes) {
			return storage.ModelFilePath(m.ID, f.Filename)
		}
	}
	return ""
}
