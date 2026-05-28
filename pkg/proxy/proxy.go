package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xluos/ai-model-daemon/pkg/manifest"
	"github.com/xluos/ai-model-daemon/pkg/queue"
	"github.com/xluos/ai-model-daemon/pkg/runtime"
	"github.com/xluos/ai-model-daemon/pkg/storage"
)

type Proxy struct {
	scheduler *queue.Scheduler
	rtm       *runtime.RuntimeManager
}

func New(scheduler *queue.Scheduler, rtm *runtime.RuntimeManager) *Proxy {
	return &Proxy{
		scheduler: scheduler,
		rtm:       rtm,
	}
}

// HandleChatCompletions handles POST /v1/chat/completions
func (p *Proxy) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model field is required")
		return
	}

	modelID := resolveModelID(req.Model)
	m := manifest.Find(modelID)
	if m == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("model %q not found", req.Model))
		return
	}

	if !isModelReady(m) {
		writeError(w, http.StatusPreconditionFailed, fmt.Sprintf("model %q is not downloaded", modelID))
		return
	}

	kind, err := runtime.RuntimeKindFor(m.RuntimeKind)
	if err != nil || kind != "llm" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("model %q is not an LLM model", modelID))
		return
	}

	clientID := r.Header.Get("X-Client-ID")
	priority := queue.PriorityNormal
	if r.Header.Get("X-Priority") == "high" {
		priority = queue.PriorityHigh
	}

	if err := p.scheduler.HTTPHandler(kind, modelID, clientID, priority, w, r, func() {
		target := p.scheduler.ProxyTarget("llm")
		if target == "" {
			writeError(w, http.StatusServiceUnavailable, "LLM runtime not ready")
			return
		}
		reverseProxy(w, r, target, body)
	}); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
	}
}

// HandleCompletions handles POST /v1/completions (text completion)
func (p *Proxy) HandleCompletions(w http.ResponseWriter, r *http.Request) {
	p.HandleChatCompletions(w, r)
}

// HandleModels handles GET /v1/models — returns downloaded LLM/VLM models in OpenAI format.
func (p *Proxy) HandleModels(w http.ResponseWriter, r *http.Request) {
	var models []map[string]interface{}
	for _, m := range manifest.Registry {
		kind, _ := runtime.RuntimeKindFor(m.RuntimeKind)
		if kind != "llm" && kind != "whisper" {
			continue
		}
		hasRunnable := len(m.Quantizations) > 0 || m.RuntimeKind == "whisper"
		if !hasRunnable {
			continue
		}
		if !isModelReady(&m) {
			continue
		}
		models = append(models, map[string]interface{}{
			"id":       m.ID,
			"object":   "model",
			"created":  time.Now().Unix(),
			"owned_by": "local",
			"meta": map[string]interface{}{
				"name":   m.Name,
				"desc":   m.Desc,
				"family": m.Family,
				"params": m.Params,
				"kind":   kind,
				"ready":  true,
			},
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   models,
	})
}

// HandleAudioTranscriptions handles POST /v1/audio/transcriptions
func (p *Proxy) HandleAudioTranscriptions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	r.Body = io.NopCloser(bytes.NewReader(body))
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	modelName := r.FormValue("model")
	if modelName == "" {
		modelName = "whisper-large-v3-turbo"
	}

	modelID := resolveModelID(modelName)
	m := manifest.Find(modelID)
	if m == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("model %q not found", modelName))
		return
	}

	if !isModelReady(m) {
		writeError(w, http.StatusPreconditionFailed, fmt.Sprintf("model %q is not downloaded", modelID))
		return
	}

	forwardBody, forwardContentType, cleanup := transcodeIfNeeded(r, body)
	if cleanup != nil {
		defer cleanup()
	}

	clientID := r.Header.Get("X-Client-ID")

	if err := p.scheduler.HTTPHandler("whisper", modelID, clientID, queue.PriorityNormal, w, r, func() {
		target := p.scheduler.ProxyTarget("whisper")
		if target == "" {
			writeError(w, http.StatusServiceUnavailable, "Whisper runtime not ready")
			return
		}
		forwardMultipart(w, r, target, forwardBody, forwardContentType)
	}); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
	}
}

var nativeFormats = map[string]bool{
	".wav": true, ".mp3": true, ".flac": true, ".wave": true,
}

func transcodeIfNeeded(r *http.Request, originalBody []byte) (body []byte, contentType string, cleanup func()) {
	fileHeaders := r.MultipartForm.File["file"]
	if len(fileHeaders) == 0 {
		return originalBody, r.Header.Get("Content-Type"), nil
	}
	header := fileHeaders[0]
	file, err := header.Open()
	if err != nil {
		return originalBody, r.Header.Get("Content-Type"), nil
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if nativeFormats[ext] {
		return originalBody, r.Header.Get("Content-Type"), nil
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return originalBody, r.Header.Get("Content-Type"), nil
	}

	tmpIn, _ := os.CreateTemp("", "whisper-in-*"+ext)
	tmpOut, _ := os.CreateTemp("", "whisper-out-*.wav")
	tmpIn.Close()
	tmpOut.Close()

	inPath := tmpIn.Name()
	outPath := tmpOut.Name()

	data, _ := io.ReadAll(file)
	os.WriteFile(inPath, data, 0644)

	cmd := exec.Command("ffmpeg", "-i", inPath, "-ar", "16000", "-ac", "1", "-f", "wav", outPath, "-y")
	if err := cmd.Run(); err != nil {
		os.Remove(inPath)
		os.Remove(outPath)
		return originalBody, r.Header.Get("Content-Type"), nil
	}

	wavData, err := os.ReadFile(outPath)
	if err != nil {
		os.Remove(inPath)
		os.Remove(outPath)
		return originalBody, r.Header.Get("Content-Type"), nil
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	part, _ := w.CreateFormFile("file", strings.TrimSuffix(header.Filename, ext)+".wav")
	part.Write(wavData)

	for key, vals := range r.MultipartForm.Value {
		if key == "file" {
			continue
		}
		for _, v := range vals {
			w.WriteField(key, v)
		}
	}
	w.Close()

	return buf.Bytes(), w.FormDataContentType(), func() {
		os.Remove(inPath)
		os.Remove(outPath)
	}
}

func reverseProxy(w http.ResponseWriter, r *http.Request, targetBase string, body []byte) {
	targetURL, err := url.Parse(targetBase)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid proxy target")
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.URL.Path = r.URL.Path
			req.URL.RawQuery = r.URL.RawQuery
			req.Host = targetURL.Host
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
		},
		FlushInterval: -1,
	}

	proxy.ServeHTTP(w, r)
}

func forwardMultipart(w http.ResponseWriter, r *http.Request, targetBase string, body []byte, contentType string) {
	targetURL, err := url.Parse(targetBase)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid proxy target")
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.URL.Path = "/inference"
			req.Host = targetURL.Host
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
		},
		FlushInterval: -1,
	}

	proxy.ServeHTTP(w, r)
}

func isModelReady(m *manifest.Model) bool {
	if m.Bundled {
		return true
	}
	for _, f := range m.Files {
		if !storage.IsFileReady(m.ID, f.Filename, f.Bytes) {
			return false
		}
	}
	return true
}

func resolveModelID(name string) string {
	if m := manifest.Find(name); m != nil {
		return name
	}
	lower := strings.ToLower(name)
	for _, m := range manifest.Registry {
		if strings.ToLower(m.ID) == lower || strings.ToLower(m.Name) == lower {
			return m.ID
		}
	}
	return name
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": msg,
			"type":    "invalid_request_error",
		},
	})
}
