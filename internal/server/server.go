package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xluos/ai-model-daemon/internal/webui"
	"github.com/xluos/ai-model-daemon/pkg/clients"
	"github.com/xluos/ai-model-daemon/pkg/download"
	"github.com/xluos/ai-model-daemon/pkg/fit"
	"github.com/xluos/ai-model-daemon/pkg/hardware"
	"github.com/xluos/ai-model-daemon/pkg/manifest"
	"github.com/xluos/ai-model-daemon/pkg/proxy"
	"github.com/xluos/ai-model-daemon/pkg/queue"
	"github.com/xluos/ai-model-daemon/pkg/runtime"
	"github.com/xluos/ai-model-daemon/pkg/storage"
)

type Server struct {
	listener    net.Listener
	tcpListener net.Listener
	mux         *http.ServeMux
	mu          sync.Mutex
	active      map[string]context.CancelFunc
	config      download.Config
	token       string
	version     string

	rtm        *runtime.RuntimeManager
	scheduler  *queue.Scheduler
	proxy      *proxy.Proxy
	tracker    *clients.Tracker
	onShutdown func()
}

func New(token string, version string, onShutdown func()) *Server {
	rtm := runtime.NewRuntimeManager()
	sched := queue.NewScheduler(rtm, queue.DefaultConfig())
	p := proxy.New(sched, rtm)

	s := &Server{
		mux:        http.NewServeMux(),
		active:     make(map[string]context.CancelFunc),
		token:      token,
		version:    version,
		rtm:        rtm,
		scheduler:  sched,
		proxy:      p,
		tracker:    clients.NewTracker(30*time.Second, onShutdown),
		onShutdown: onShutdown,
	}

	// Existing model management routes
	s.mux.HandleFunc("GET /models", s.handleListModels)
	s.mux.HandleFunc("GET /models/{id}", s.handleGetModel)
	s.mux.HandleFunc("POST /models/{id}/download", s.handleDownload)
	s.mux.HandleFunc("POST /models/{id}/cancel-download", s.handleCancelDownload)
	s.mux.HandleFunc("GET /models/{id}/path", s.handleModelPath)
	s.mux.HandleFunc("DELETE /models/{id}", s.handleDeleteModel)
	s.mux.HandleFunc("GET /version", s.handleVersion)
	s.mux.HandleFunc("GET /status", s.handleStatus)
	s.mux.HandleFunc("POST /config", s.handleConfig)
	s.mux.HandleFunc("GET /hardware", s.handleHardware)
	s.mux.HandleFunc("GET /models/recommended", s.handleRecommended)
	s.mux.HandleFunc("POST /models/{id}/recompute-fit", s.handleRecomputeFit)

	// OpenAI-compatible proxy routes
	s.mux.HandleFunc("GET /v1/models", p.HandleModels)
	s.mux.HandleFunc("POST /v1/chat/completions", p.HandleChatCompletions)
	s.mux.HandleFunc("POST /v1/completions", p.HandleCompletions)
	s.mux.HandleFunc("POST /v1/audio/transcriptions", p.HandleAudioTranscriptions)
	s.mux.HandleFunc("POST /v1/audio/transcriptions/faster", p.HandleFasterWhisperTranscriptions)
	s.mux.HandleFunc("POST /v1/ocr", p.HandleOCR)

	// Runtime management routes
	s.mux.HandleFunc("GET /api/runtime/status", s.handleRuntimeStatus)
	s.mux.HandleFunc("POST /api/runtime/llm/start", s.handleRuntimeLLMStart)
	s.mux.HandleFunc("POST /api/runtime/llm/stop", s.handleRuntimeLLMStop)
	s.mux.HandleFunc("POST /api/runtime/whisper/start", s.handleRuntimeWhisperStart)
	s.mux.HandleFunc("POST /api/runtime/whisper/stop", s.handleRuntimeWhisperStop)
	s.mux.HandleFunc("GET /api/runtime/llm/logs", s.handleRuntimeLLMLogs)
	s.mux.HandleFunc("GET /api/runtime/whisper/logs", s.handleRuntimeWhisperLogs)

	// Generic runtime routes (work for any registered runtime kind)
	s.mux.HandleFunc("POST /api/runtime/{kind}/start", s.handleGenericRuntimeStart)
	s.mux.HandleFunc("POST /api/runtime/{kind}/stop", s.handleGenericRuntimeStop)
	s.mux.HandleFunc("GET /api/runtime/{kind}/logs", s.handleGenericRuntimeLogs)

	// Queue management routes
	s.mux.HandleFunc("GET /api/queue/status", s.handleQueueStatus)
	s.mux.HandleFunc("POST /api/queue/config", s.handleQueueConfig)

	// Binary management routes
	s.mux.HandleFunc("GET /api/binaries/status", s.handleBinariesStatus)
	s.mux.HandleFunc("POST /api/binaries/llama-server/download", s.handleBinaryDownload(runtime.BinaryLlamaServer))
	s.mux.HandleFunc("POST /api/binaries/whisper-server/download", s.handleBinaryDownload(runtime.BinaryWhisperServer))

	// Client lifecycle routes
	s.mux.HandleFunc("POST /api/clients/register", s.handleClientRegister)
	s.mux.HandleFunc("POST /api/clients/deregister", s.handleClientDeregister)
	s.mux.HandleFunc("POST /api/clients/heartbeat", s.handleClientHeartbeat)
	s.mux.HandleFunc("GET /api/clients", s.handleClientList)

	// Web UI (served on GET / with auto token redirect)
	s.mux.HandleFunc("GET /ui", s.handleUI)
	s.mux.HandleFunc("GET /", s.handleIndex)

	return s
}

func (s *Server) ListenAndServe(socketPath string) error {
	ln, err := listenIPC(socketPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = ln
	return http.Serve(ln, s.withAuth(s.mux))
}

func (s *Server) ListenAndServeTCP(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen tcp: %w", err)
	}
	s.tcpListener = ln
	return http.Serve(ln, s.withCORS(s.withAuth(s.mux)))
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for UI pages (token is passed via URL param, then used in JS headers)
		if r.URL.Path == "/" || r.URL.Path == "/ui" {
			next.ServeHTTP(w, r)
			return
		}
		if s.token != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+s.token {
				// Also accept token as query parameter for browser convenience
				if r.URL.Query().Get("token") != s.token {
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func Cleanup(socketPath string) {
	cleanupIPC(socketPath)
}

func (s *Server) Tracker() *clients.Tracker {
	return s.tracker
}

func (s *Server) Close() error {
	if s.tracker != nil {
		s.tracker.Close()
	}
	if s.scheduler != nil {
		s.scheduler.Close()
	}
	if s.rtm != nil {
		s.rtm.StopAll()
	}
	if s.tcpListener != nil {
		s.tcpListener.Close()
	}
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

type fileStatus struct {
	Role     string   `json:"role"`
	Filename string   `json:"filename"`
	Bytes    int64    `json:"bytes"`
	URLs     []string `json:"urls"`
	Ready    bool     `json:"ready"`
	Path     string   `json:"path"`
}

type modelStatus struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Desc        string       `json:"desc"`
	Files       []fileStatus `json:"files"`
	TotalBytes  int64        `json:"totalBytes"`
	Enables     []string     `json:"enables"`
	Apps        []string     `json:"apps"`
	Bundled     bool         `json:"bundled"`
	Ready       bool         `json:"ready"`
	Downloading bool         `json:"downloading"`
}

func (s *Server) getModelStatus(m *manifest.Model) modelStatus {
	s.mu.Lock()
	_, downloading := s.active[m.ID]
	s.mu.Unlock()

	allReady := true
	var files []fileStatus
	for _, f := range m.Files {
		ready := m.Bundled || storage.IsFileReady(m.ID, f.Filename, f.Bytes)
		p := ""
		if ready {
			p = storage.ModelFilePath(m.ID, f.Filename)
		}
		if !ready {
			allReady = false
		}
		files = append(files, fileStatus{
			Role:     f.Role,
			Filename: f.Filename,
			Bytes:    f.Bytes,
			URLs:     f.URLs,
			Ready:    ready,
			Path:     p,
		})
	}

	return modelStatus{
		ID:          m.ID,
		Name:        m.Name,
		Desc:        m.Desc,
		Files:       files,
		TotalBytes:  m.TotalBytes(),
		Enables:     m.Enables,
		Apps:        m.Apps,
		Bundled:     m.Bundled,
		Ready:       allReady,
		Downloading: downloading,
	}
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	appFilter := r.URL.Query().Get("app")
	var results []modelStatus
	for i := range manifest.Registry {
		m := &manifest.Registry[i]
		if appFilter != "" {
			found := false
			for _, a := range m.Apps {
				if a == appFilter {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		results = append(results, s.getModelStatus(m))
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m := manifest.Find(id)
	if m == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	}
	writeJSON(w, http.StatusOK, s.getModelStatus(m))
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m := manifest.Find(id)
	if m == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	}
	if m.Bundled {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is bundled, no download needed"})
		return
	}

	allReady := true
	for _, f := range m.Files {
		if !storage.IsFileReady(id, f.Filename, f.Bytes) {
			allReady = false
			break
		}
	}
	if allReady {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already_ready"})
		return
	}

	s.mu.Lock()
	if _, exists := s.active[id]; exists {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "download already in progress"})
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	s.active[id] = cancel
	s.mu.Unlock()

	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.active, id)
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	if err := storage.EnsureModelDir(id); err != nil {
		data, _ := json.Marshal(map[string]interface{}{"modelId": id, "ok": false, "error": err.Error()})
		fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
		flusher.Flush()
		return
	}

	for _, f := range m.Files {
		if storage.IsFileReady(id, f.Filename, f.Bytes) {
			data, _ := json.Marshal(map[string]interface{}{
				"modelId": id, "fileRole": f.Role, "status": "skip",
			})
			fmt.Fprintf(w, "event: skip\ndata: %s\n\n", data)
			flusher.Flush()
			continue
		}

		destPath := storage.ModelFilePath(id, f.Filename)
		fileRole := f.Role
		dlCfg := s.config
		if raw := r.URL.Query().Get("progressInterval"); raw != "" {
			if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
				dlCfg.ProgressInterval = time.Duration(ms) * time.Millisecond
			}
		}
		err := download.Download(ctx, f.URLs, destPath, f.Bytes, dlCfg, func(p download.Progress) {
			p.ModelID = id
			p.FileRole = fileRole
			data, _ := json.Marshal(p)
			fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
			flusher.Flush()
		})

		if err != nil {
			cancelled := ctx.Err() != nil
			data, _ := json.Marshal(map[string]interface{}{
				"modelId": id, "fileRole": fileRole, "ok": false,
				"error": err.Error(), "cancelled": cancelled,
			})
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
			flusher.Flush()
			return
		}

		// Extract tar archives (e.g. PaddleOCR model packages)
		if _, extractErr := download.ExtractTarIfNeeded(destPath); extractErr != nil {
			data, _ := json.Marshal(map[string]interface{}{
				"modelId": id, "fileRole": fileRole, "ok": false,
				"error": extractErr.Error(),
			})
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
			flusher.Flush()
			return
		}
	}

	data, _ := json.Marshal(map[string]interface{}{"modelId": id, "ok": true})
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
	flusher.Flush()
}

func (s *Server) handleCancelDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	cancel, exists := s.active[id]
	s.mu.Unlock()
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no active download for this model"})
		return
	}
	cancel()
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (s *Server) handleModelPath(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m := manifest.Find(id)
	if m == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	}

	paths := make(map[string]string)
	for _, f := range m.Files {
		if !m.Bundled && !storage.IsFileReady(id, f.Filename, f.Bytes) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not ready"})
			return
		}
		role := f.Role
		if role == "" {
			role = "default"
		}
		paths[role] = storage.ModelFilePath(id, f.Filename)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"paths": paths})
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m := manifest.Find(id)
	if m == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	}
	for _, f := range m.Files {
		p := storage.ModelFilePath(id, f.Filename)
		os.Remove(p)
		os.Remove(p + ".part")
		os.Remove(p + ".part.url")
	}
	os.Remove(storage.ModelDir(id))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.version})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pid":     os.Getpid(),
		"version": s.version,
		"storage": storage.Dir(),
		"socket":  storage.SocketPath(),
		"ready":   true,
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	var cfg struct {
		PreferMirror string `json:"preferMirror"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	cfg.PreferMirror = strings.TrimSpace(cfg.PreferMirror)
	s.mu.Lock()
	s.config.PreferMirror = cfg.PreferMirror
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleHardware returns the current machine's hardware info.
func (s *Server) handleHardware(w http.ResponseWriter, r *http.Request) {
	info := hardware.Detect()
	writeJSON(w, http.StatusOK, info)
}

type recommendedModel struct {
	ID                  string                `json:"id"`
	Name                string                `json:"name"`
	Desc                string                `json:"desc"`
	Family              string                `json:"family,omitempty"`
	Params              string                `json:"params,omitempty"`
	PrimaryCapabilities []string              `json:"primaryCapabilities,omitempty"`
	SecondaryTags       []string              `json:"secondaryTags,omitempty"`
	ContextSize         int                   `json:"contextSize,omitempty"`
	NativeContextSize   int                   `json:"nativeContextSize,omitempty"`
	Quantizations       []manifest.Quantization `json:"quantizations,omitempty"`
	IsThinking          bool                  `json:"isThinking,omitempty"`
	Available           *bool                 `json:"available,omitempty"`
	Fit                 fit.FitLevel          `json:"fit"`
	MemPercent          int                   `json:"memPercent"`
	TPS                 int                   `json:"tps"`
	Ready               bool                  `json:"ready"`
	Downloading         bool                  `json:"downloading"`
}

// handleRecommended returns models annotated with fit info, sorted by fit level.
func (s *Server) handleRecommended(w http.ResponseWriter, r *http.Request) {
	appFilter := r.URL.Query().Get("app")
	ctxOverrides := parseCtxOverrides(r)
	machine := hardware.Detect()

	var filtered []manifest.Model
	for _, m := range manifest.Registry {
		if appFilter != "" {
			found := false
			for _, a := range m.Apps {
				if a == appFilter {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if len(m.Quantizations) == 0 && m.Params == "" {
			continue
		}
		filtered = append(filtered, m)
	}

	annotated := fit.AnnotateModels(filtered, machine, ctxOverrides)
	fit.SortByFit(annotated)

	s.mu.Lock()
	defer s.mu.Unlock()

	results := make([]recommendedModel, 0, len(annotated))
	for _, a := range annotated {
		allReady := true
		for _, f := range a.Files {
			if !a.Bundled && !storage.IsFileReady(a.ID, f.Filename, f.Bytes) {
				allReady = false
				break
			}
		}

		results = append(results, recommendedModel{
			ID:                  a.ID,
			Name:                a.Name,
			Desc:                a.Desc,
			Family:              a.Family,
			Params:              a.Params,
			PrimaryCapabilities: a.PrimaryCapabilities,
			SecondaryTags:       a.SecondaryTags,
			ContextSize:         a.ContextSize,
			NativeContextSize:   a.NativeContextSize,
			Quantizations:       a.Quantizations,
			IsThinking:          a.IsThinking,
			Available:           a.Available,
			Fit:                 a.Fit,
			MemPercent:          a.MemPercent,
			TPS:                 a.TPS,
			Ready:               allReady,
			Downloading:         func() bool { _, ok := s.active[a.ID]; return ok }(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"machine": machine,
		"models":  results,
	})
}

// handleRecomputeFit recomputes fit for a single model with a custom context size.
func (s *Server) handleRecomputeFit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m := manifest.Find(id)
	if m == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	}

	var body struct {
		ContextSize int `json:"contextSize"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	paramsB := fit.ParseParams(m.Params)
	if len(m.Quantizations) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model has no quantization info"})
		return
	}

	machine := hardware.Detect()
	q := m.Quantizations[0]
	result := fit.ComputeQuantFit(q.SizeBytes, q.Label, machine, paramsB, body.ContextSize, m)
	writeJSON(w, http.StatusOK, result)
}

func parseCtxOverrides(r *http.Request) map[string]int {
	overrides := make(map[string]int)
	for key, values := range r.URL.Query() {
		if !strings.HasPrefix(key, "ctx.") || len(values) == 0 {
			continue
		}
		modelID := strings.TrimPrefix(key, "ctx.")
		if v, err := strconv.Atoi(values[0]); err == nil && v > 0 {
			overrides[modelID] = v
		}
	}
	return overrides
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// --- Runtime management handlers ---

func (s *Server) handleRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.rtm.Status())
}

func (s *Server) handleRuntimeLLMStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ModelID     string `json:"modelId"`
		ContextSize int    `json:"contextSize,omitempty"`
		GPULayers   int    `json:"gpuLayers,omitempty"`
		Parallel    int    `json:"parallel,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.ModelID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "modelId is required"})
		return
	}

	opts := runtime.LLMOpts{
		ContextSize: body.ContextSize,
		GPULayers:   body.GPULayers,
		Parallel:    body.Parallel,
	}
	if err := s.rtm.EnsureLLM(body.ModelID, opts); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.rtm.LLM().StatusTyped())
}

func (s *Server) handleRuntimeLLMStop(w http.ResponseWriter, r *http.Request) {
	if err := s.rtm.LLM().Stop(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handleRuntimeWhisperStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ModelID string `json:"modelId"`
		Threads int    `json:"threads,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.ModelID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "modelId is required"})
		return
	}

	opts := runtime.WhisperOpts{Threads: body.Threads}
	if err := s.rtm.EnsureWhisper(body.ModelID, opts); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.rtm.Whisper().StatusTyped())
}

func (s *Server) handleRuntimeWhisperStop(w http.ResponseWriter, r *http.Request) {
	if err := s.rtm.Whisper().Stop(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handleRuntimeLLMLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"logs": s.rtm.LLM().Logs()})
}

func (s *Server) handleRuntimeWhisperLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"logs": s.rtm.Whisper().Logs()})
}

// --- Queue management handlers ---

func (s *Server) handleQueueStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.scheduler.Status())
}

func (s *Server) handleQueueConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MaxQueueSize         *int    `json:"maxQueueSize,omitempty"`
		MaxWaitTimeSec       *int    `json:"maxWaitTimeSec,omitempty"`
		IdleTimeoutSec       *int    `json:"idleTimeoutSec,omitempty"`
		MaxBatchBeforeSwitch *int    `json:"maxBatchBeforeSwitch,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	cfg := s.scheduler.Config()
	if body.MaxQueueSize != nil {
		cfg.MaxQueueSize = *body.MaxQueueSize
	}
	if body.MaxWaitTimeSec != nil {
		cfg.MaxWaitTime = time.Duration(*body.MaxWaitTimeSec) * time.Second
	}
	if body.IdleTimeoutSec != nil {
		cfg.IdleTimeout = time.Duration(*body.IdleTimeoutSec) * time.Second
	}
	if body.MaxBatchBeforeSwitch != nil {
		cfg.MaxBatchBeforeSwitch = *body.MaxBatchBeforeSwitch
	}
	s.scheduler.UpdateConfig(cfg)
	writeJSON(w, http.StatusOK, cfg)
}

// --- Binary management handlers ---

func (s *Server) handleBinariesStatus(w http.ResponseWriter, r *http.Request) {
	bm := s.rtm.BinaryManager()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"llamaServer":   bm.Status(runtime.BinaryLlamaServer),
		"whisperServer": bm.Status(runtime.BinaryWhisperServer),
	})
}

func (s *Server) handleBinaryDownload(kind runtime.BinaryKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bm := s.rtm.BinaryManager()

		info := bm.Status(kind)
		if info.Available {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"status": "already_available",
				"path":   info.Path,
			})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
			return
		}

		err := bm.Download(r.Context(), kind, func(p download.Progress) {
			data, _ := json.Marshal(p)
			fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
			flusher.Flush()
		})

		if err != nil {
			data, _ := json.Marshal(map[string]interface{}{"ok": false, "error": err.Error()})
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
			flusher.Flush()
			return
		}

		updated := bm.Status(kind)
		data, _ := json.Marshal(map[string]interface{}{"ok": true, "path": updated.Path})
		fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
		flusher.Flush()
	}
}

// --- Web UI handlers ---

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(webui.IndexHTML)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/ui?token="+s.token, http.StatusFound)
}

// --- Client lifecycle handlers ---

func (s *Server) handleClientRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name,omitempty"`
		PID  int    `json:"pid,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	s.tracker.Register(body.ID, body.Name, body.PID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "registered",
		"clients": s.tracker.Count(),
	})
}

func (s *Server) handleClientDeregister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	remaining := s.tracker.Deregister(body.ID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "deregistered",
		"remaining": remaining,
	})
}

func (s *Server) handleClientHeartbeat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if !s.tracker.Heartbeat(body.ID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "client not registered"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleClientList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"clients": s.tracker.List(),
		"count":   s.tracker.Count(),
	})
}

// --- Generic runtime handlers ---

func (s *Server) handleGenericRuntimeStart(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	rt := s.rtm.Get(kind)
	if rt == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown runtime kind: " + kind})
		return
	}

	var body struct {
		ModelID string `json:"modelId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.ModelID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "modelId is required"})
		return
	}

	if err := rt.Ensure(body.ModelID, nil); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rt.Status())
}

func (s *Server) handleGenericRuntimeStop(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	rt := s.rtm.Get(kind)
	if rt == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown runtime kind: " + kind})
		return
	}
	if err := rt.Stop(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handleGenericRuntimeLogs(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	rt := s.rtm.Get(kind)
	if rt == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown runtime kind: " + kind})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"logs": rt.Logs()})
}
