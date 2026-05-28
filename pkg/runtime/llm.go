package runtime

import (
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xluos/ai-model-daemon/pkg/manifest"
	"github.com/xluos/ai-model-daemon/pkg/storage"
)

type LLMOpts struct {
	ContextSize int
	GPULayers   int
	Parallel    int
}

type LLMRuntime struct {
	mu          sync.Mutex
	proc        *ProcessHandle
	binMgr      *BinaryManager
	loadedModel string
	port        int
	startedAt   time.Time
	opts        LLMOpts
	inFlight    atomic.Int64

	crashCount  int
	lastCrashAt time.Time
}

func NewLLMRuntime(binMgr *BinaryManager) *LLMRuntime {
	r := &LLMRuntime{
		binMgr: binMgr,
	}
	r.proc = NewProcess(r.onCrash)
	return r
}

type LLMStatus struct {
	State       ProcessState `json:"state"`
	ModelID     string       `json:"modelId,omitempty"`
	Port        int          `json:"port,omitempty"`
	StartedAt   *time.Time   `json:"startedAt,omitempty"`
	InFlight    int64        `json:"inFlight"`
	ContextSize int          `json:"contextSize,omitempty"`
	Parallel    int          `json:"parallel,omitempty"`
	Error       string       `json:"error,omitempty"`
}

func (r *LLMRuntime) Status() LLMStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := LLMStatus{
		State:       r.proc.State(),
		ModelID:     r.loadedModel,
		Port:        r.port,
		InFlight:    r.inFlight.Load(),
		ContextSize: r.opts.ContextSize,
		Parallel:    r.opts.Parallel,
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

func (r *LLMRuntime) LoadedModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadedModel
}

func (r *LLMRuntime) Port() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.port
}

func (r *LLMRuntime) ProxyURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", r.port)
}

func (r *LLMRuntime) IsReady() bool {
	return r.proc.State() == StateReady
}

func (r *LLMRuntime) AcquireSlot() {
	r.inFlight.Add(1)
}

func (r *LLMRuntime) ReleaseSlot() {
	r.inFlight.Add(-1)
}

func (r *LLMRuntime) InFlightCount() int64 {
	return r.inFlight.Load()
}

func (r *LLMRuntime) Logs() []string {
	return r.proc.Logs()
}

func (r *LLMRuntime) Start(modelID string, opts LLMOpts) error {
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

	binaryPath, err := r.binMgr.Resolve(BinaryLlamaServer)
	if err != nil {
		return err
	}

	m := manifest.Find(modelID)
	if m == nil {
		return fmt.Errorf("model %q not found in manifest", modelID)
	}

	llmPath, mmprojPath := resolveModelPaths(m)
	if llmPath == "" {
		return fmt.Errorf("model %q has no LLM file", modelID)
	}

	port, err := findFreePort()
	if err != nil {
		return fmt.Errorf("find free port: %w", err)
	}

	if opts.ContextSize <= 0 {
		opts.ContextSize = m.ContextSize
		if opts.ContextSize <= 0 {
			opts.ContextSize = 8192
		}
	}
	if opts.GPULayers <= 0 {
		opts.GPULayers = 999
	}
	if opts.Parallel <= 0 {
		opts.Parallel = 1
	}

	args := []string{
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--model", llmPath,
		"--ctx-size", strconv.Itoa(opts.ContextSize),
		"--n-gpu-layers", strconv.Itoa(opts.GPULayers),
		"--parallel", strconv.Itoa(opts.Parallel),
		"--reasoning-format", "deepseek",
	}
	if mmprojPath != "" {
		args = append(args, "--mmproj", mmprojPath)
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

func (r *LLMRuntime) Stop() error {
	r.mu.Lock()
	r.loadedModel = ""
	r.port = 0
	r.mu.Unlock()
	return r.proc.Stop(5 * time.Second)
}

// ModelSwitch stops the current model and starts a new one.
// It waits for in-flight requests to drain before switching.
func (r *LLMRuntime) ModelSwitch(modelID string, opts LLMOpts) error {
	deadline := time.Now().Add(30 * time.Second)
	for r.inFlight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}

	if err := r.Stop(); err != nil {
		return fmt.Errorf("stop current model: %w", err)
	}

	return r.Start(modelID, opts)
}

func (r *LLMRuntime) onCrash(err error) {
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

func resolveModelPaths(m *manifest.Model) (llmPath, mmprojPath string) {
	for _, f := range m.Files {
		p := storage.ModelFilePath(m.ID, f.Filename)
		switch f.Role {
		case "llm", "":
			if storage.IsFileReady(m.ID, f.Filename, f.Bytes) {
				llmPath = p
			}
		case "mmproj":
			if storage.IsFileReady(m.ID, f.Filename, f.Bytes) {
				mmprojPath = p
			}
		}
	}
	return
}

func libraryPathEnv(binaryPath string) []string {
	dir := filepath.Dir(binaryPath)
	switch runtime.GOOS {
	case "darwin":
		return []string{"DYLD_LIBRARY_PATH=" + dir}
	case "linux":
		return []string{"LD_LIBRARY_PATH=" + dir}
	default:
		return nil
	}
}

func findFreePort() (int, error) {
	if runtime.GOOS == "windows" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		return port, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}
