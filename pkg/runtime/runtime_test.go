package runtime

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRingBufferUnderCapacity(t *testing.T) {
	rb := newRingBuffer(5)
	rb.Add("a")
	rb.Add("b")
	got := rb.Lines()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("Lines() = %v, want [a b]", got)
	}
}

func TestRingBufferWrapAround(t *testing.T) {
	rb := newRingBuffer(3)
	for _, s := range []string{"1", "2", "3", "4", "5"} {
		rb.Add(s)
	}
	// Capacity 3, last 3 lines in order should be 3,4,5.
	got := rb.Lines()
	want := []string{"3", "4", "5"}
	if len(got) != 3 {
		t.Fatalf("Lines() len = %d, want 3", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Lines()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRingBufferExactCapacity(t *testing.T) {
	rb := newRingBuffer(3)
	rb.Add("a")
	rb.Add("b")
	rb.Add("c")
	got := rb.Lines()
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("Lines() = %v, want [a b c]", got)
	}
}

func TestRingBufferReset(t *testing.T) {
	rb := newRingBuffer(3)
	rb.Add("a")
	rb.Add("b")
	rb.Reset()
	if got := rb.Lines(); len(got) != 0 {
		t.Errorf("after Reset Lines() = %v, want empty", got)
	}
	rb.Add("c")
	if got := rb.Lines(); len(got) != 1 || got[0] != "c" {
		t.Errorf("after Reset+Add Lines() = %v, want [c]", got)
	}
}

func TestProcessConfigDefaults(t *testing.T) {
	c := ProcessConfig{}
	c.defaults()
	if c.HealthTimeout != 60*time.Second {
		t.Errorf("HealthTimeout = %v, want 60s", c.HealthTimeout)
	}
	if c.HealthInterval != 500*time.Millisecond {
		t.Errorf("HealthInterval = %v, want 500ms", c.HealthInterval)
	}
	if c.StopTimeout != 5*time.Second {
		t.Errorf("StopTimeout = %v, want 5s", c.StopTimeout)
	}

	// Explicit values are preserved.
	c2 := ProcessConfig{HealthTimeout: time.Second, HealthInterval: time.Second, StopTimeout: time.Second}
	c2.defaults()
	if c2.HealthTimeout != time.Second || c2.HealthInterval != time.Second || c2.StopTimeout != time.Second {
		t.Error("defaults() overwrote explicit values")
	}
}

func TestPlatformKey(t *testing.T) {
	key := platformKey()
	if !strings.HasPrefix(key, runtime.GOOS+"-") {
		t.Errorf("platformKey %q should start with %q", key, runtime.GOOS)
	}
	// amd64 must be normalized to x64.
	if runtime.GOARCH == "amd64" && !strings.HasSuffix(key, "-x64") {
		t.Errorf("amd64 should map to x64, got %q", key)
	}
	if runtime.GOARCH == "arm64" && !strings.HasSuffix(key, "-arm64") {
		t.Errorf("arm64 should stay arm64, got %q", key)
	}
}

func TestLlamaCppAssetName(t *testing.T) {
	got := llamaCppAssetName("llama-${REL}-bin-macos-arm64.zip")
	if strings.Contains(got, "${REL}") {
		t.Errorf("release placeholder not substituted: %q", got)
	}
	if !strings.Contains(got, llamaCppRelease) {
		t.Errorf("asset name %q should contain release %q", got, llamaCppRelease)
	}
}

func TestLibraryPathEnv(t *testing.T) {
	got := libraryPathEnv("/opt/llama/bin/llama-server")
	switch runtime.GOOS {
	case "darwin":
		if len(got) != 1 || !strings.HasPrefix(got[0], "DYLD_LIBRARY_PATH=") {
			t.Errorf("darwin libraryPathEnv = %v", got)
		}
		if !strings.HasSuffix(got[0], "/opt/llama/bin") {
			t.Errorf("library dir should be binary's dir, got %v", got)
		}
	case "linux":
		if len(got) != 1 || !strings.HasPrefix(got[0], "LD_LIBRARY_PATH=") {
			t.Errorf("linux libraryPathEnv = %v", got)
		}
	default:
		if got != nil {
			t.Errorf("unsupported platform libraryPathEnv = %v, want nil", got)
		}
	}
}

func TestFindFreePort(t *testing.T) {
	port, err := findFreePort()
	if err != nil {
		t.Fatalf("findFreePort error: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Errorf("port %d out of range", port)
	}
}

func TestRuntimeManagerRegistryDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())

	rm := NewRuntimeManager()
	for _, kind := range []string{"llm", "whisper", "ocr", "rapidocr", "faster-whisper"} {
		if rm.Get(kind) == nil {
			t.Errorf("expected built-in runtime %q to be registered", kind)
		}
	}
	if rm.Get("nonexistent") != nil {
		t.Error("Get(nonexistent) should return nil")
	}

	kinds := rm.Kinds()
	if len(kinds) < 5 {
		t.Errorf("expected >=5 kinds, got %d: %v", len(kinds), kinds)
	}
}

func TestRuntimeManagerValidateKind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	rm := NewRuntimeManager()

	if k, err := rm.ValidateKind(""); err != nil || k != "llm" {
		t.Errorf("ValidateKind(\"\") = %q, %v; want llm, nil", k, err)
	}
	if k, err := rm.ValidateKind("whisper"); err != nil || k != "whisper" {
		t.Errorf("ValidateKind(whisper) = %q, %v", k, err)
	}
	if _, err := rm.ValidateKind("bogus"); err == nil {
		t.Error("ValidateKind(bogus) should error")
	}
}

func TestRuntimeManagerDuplicatePanics(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	rm := NewRuntimeManager()

	defer func() {
		if recover() == nil {
			t.Error("registering a duplicate kind should panic")
		}
	}()
	rm.Register(NewOCRRuntime(t.TempDir())) // "ocr" already registered
}

func TestRuntimeKindForBackwardCompat(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "llm", false},
		{"llm", "llm", false},
		{"whisper", "whisper", false},
		{"ocr", "", true}, // legacy function only knows llm/whisper
		{"bogus", "", true},
	}
	for _, tt := range tests {
		got, err := RuntimeKindFor(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("RuntimeKindFor(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if got != tt.want {
			t.Errorf("RuntimeKindFor(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRuntimeManagerEnsureUnknownKind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	rm := NewRuntimeManager()
	if err := rm.Ensure("nope", "model", nil); err == nil {
		t.Error("Ensure with unknown kind should error")
	}
}
