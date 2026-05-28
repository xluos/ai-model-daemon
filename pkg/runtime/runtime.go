package runtime

import (
	"fmt"
	"sync"
)

type RuntimeManager struct {
	mu      sync.RWMutex
	llm     *LLMRuntime
	whisper *WhisperRuntime
	binMgr  *BinaryManager
}

func NewRuntimeManager() *RuntimeManager {
	binMgr := NewBinaryManager()
	return &RuntimeManager{
		llm:     NewLLMRuntime(binMgr),
		whisper: NewWhisperRuntime(binMgr),
		binMgr:  binMgr,
	}
}

func (rm *RuntimeManager) LLM() *LLMRuntime {
	return rm.llm
}

func (rm *RuntimeManager) Whisper() *WhisperRuntime {
	return rm.whisper
}

func (rm *RuntimeManager) BinaryManager() *BinaryManager {
	return rm.binMgr
}

type FullStatus struct {
	LLM      LLMStatus     `json:"llm"`
	Whisper  WhisperStatus `json:"whisper"`
	Binaries struct {
		LlamaServer   BinaryInfo `json:"llamaServer"`
		WhisperServer BinaryInfo `json:"whisperServer"`
	} `json:"binaries"`
}

func (rm *RuntimeManager) Status() FullStatus {
	s := FullStatus{
		LLM:     rm.llm.Status(),
		Whisper: rm.whisper.Status(),
	}
	s.Binaries.LlamaServer = rm.binMgr.Status(BinaryLlamaServer)
	s.Binaries.WhisperServer = rm.binMgr.Status(BinaryWhisperServer)
	return s
}

// EnsureLLM starts the LLM runtime with the given model, switching if necessary.
func (rm *RuntimeManager) EnsureLLM(modelID string, opts LLMOpts) error {
	current := rm.llm.LoadedModel()
	if current == modelID && rm.llm.IsReady() {
		return nil
	}
	if current != "" && current != modelID {
		return rm.llm.ModelSwitch(modelID, opts)
	}
	return rm.llm.Start(modelID, opts)
}

// EnsureWhisper starts the Whisper runtime with the given model, switching if necessary.
func (rm *RuntimeManager) EnsureWhisper(modelID string, opts WhisperOpts) error {
	current := rm.whisper.LoadedModel()
	if current == modelID && rm.whisper.IsReady() {
		return nil
	}
	if current != "" && current != modelID {
		return rm.whisper.ModelSwitch(modelID, opts)
	}
	return rm.whisper.Start(modelID, opts)
}

func (rm *RuntimeManager) StopAll() {
	rm.llm.Stop()
	rm.whisper.Stop()
}

// RuntimeKindFor returns whether a model should use the LLM or Whisper runtime.
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
