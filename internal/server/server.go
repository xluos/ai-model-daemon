package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/xushuaiwu/ai-model-daemon/internal/download"
	"github.com/xushuaiwu/ai-model-daemon/internal/fit"
	"github.com/xushuaiwu/ai-model-daemon/internal/hardware"
	"github.com/xushuaiwu/ai-model-daemon/internal/manifest"
	"github.com/xushuaiwu/ai-model-daemon/internal/storage"
)

type Server struct {
	listener net.Listener
	mux      *http.ServeMux
	mu       sync.Mutex
	active   map[string]bool
	config   download.Config
}

func New() *Server {
	s := &Server{
		mux:    http.NewServeMux(),
		active: make(map[string]bool),
	}
	s.mux.HandleFunc("GET /models", s.handleListModels)
	s.mux.HandleFunc("GET /models/{id}", s.handleGetModel)
	s.mux.HandleFunc("POST /models/{id}/download", s.handleDownload)
	s.mux.HandleFunc("GET /models/{id}/path", s.handleModelPath)
	s.mux.HandleFunc("DELETE /models/{id}", s.handleDeleteModel)
	s.mux.HandleFunc("GET /status", s.handleStatus)
	s.mux.HandleFunc("POST /config", s.handleConfig)
	s.mux.HandleFunc("GET /hardware", s.handleHardware)
	s.mux.HandleFunc("GET /models/recommended", s.handleRecommended)
	s.mux.HandleFunc("POST /models/{id}/recompute-fit", s.handleRecomputeFit)
	return s
}

func (s *Server) ListenAndServe(socketPath string) error {
	ln, err := listenIPC(socketPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = ln
	return http.Serve(ln, s.mux)
}

func Cleanup(socketPath string) {
	cleanupIPC(socketPath)
}

func (s *Server) Close() error {
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
	downloading := s.active[m.ID]
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
	if s.active[id] {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "download already in progress"})
		return
	}
	s.active[id] = true
	s.mu.Unlock()

	defer func() {
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
		err := download.Download(f.URLs, destPath, f.Bytes, s.config, func(p download.Progress) {
			p.ModelID = id
			p.FileRole = fileRole
			data, _ := json.Marshal(p)
			fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
			flusher.Flush()
		})

		if err != nil {
			data, _ := json.Marshal(map[string]interface{}{
				"modelId": id, "fileRole": fileRole, "ok": false, "error": err.Error(),
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

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pid":     os.Getpid(),
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
			Fit:                 a.Fit,
			MemPercent:          a.MemPercent,
			TPS:                 a.TPS,
			Ready:               allReady,
			Downloading:         s.active[a.ID],
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
