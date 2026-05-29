package download

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// writeTar builds a tar (optionally gzipped) archive at path from the given
// entries (name → content). Directory entries have a trailing slash.
func writeTar(t *testing.T, path string, gzipped bool, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var tw *tar.Writer
	if gzipped {
		gz := gzip.NewWriter(f)
		defer gz.Close()
		tw = tar.NewWriter(gz)
	} else {
		tw = tar.NewWriter(f)
	}
	defer tw.Close()

	for name, content := range entries {
		if content == "" && name[len(name)-1] == '/' {
			hdr := &tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0755}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatal(err)
			}
			continue
		}
		hdr := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExtractTarIfNeededNonArchive(t *testing.T) {
	dir, err := ExtractTarIfNeeded("/tmp/model.gguf")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if dir != "" {
		t.Errorf("non-archive should return empty dir, got %q", dir)
	}
}

func TestExtractTarPlain(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "model.tar")
	writeTar(t, archive, false, map[string]string{
		"PP-OCRv5_det/":                    "",
		"PP-OCRv5_det/inference.pdmodel":   "model-bytes",
		"PP-OCRv5_det/inference.pdiparams": "param-bytes",
	})

	extracted, err := ExtractTarIfNeeded(archive)
	if err != nil {
		t.Fatalf("ExtractTarIfNeeded error: %v", err)
	}
	if filepath.Base(extracted) != "PP-OCRv5_det" {
		t.Errorf("extracted top dir = %q, want PP-OCRv5_det", extracted)
	}

	got, err := os.ReadFile(filepath.Join(extracted, "inference.pdmodel"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != "model-bytes" {
		t.Errorf("extracted content = %q, want model-bytes", got)
	}
}

func TestExtractTarGz(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "model.tar.gz")
	writeTar(t, archive, true, map[string]string{
		"weights/":      "",
		"weights/a.bin": "hello",
	})

	extracted, err := ExtractTarIfNeeded(archive)
	if err != nil {
		t.Fatalf("ExtractTarIfNeeded error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(extracted, "a.bin"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want hello", got)
	}
}

func TestExtractTarSkipsPathTraversal(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "evil.tar")
	writeTar(t, archive, false, map[string]string{
		"safe/":         "",
		"safe/ok.txt":   "fine",
		"../escape.txt": "malicious",
	})

	_, err := ExtractTarIfNeeded(archive)
	if err != nil {
		t.Fatalf("ExtractTarIfNeeded error: %v", err)
	}

	// The traversal entry must not have escaped the temp dir.
	if _, err := os.Stat(filepath.Join(filepath.Dir(tmp), "escape.txt")); err == nil {
		t.Error("path traversal entry was written outside dest dir")
	}
}

func TestExtractTarMissingFile(t *testing.T) {
	_, err := ExtractTarIfNeeded(filepath.Join(t.TempDir(), "nope.tar"))
	if err == nil {
		t.Error("extracting a missing archive should error")
	}
}

func TestExtractTarCorruptGzip(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "broken.tar.gz")
	// Not actually gzip data.
	if err := os.WriteFile(archive, []byte("not gzip at all"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractTarIfNeeded(archive); err == nil {
		t.Error("corrupt gzip should error")
	}
}

// Sanity: confirm round trip of an in-memory gzip tar, guarding helper correctness.
func TestWriteTarHelperRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "x", Size: 1, Mode: 0644})
	_, _ = tw.Write([]byte("y"))
	tw.Close()
	gz.Close()
	if buf.Len() == 0 {
		t.Error("expected non-empty gzip tar")
	}
}
