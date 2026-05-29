package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setHome points the user home dir at a temp dir so storage paths are isolated.
// On Windows storage.Dir() uses LOCALAPPDATA instead of the home dir.
func setHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", tmp)
	} else {
		t.Setenv("HOME", tmp)
	}
	return tmp
}

func TestDirAndPaths(t *testing.T) {
	setHome(t)

	dir := Dir()
	if !strings.HasSuffix(dir, "AIModels") {
		t.Errorf("Dir() = %q, want suffix AIModels", dir)
	}

	if got := SocketPath(); got != filepath.Join(dir, ".daemon.sock") {
		t.Errorf("SocketPath() = %q", got)
	}
	if got := PIDPath(); got != filepath.Join(dir, ".daemon.pid") {
		t.Errorf("PIDPath() = %q", got)
	}
	if got := TokenPath(); got != filepath.Join(dir, ".daemon.token") {
		t.Errorf("TokenPath() = %q", got)
	}
	if got := HTTPAddrPath(); got != filepath.Join(dir, ".daemon.http") {
		t.Errorf("HTTPAddrPath() = %q", got)
	}
	if got := ModelDir("m1"); got != filepath.Join(dir, "m1") {
		t.Errorf("ModelDir() = %q", got)
	}
	if got := ModelFilePath("m1", "f.bin"); got != filepath.Join(dir, "m1", "f.bin") {
		t.Errorf("ModelFilePath() = %q", got)
	}
}

func TestEnsureDirAndModelDir(t *testing.T) {
	setHome(t)

	if err := EnsureDir(); err != nil {
		t.Fatalf("EnsureDir() error: %v", err)
	}
	if info, err := os.Stat(Dir()); err != nil || !info.IsDir() {
		t.Fatalf("Dir not created: %v", err)
	}

	if err := EnsureModelDir("modelX"); err != nil {
		t.Fatalf("EnsureModelDir() error: %v", err)
	}
	if info, err := os.Stat(ModelDir("modelX")); err != nil || !info.IsDir() {
		t.Fatalf("model dir not created: %v", err)
	}
}

func TestExtractedDirPath(t *testing.T) {
	setHome(t)
	dir := Dir()

	tests := []struct {
		filename string
		want     string
	}{
		{"PP-OCRv5_server_det_infer.tar", filepath.Join(dir, "m", "PP-OCRv5_server_det_infer")},
		{"model.tar.gz", filepath.Join(dir, "m", "model")},
		{"model.tgz", filepath.Join(dir, "m", "model")},
		{"MODEL.TAR", filepath.Join(dir, "m", "MODEL")}, // case-insensitive suffix match
		{"weights.bin", ""}, // not an archive
		{"noext", ""},
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			if got := ExtractedDirPath("m", tt.filename); got != tt.want {
				t.Errorf("ExtractedDirPath(m, %q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestIsFileReadyExactSize(t *testing.T) {
	setHome(t)
	_ = EnsureModelDir("m")

	path := ModelFilePath("m", "f.bin")
	if err := os.WriteFile(path, make([]byte, 1000), 0644); err != nil {
		t.Fatal(err)
	}

	// Within 5% tolerance.
	if !IsFileReady("m", "f.bin", 1000) {
		t.Error("exact size should be ready")
	}
	if !IsFileReady("m", "f.bin", 960) { // 1000 within [912,1008]
		t.Error("size within +5% tolerance should be ready")
	}
	if !IsFileReady("m", "f.bin", 1040) { // 1000 within [988,1092]
		t.Error("size within -5% tolerance should be ready")
	}

	// Outside tolerance.
	if IsFileReady("m", "f.bin", 2000) {
		t.Error("size far below expected should NOT be ready")
	}
	if IsFileReady("m", "f.bin", 500) {
		t.Error("size far above expected should NOT be ready")
	}
}

func TestIsFileReadyZeroExpected(t *testing.T) {
	setHome(t)
	_ = EnsureModelDir("m")

	empty := ModelFilePath("m", "empty.bin")
	_ = os.WriteFile(empty, nil, 0644)
	if IsFileReady("m", "empty.bin", 0) {
		t.Error("empty file should NOT be ready even with expected=0")
	}

	nonEmpty := ModelFilePath("m", "data.bin")
	_ = os.WriteFile(nonEmpty, []byte("x"), 0644)
	if !IsFileReady("m", "data.bin", 0) {
		t.Error("non-empty file with expected=0 should be ready")
	}
}

func TestIsFileReadyMissing(t *testing.T) {
	setHome(t)
	if IsFileReady("m", "ghost.bin", 100) {
		t.Error("missing file should not be ready")
	}
}

func TestIsFileReadyExtractedDir(t *testing.T) {
	setHome(t)
	_ = EnsureModelDir("m")

	// No .tar file, but the extracted directory exists → ready.
	extractedDir := ExtractedDirPath("m", "bundle.tar")
	if extractedDir == "" {
		t.Fatal("expected non-empty extracted dir path for .tar")
	}
	if err := os.MkdirAll(extractedDir, 0755); err != nil {
		t.Fatal(err)
	}
	if !IsFileReady("m", "bundle.tar", 12345) {
		t.Error("extracted dir present should mark tar archive ready")
	}
}
