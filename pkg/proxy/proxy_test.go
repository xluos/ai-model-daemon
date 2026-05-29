package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xluos/ai-model-daemon/pkg/manifest"
	"github.com/xluos/ai-model-daemon/pkg/queue"
	"github.com/xluos/ai-model-daemon/pkg/runtime"
)

func newTestProxy(t *testing.T) *Proxy {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	rtm := runtime.NewRuntimeManager()
	sched := queue.NewScheduler(rtm, queue.DefaultConfig())
	t.Cleanup(sched.Close)
	return New(sched, rtm)
}

func TestResolveModelID(t *testing.T) {
	// Exact ID match.
	if got := resolveModelID("qwen3-4b-q4"); got != "qwen3-4b-q4" {
		t.Errorf("resolveModelID(exact) = %q", got)
	}
	// Case-insensitive match by ID.
	if got := resolveModelID("QWEN3-4B-Q4"); got != "qwen3-4b-q4" {
		t.Errorf("resolveModelID(upper) = %q, want qwen3-4b-q4", got)
	}
	// Match by display name.
	if got := resolveModelID("Qwen3-4B Q4_K_M"); got != "qwen3-4b-q4" {
		t.Errorf("resolveModelID(by name) = %q, want qwen3-4b-q4", got)
	}
	// Unknown returns the input unchanged.
	if got := resolveModelID("totally-unknown"); got != "totally-unknown" {
		t.Errorf("resolveModelID(unknown) = %q", got)
	}
}

func TestIsModelReadyBundled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// rapidocr models are bundled → always ready regardless of files on disk.
	m := manifest.Find("rapidocr-ppocr-v5-mobile")
	if m == nil {
		t.Fatal("bundled model not found in manifest")
	}
	if !isModelReady(m) {
		t.Error("bundled model should always report ready")
	}
}

func TestIsModelReadyMissingFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	// A non-bundled model with no downloaded files → not ready.
	m := manifest.Find("qwen3-4b-q4")
	if m == nil {
		t.Fatal("model not found")
	}
	if isModelReady(m) {
		t.Error("model with missing files should not be ready")
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusNotFound, "boom")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if body.Error.Message != "boom" {
		t.Errorf("error message = %q, want boom", body.Error.Message)
	}
	if body.Error.Type != "invalid_request_error" {
		t.Errorf("error type = %q", body.Error.Type)
	}
}

func TestNativeFormats(t *testing.T) {
	for _, ext := range []string{".wav", ".mp3", ".flac", ".wave"} {
		if !nativeFormats[ext] {
			t.Errorf("%s should be a native format", ext)
		}
	}
	if nativeFormats[".m4a"] {
		t.Error(".m4a should not be native (needs transcode)")
	}
}

func TestHandleChatCompletionsBadJSON(t *testing.T) {
	p := newTestProxy(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	p.HandleChatCompletions(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleChatCompletionsMissingModel(t *testing.T) {
	p := newTestProxy(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	rec := httptest.NewRecorder()
	p.HandleChatCompletions(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (model required)", rec.Code)
	}
}

func TestHandleChatCompletionsUnknownModel(t *testing.T) {
	p := newTestProxy(t)
	body := `{"model":"no-such-model","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	p.HandleChatCompletions(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleChatCompletionsNotDownloaded(t *testing.T) {
	p := newTestProxy(t)
	// Known model, but not downloaded in the temp HOME.
	body := `{"model":"qwen3-4b-q4","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	p.HandleChatCompletions(rec, req)
	if rec.Code != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412 (not downloaded)", rec.Code)
	}
}

func TestHandleChatCompletionsWrongRuntimeKind(t *testing.T) {
	p := newTestProxy(t)
	// rapidocr model is bundled (ready) but not an LLM → 400.
	body := `{"model":"rapidocr-ppocr-v5-mobile","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	p.HandleChatCompletions(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (not an LLM model)", rec.Code)
	}
}

func TestHandleOCRUnknownModel(t *testing.T) {
	p := newTestProxy(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/ocr?model=no-such-ocr", bytes.NewReader([]byte("imgdata")))
	rec := httptest.NewRecorder()
	p.HandleOCR(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleModelsReturnsList(t *testing.T) {
	p := newTestProxy(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	p.HandleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Object string                   `json:"object"`
		Data   []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("object = %q, want list", resp.Object)
	}
	// Bundled rapidocr models are always ready → should appear even with no downloads.
	found := false
	for _, m := range resp.Data {
		if m["id"] == "rapidocr-ppocr-v5-mobile" {
			found = true
		}
	}
	if !found {
		t.Error("bundled rapidocr model should appear in /v1/models")
	}
}

// TestProxyOCRWrapsItems verifies the OCR response wrapping: a JSON backend
// response is wrapped in {items, elapsed_ms} and OCR query params are forwarded.
func TestProxyOCRWrapsItems(t *testing.T) {
	var gotQuery url.Values
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"text":"hello","confidence":0.9}]`))
	}))
	defer backend.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/ocr", bytes.NewReader([]byte("imgbytes")))

	params := url.Values{}
	params.Set("box_thresh", "0.5")
	params.Set("rec_thresh", "0.3")

	proxyOCR(rec, req, backend.URL, []byte("imgbytes"), params)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var wrapped struct {
		Items     json.RawMessage `json:"items"`
		ElapsedMS int64           `json:"elapsed_ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wrapped); err != nil {
		t.Fatalf("response not wrapped JSON: %v", err)
	}
	if !strings.Contains(string(wrapped.Items), "hello") {
		t.Errorf("items not preserved: %s", wrapped.Items)
	}
	if wrapped.ElapsedMS < 0 {
		t.Errorf("elapsed_ms negative: %d", wrapped.ElapsedMS)
	}
	if gotQuery.Get("box_thresh") != "0.5" || gotQuery.Get("rec_thresh") != "0.3" {
		t.Errorf("OCR params not forwarded to backend: %v", gotQuery)
	}
}

func TestProxyOCRPassesThroughNonJSON(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("backend exploded"))
	}))
	defer backend.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/ocr", bytes.NewReader(nil))
	proxyOCR(rec, req, backend.URL, nil, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 passthrough", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "backend exploded") {
		t.Errorf("non-JSON body should pass through unchanged: %q", rec.Body.String())
	}
}
