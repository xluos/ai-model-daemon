package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xluos/ai-model-daemon/pkg/download"
	"github.com/xluos/ai-model-daemon/pkg/fit"
	"github.com/xluos/ai-model-daemon/pkg/hardware"
	"github.com/xluos/ai-model-daemon/pkg/manifest"
	"github.com/xluos/ai-model-daemon/internal/server"
	"github.com/xluos/ai-model-daemon/pkg/storage"
)

var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
		return
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
  serve [--http addr] [--no-reuse]  Start the daemon (Unix socket + optional TCP)
  status             Print daemon status
  list               List all models (JSON)
  download <id>      Download a model (blocks until complete)
  path <id>          Print model file paths (JSON)
  hardware           Detect and print hardware info (JSON)
  recommend          List models sorted by fit for this machine (JSON)
  version            Print version

Examples:
  ai-model-daemon serve --http :9090              # Unix socket + HTTP on port 9090
  ai-model-daemon serve --no-reuse --http :9090   # Force new instance`)
}

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", os.Getpid())
	}
	return hex.EncodeToString(b)
}

type serveFlags struct {
	httpAddr string
	noReuse  bool
}

func parseServeFlags() serveFlags {
	var f serveFlags
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--http":
			if i+1 < len(os.Args) {
				f.httpAddr = os.Args[i+1]
				i++
			}
		case "--no-reuse":
			f.noReuse = true
		}
	}
	return f
}

func isExistingDaemonAlive(pidPath, socketPath string) (int, string, bool) {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, "", false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, "", false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, "", false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return 0, "", false
	}
	tokenData, _ := os.ReadFile(storage.TokenPath())
	token := strings.TrimSpace(string(tokenData))
	if token == "" {
		return 0, "", false
	}
	// PID 存活不代表是我们的 daemon（PID 可能被复用）
	// 必须验证 socket 能连通并且 /status 返回正常
	if !probeDaemonSocket(socketPath, token) {
		return 0, "", false
	}
	return pid, token, true
}

func probeDaemonSocket(socketPath, token string) bool {
	resp, err := daemonGet(socketPath, token, "/status")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func probeDaemonVersion(socketPath, token string) string {
	resp, err := daemonGet(socketPath, token, "/version")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var v struct{ Version string }
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return ""
	}
	return v.Version
}

func daemonGet(socketPath, token, path string) (*http.Response, error) {
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.DialTimeout("unix", socketPath, 2*time.Second)
			},
		},
		Timeout: 3 * time.Second,
	}
	req, err := http.NewRequest("GET", "http://daemon"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return client.Do(req)
}

func cmdServe() {
	flags := parseServeFlags()

	if err := storage.EnsureDir(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	socketPath := storage.SocketPath()
	pidPath := storage.PIDPath()
	tokenPath := storage.TokenPath()

	if existPID, existToken, alive := isExistingDaemonAlive(pidPath, socketPath); alive {
		canReuse := !flags.noReuse
		if canReuse {
			remoteVer := probeDaemonVersion(socketPath, existToken)
			if remoteVer != "" && remoteVer != Version {
				fmt.Fprintf(os.Stderr, "existing daemon version %q != current %q, stopping it\n", remoteVer, Version)
				canReuse = false
			}
		}
		if canReuse {
			ready := map[string]interface{}{
				"socket":  socketPath,
				"pid":     existPID,
				"token":   existToken,
				"version": Version,
				"reused":  true,
			}
			if existHTTP, err := os.ReadFile(storage.HTTPAddrPath()); err == nil {
				ready["http"] = strings.TrimSpace(string(existHTTP))
			}
			readyJSON, _ := json.Marshal(ready)
			fmt.Println(string(readyJSON))
			fmt.Fprintf(os.Stderr, "daemon already running (pid %d), reusing\n", existPID)
			os.Exit(0)
		}
		// kill the old daemon before starting
		if proc, err := os.FindProcess(existPID); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
			time.Sleep(500 * time.Millisecond)
		}
	}

	pid := os.Getpid()
	token := generateToken()

	httpAddrPath := storage.HTTPAddrPath()
	os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644)
	os.WriteFile(tokenPath, []byte(token), 0600)
	if flags.httpAddr != "" {
		os.WriteFile(httpAddrPath, []byte(flags.httpAddr), 0644)
	} else {
		os.Remove(httpAddrPath)
	}

	shutdown := func() {
		fmt.Fprintf(os.Stderr, "all clients disconnected, shutting down\n")
		os.Remove(socketPath)
		os.Remove(pidPath)
		os.Remove(tokenPath)
		os.Remove(httpAddrPath)
		os.Exit(0)
	}

	srv := server.New(token, Version, shutdown)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		done := make(chan struct{})
		go func() {
			srv.Close()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			fmt.Fprintf(os.Stderr, "cleanup timed out, forcing exit\n")
		}
		os.Remove(socketPath)
		os.Remove(pidPath)
		os.Remove(tokenPath)
		os.Remove(httpAddrPath)
		os.Exit(0)
	}()

	ready := map[string]interface{}{
		"socket":  socketPath,
		"pid":     pid,
		"token":   token,
		"version": Version,
	}
	if flags.httpAddr != "" {
		ready["http"] = flags.httpAddr
	}
	readyJSON, _ := json.Marshal(ready)
	fmt.Println(string(readyJSON))

	if flags.httpAddr != "" {
		go func() {
			fmt.Fprintf(os.Stderr, "HTTP listening on %s\n", flags.httpAddr)
			if err := srv.ListenAndServeTCP(flags.httpAddr); err != nil {
				fmt.Fprintf(os.Stderr, "http server error: %v\n", err)
			}
		}()
	}

	if err := srv.ListenAndServe(socketPath); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Remove(socketPath)
		os.Remove(pidPath)
		os.Remove(tokenPath)
		os.Remove(httpAddrPath)
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

		err := download.Download(context.Background(), f.URLs, destPath, f.Bytes, download.Config{}, func(p download.Progress) {
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
