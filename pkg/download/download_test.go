package download

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDownloadNoURLs(t *testing.T) {
	err := Download(context.Background(), nil, "/tmp/x", 0, Config{}, nil)
	if err == nil {
		t.Error("expected error with no URLs")
	}
}

func TestDownloadSimple(t *testing.T) {
	payload := []byte("hello world contents")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	var lastProgress Progress
	err := Download(context.Background(), []string{srv.URL}, dest, int64(len(payload)), Config{}, func(p Progress) {
		lastProgress = p
	})
	if err != nil {
		t.Fatalf("Download error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("downloaded content = %q, want %q", got, payload)
	}
	if lastProgress.Pct != 100 {
		t.Errorf("final progress pct = %d, want 100", lastProgress.Pct)
	}

	// The .part and .part.url sidecar files must be cleaned up on success.
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Error(".part file should be removed after success")
	}
	if _, err := os.Stat(dest + ".part.url"); !os.IsNotExist(err) {
		t.Error(".part.url file should be removed after success")
	}
}

func TestDownloadMirrorFallback(t *testing.T) {
	payload := []byte("from second mirror")

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Write(payload)
	}))
	defer good.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	err := Download(context.Background(), []string{bad.URL, good.URL}, dest, int64(len(payload)), Config{}, nil)
	if err != nil {
		t.Fatalf("Download should succeed via fallback: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(payload) {
		t.Errorf("content = %q, want %q", got, payload)
	}
}

func TestDownloadAllMirrorsFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer bad.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	err := Download(context.Background(), []string{bad.URL, bad.URL}, dest, 10, Config{}, nil)
	if err == nil {
		t.Fatal("expected error when all mirrors fail")
	}
	if !strings.Contains(err.Error(), "all mirrors failed") {
		t.Errorf("error = %v, want 'all mirrors failed'", err)
	}
}

func TestDownloadSizeMismatch(t *testing.T) {
	payload := []byte("short")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	// Expect a much larger file → size validation fails.
	err := Download(context.Background(), []string{srv.URL}, dest, 100000, Config{}, nil)
	if err == nil {
		t.Fatal("expected size mismatch error")
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("final file should not exist on size mismatch")
	}
}

func TestDownloadResume(t *testing.T) {
	payload := []byte("0123456789ABCDEF")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			w.Write(payload)
			return
		}
		// Parse "bytes=N-"
		var start int
		fmt.Sscanf(rangeHdr, "bytes=%d-", &start)
		rest := payload[start:]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(rest)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(rest)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")

	// Pre-seed a partial .part file and the matching url sidecar.
	if err := os.WriteFile(dest+".part", payload[:6], 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".part.url", []byte(srv.URL), 0644); err != nil {
		t.Fatal(err)
	}

	err := Download(context.Background(), []string{srv.URL}, dest, int64(len(payload)), Config{}, nil)
	if err != nil {
		t.Fatalf("resume download error: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(payload) {
		t.Errorf("resumed content = %q, want %q", got, payload)
	}
}

func TestDownloadStalePartDifferentURL(t *testing.T) {
	payload := []byte("fresh-download-content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ignore Range — always serve the full body fresh.
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	// Stale .part from a *different* URL must be discarded.
	_ = os.WriteFile(dest+".part", []byte("STALE"), 0644)
	_ = os.WriteFile(dest+".part.url", []byte("http://old-mirror/file"), 0644)

	err := Download(context.Background(), []string{srv.URL}, dest, int64(len(payload)), Config{}, nil)
	if err != nil {
		t.Fatalf("download error: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(payload) {
		t.Errorf("content = %q, want %q (stale part should have been discarded)", got, payload)
	}
}

func TestDownloadContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(http.StatusOK)
		// Slowly dribble bytes so cancel takes effect mid-stream.
		buf := make([]byte, 1024)
		for i := 0; i < 1000; i++ {
			if _, err := w.Write(buf); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	dest := filepath.Join(t.TempDir(), "out.bin")
	err := Download(ctx, []string{srv.URL}, dest, 1000000, Config{}, nil)
	if err == nil {
		t.Error("expected cancellation error")
	}
}
