package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/xluos/ai-model-daemon/pkg/download"
	"github.com/xluos/ai-model-daemon/pkg/fit"
	"github.com/xluos/ai-model-daemon/pkg/hardware"
	"github.com/xluos/ai-model-daemon/pkg/manifest"
	"github.com/xluos/ai-model-daemon/internal/server"
	"github.com/xluos/ai-model-daemon/pkg/storage"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		cmdServe()
	case "status":
		cmdStatus()
	case "list":
		cmdList()
	case "download":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: ai-model-daemon download <model-id>")
			os.Exit(1)
		}
		cmdDownload(os.Args[2])
	case "path":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: ai-model-daemon path <model-id>")
			os.Exit(1)
		}
		cmdPath(os.Args[2])
	case "hardware":
		cmdHardware()
	case "recommend":
		cmdRecommend()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: ai-model-daemon <command>

Commands:
  serve              Start the daemon (HTTP API over Unix socket)
  status             Print daemon status
  list               List all models (JSON)
  download <id>      Download a model (blocks until complete)
  path <id>          Print model file paths (JSON)
  hardware           Detect and print hardware info (JSON)
  recommend          List models sorted by fit for this machine (JSON)`)
}

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", os.Getpid())
	}
	return hex.EncodeToString(b)
}

func cmdServe() {
	if err := storage.EnsureDir(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	socketPath := storage.SocketPath()
	pidPath := storage.PIDPath()
	tokenPath := storage.TokenPath()

	pid := os.Getpid()
	token := generateToken()

	os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644)
	os.WriteFile(tokenPath, []byte(token), 0600)

	srv := server.New(token)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		srv.Close()
		os.Remove(socketPath)
		os.Remove(pidPath)
		os.Remove(tokenPath)
		os.Exit(0)
	}()

	ready := map[string]interface{}{
		"socket": socketPath,
		"pid":    pid,
		"token":  token,
	}
	readyJSON, _ := json.Marshal(ready)
	fmt.Println(string(readyJSON))

	if err := srv.ListenAndServe(socketPath); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Remove(pidPath)
		os.Remove(tokenPath)
		os.Exit(1)
	}
}

func cmdStatus() {
	pidPath := storage.PIDPath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		fmt.Println(`{"running":false,"storage":"` + storage.Dir() + `"}`)
		return
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		fmt.Println(`{"running":false,"storage":"` + storage.Dir() + `"}`)
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Println(`{"running":false,"storage":"` + storage.Dir() + `"}`)
		return
	}
	err = proc.Signal(syscall.Signal(0))
	running := err == nil

	result := map[string]interface{}{
		"running": running,
		"pid":     pid,
		"socket":  storage.SocketPath(),
		"storage": storage.Dir(),
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(out))
}

func cmdList() {
	type fileEntry struct {
		Role     string `json:"role,omitempty"`
		Filename string `json:"filename"`
		Bytes    int64  `json:"bytes"`
		Ready    bool   `json:"ready"`
	}
	type entry struct {
		ID      string      `json:"id"`
		Name    string      `json:"name"`
		Files   []fileEntry `json:"files"`
		Ready   bool        `json:"ready"`
		Bundled bool        `json:"bundled"`
		Apps    []string    `json:"apps"`
		Enables []string    `json:"enables"`
	}
	var entries []entry
	for _, m := range manifest.Registry {
		allReady := true
		var files []fileEntry
		for _, f := range m.Files {
			ready := m.Bundled || storage.IsFileReady(m.ID, f.Filename, f.Bytes)
			if !ready {
				allReady = false
			}
			files = append(files, fileEntry{
				Role:     f.Role,
				Filename: f.Filename,
				Bytes:    f.Bytes,
				Ready:    ready,
			})
		}
		entries = append(entries, entry{
			ID:      m.ID,
			Name:    m.Name,
			Files:   files,
			Ready:   allReady,
			Bundled: m.Bundled,
			Apps:    m.Apps,
			Enables: m.Enables,
		})
	}
	out, _ := json.MarshalIndent(entries, "", "  ")
	fmt.Println(string(out))
}

func cmdDownload(id string) {
	m := manifest.Find(id)
	if m == nil {
		fmt.Fprintf(os.Stderr, "error: unknown model %q\n", id)
		os.Exit(1)
	}
	if m.Bundled {
		fmt.Println("model is bundled with app, no download needed")
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
		fmt.Printf("model %q already downloaded\n", id)
		return
	}

	if err := storage.EnsureDir(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if err := storage.EnsureModelDir(id); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	for _, f := range m.Files {
		if storage.IsFileReady(id, f.Filename, f.Bytes) {
			label := f.Filename
			if f.Role != "" {
				label = f.Role + ": " + label
			}
			fmt.Printf("  %s (already ready)\n", label)
			continue
		}

		destPath := storage.ModelFilePath(id, f.Filename)
		label := f.Filename
		if f.Role != "" {
			label = f.Role
		}
		fmt.Printf("downloading %s [%s] ...\n", m.Name, label)

		err := download.Download(f.URLs, destPath, f.Bytes, download.Config{}, func(p download.Progress) {
			fmt.Printf("\r  %d%% (%d / %d MB) @ %.1f MB/s",
				p.Pct,
				p.Done/(1024*1024),
				p.Total/(1024*1024),
				float64(p.Speed)/(1024*1024),
			)
		})
		fmt.Println()

		if err != nil {
			fmt.Fprintf(os.Stderr, "download failed: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Printf("done: %s\n", id)
}

func cmdPath(id string) {
	m := manifest.Find(id)
	if m == nil {
		fmt.Fprintf(os.Stderr, "error: unknown model %q\n", id)
		os.Exit(1)
	}
	paths := make(map[string]string)
	for _, f := range m.Files {
		if !m.Bundled && !storage.IsFileReady(id, f.Filename, f.Bytes) {
			fmt.Fprintf(os.Stderr, "model %q is not ready (not downloaded)\n", id)
			os.Exit(1)
		}
		role := f.Role
		if role == "" {
			role = "default"
		}
		paths[role] = storage.ModelFilePath(id, f.Filename)
	}
	out, _ := json.MarshalIndent(paths, "", "  ")
	fmt.Println(string(out))
}

func cmdHardware() {
	info := hardware.Detect()
	out, _ := json.MarshalIndent(info, "", "  ")
	fmt.Println(string(out))
}

func cmdRecommend() {
	machine := hardware.Detect()
	annotated := fit.AnnotateModels(manifest.Registry, machine, nil)
	fit.SortByFit(annotated)

	type entry struct {
		ID         string       `json:"id"`
		Name       string       `json:"name"`
		Params     string       `json:"params,omitempty"`
		Family     string       `json:"family,omitempty"`
		Fit        fit.FitLevel `json:"fit"`
		MemPercent int          `json:"memPercent"`
		TPS        int          `json:"tps"`
	}

	var entries []entry
	for _, a := range annotated {
		if a.Params == "" {
			continue
		}
		entries = append(entries, entry{
			ID:         a.ID,
			Name:       a.Name,
			Params:     a.Params,
			Family:     a.Family,
			Fit:        a.Fit,
			MemPercent: a.MemPercent,
			TPS:        a.TPS,
		})
	}

	result := map[string]interface{}{
		"machine": machine,
		"models":  entries,
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(out))
}
