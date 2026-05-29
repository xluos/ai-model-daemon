package manifest

import (
	"strings"
	"testing"
)

func TestModelTotalBytes(t *testing.T) {
	tests := []struct {
		name  string
		model Model
		want  int64
	}{
		{
			name:  "no files",
			model: Model{},
			want:  0,
		},
		{
			name: "single file",
			model: Model{Files: []ModelFile{
				{Filename: "a.bin", Bytes: 100},
			}},
			want: 100,
		},
		{
			name: "multiple files sum",
			model: Model{Files: []ModelFile{
				{Filename: "a.bin", Bytes: 100},
				{Filename: "b.bin", Bytes: 250},
				{Filename: "c.bin", Bytes: 0},
			}},
			want: 350,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.model.TotalBytes(); got != tt.want {
				t.Errorf("TotalBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestModelFindFile(t *testing.T) {
	m := Model{Files: []ModelFile{
		{Role: "llm", Filename: "model.gguf"},
		{Role: "mmproj", Filename: "mmproj.gguf"},
	}}

	if f := m.FindFile("llm"); f == nil || f.Filename != "model.gguf" {
		t.Errorf("FindFile(llm) = %v, want model.gguf", f)
	}
	if f := m.FindFile("mmproj"); f == nil || f.Filename != "mmproj.gguf" {
		t.Errorf("FindFile(mmproj) = %v, want mmproj.gguf", f)
	}
	if f := m.FindFile("nonexistent"); f != nil {
		t.Errorf("FindFile(nonexistent) = %v, want nil", f)
	}
}

func TestModelFindFileReturnsMutablePointer(t *testing.T) {
	m := Model{Files: []ModelFile{{Role: "llm", Filename: "a.gguf", Bytes: 1}}}
	f := m.FindFile("llm")
	if f == nil {
		t.Fatal("FindFile returned nil")
	}
	f.Bytes = 999
	if m.Files[0].Bytes != 999 {
		t.Errorf("FindFile should return pointer into slice; got %d", m.Files[0].Bytes)
	}
}

func TestFind(t *testing.T) {
	if m := Find("qwen3-4b-q4"); m == nil || m.ID != "qwen3-4b-q4" {
		t.Errorf("Find(qwen3-4b-q4) failed: %v", m)
	}
	if m := Find("does-not-exist"); m != nil {
		t.Errorf("Find(does-not-exist) = %v, want nil", m)
	}
	if m := Find(""); m != nil {
		t.Errorf("Find(\"\") = %v, want nil", m)
	}
}

func TestFindReturnsRegistryPointer(t *testing.T) {
	a := Find("qwen3-4b-q4")
	b := Find("qwen3-4b-q4")
	if a != b {
		t.Error("Find should return a stable pointer into Registry")
	}
}

// TestRegistryIntegrity guards the model registry against common authoring
// mistakes: duplicate IDs, empty required fields, and unknown runtime kinds.
func TestRegistryIntegrity(t *testing.T) {
	validKinds := map[string]bool{
		"":               true, // defaults to llm / no inference
		"llm":            true,
		"whisper":        true,
		"faster-whisper": true,
		"ocr":            true,
		"rapidocr":       true,
	}

	seen := make(map[string]bool)
	for _, m := range Registry {
		if m.ID == "" {
			t.Errorf("model %q has empty ID", m.Name)
		}
		if seen[m.ID] {
			t.Errorf("duplicate model ID: %q", m.ID)
		}
		seen[m.ID] = true

		if m.Name == "" {
			t.Errorf("model %q has empty Name", m.ID)
		}
		if !validKinds[m.RuntimeKind] {
			t.Errorf("model %q has unknown RuntimeKind %q", m.ID, m.RuntimeKind)
		}

		// Non-bundled models must declare at least one downloadable file with URLs.
		if !m.Bundled {
			if len(m.Files) == 0 {
				t.Errorf("non-bundled model %q has no files", m.ID)
			}
			for _, f := range m.Files {
				if f.Filename == "" {
					t.Errorf("model %q has a file with empty filename", m.ID)
				}
				if len(f.URLs) == 0 {
					t.Errorf("model %q file %q has no URLs", m.ID, f.Filename)
				}
			}
		}
	}
}

func TestHFURLs(t *testing.T) {
	urls := hfURLs("org/repo", "file.gguf")
	if len(urls) != 2 {
		t.Fatalf("hfURLs returned %d urls, want 2", len(urls))
	}
	if !strings.Contains(urls[0], "hf-mirror.com") {
		t.Errorf("first url should prefer mirror, got %q", urls[0])
	}
	if !strings.Contains(urls[1], "huggingface.co") {
		t.Errorf("second url should be huggingface, got %q", urls[1])
	}
	for _, u := range urls {
		if !strings.Contains(u, "org/repo/resolve/main/file.gguf") {
			t.Errorf("url missing repo/file path: %q", u)
		}
	}
}

func TestVLMFiles(t *testing.T) {
	files := vlmFiles("org/repo", "model.gguf", 100, "mmproj.gguf", 50)
	if len(files) != 2 {
		t.Fatalf("vlmFiles returned %d files, want 2", len(files))
	}
	if files[0].Role != "llm" || files[0].Filename != "model.gguf" || files[0].Bytes != 100 {
		t.Errorf("unexpected llm file: %+v", files[0])
	}
	if files[1].Role != "mmproj" || files[1].Filename != "mmproj.gguf" || files[1].Bytes != 50 {
		t.Errorf("unexpected mmproj file: %+v", files[1])
	}
}
