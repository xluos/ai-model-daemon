package runtime

import (
	"encoding/json"
	"fmt"
	"sync"
)

type RuntimeManager struct {
	mu       sync.RWMutex
	runtimes map[string]Runtime
	binMgr   *BinaryManager
}

func NewRuntimeManager() *RuntimeManager {
	binMgr := NewBinaryManager()
	rm := &RuntimeManager{
		runtimes: make(map[string]Runtime),
		binMgr:   binMgr,
	}
	rm.Register(NewLLMRuntime(binMgr))
	rm.Register(NewWhisperRuntime(binMgr))
	rm.Register(NewOCRRuntime(binMgr.BinDir()))
	rm.Register(NewFasterWhisperRuntime(binMgr.BinDir()))
	return rm
}

func (rm *RuntimeManager) Register(rt Runtime) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if _, exists := rm.runtimes[rt.Kind()]; exists {
		panic("duplicate runtime kind: " + rt.Kind())
	}
	rm.runtimes[rt.Kind()] = rt
}

func (rm *RuntimeManager) Get(kind string) Runtime {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.runtimes[kind]
}

func (rm *RuntimeManager) Kinds() []string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	kinds := make([]string, 0, len(rm.runtimes))
	for k := range rm.runtimes {
		kinds = append(kinds, k)
	}
	return kinds
}

// LLM returns the LLM runtime. Kept for backward compatibility.
func (rm *RuntimeManager) LLM() *LLMRuntime {
	return rm.Get("llm").(*LLMRuntime)
}

// Whisper returns the Whisper runtime. Kept for backward compatibility.
func (rm *RuntimeManager) Whisper() *WhisperRuntime {
	return rm.Get("whisper").(*WhisperRuntime)
}

func (rm *RuntimeManager) BinaryManager() *BinaryManager {
	return rm.binMgr
}

type FullStatus struct {
	Runtimes map[string]any        `json:"-"`
	Binaries map[string]BinaryInfo `json:"binaries"`
}

func (s FullStatus) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(s.Runtimes)+1)
	for k, v := range s.Runtimes {
		m[k] = v
	}
	m["binaries"] = s.Binaries
	return json.Marshal(m)
}

func (rm *RuntimeManager) Status() FullStatus {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	s := FullStatus{
		Runtimes: make(map[string]any, len(rm.runtimes)),
		Binaries: make(map[string]BinaryInfo),
	}
	for kind, rt := range rm.runtimes {
		s.Runtimes[kind] = rt.Status()
	}
	s.Binaries["llamaServer"] = rm.binMgr.Status(BinaryLlamaServer)
	s.Binaries["whisperServer"] = rm.binMgr.Status(BinaryWhisperServer)
	return s
}

func (rm *RuntimeManager) Ensure(kind, modelID string, opts any) error {
	rt := rm.Get(kind)
	if rt == nil {
		return fmt.Errorf("unknown runtime kind: %s", kind)
	}
	return rt.Ensure(modelID, opts)
}

// EnsureLLM starts the LLM runtime with the given model. Kept for backward compatibility.
func (rm *RuntimeManager) EnsureLLM(modelID string, opts LLMOpts) error {
	return rm.Get("llm").Ensure(modelID, opts)
}

// EnsureWhisper starts the Whisper runtime with the given model. Kept for backward compatibility.
func (rm *RuntimeManager) EnsureWhisper(modelID string, opts WhisperOpts) error {
	return rm.Get("whisper").Ensure(modelID, opts)
}

func (rm *RuntimeManager) StopAll() {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	for _, rt := range rm.runtimes {
		rt.Stop()
	}
}

// ValidateKind checks if a runtime kind string is registered. Empty defaults to "llm".
func (rm *RuntimeManager) ValidateKind(runtimeKind string) (string, error) {
	if runtimeKind == "" {
		runtimeKind = "llm"
	}
	if rm.Get(runtimeKind) != nil {
		return runtimeKind, nil
	}
	return "", fmt.Errorf("unknown runtime kind: %s", runtimeKind)
}

// RuntimeKindFor validates a runtime kind string. Kept for backward compatibility.
func RuntimeKindFor(runtimeKind string) (string, error) {
	switch runtimeKind {
	case "llm", "":
		return "llm", nil
	case "whisper":
		return "whisper", nil
	default:
		return "", fmt.Errorf("unknown runtime kind: %s", runtimeKind)
	}
}
