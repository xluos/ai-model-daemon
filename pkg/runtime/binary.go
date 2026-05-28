package runtime

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/xluos/ai-model-daemon/pkg/download"
	"github.com/xluos/ai-model-daemon/pkg/storage"
)

const (
	llamaCppRelease    = "b9128"
	llamaCppBase       = "https://github.com/ggml-org/llama.cpp/releases/download"
	whisperCppVersion  = "v1.8.4"
	whisperCppBase     = "https://github.com/ggml-org/whisper.cpp/releases/download"
)

type BinaryKind string

const (
	BinaryLlamaServer   BinaryKind = "llama-server"
	BinaryWhisperServer BinaryKind = "whisper-server"
)

type binaryAsset struct {
	Name   string
	Format string // "tar.gz" or "zip"
	SHA256 string // empty = skip verification
}

type BinaryInfo struct {
	Kind        BinaryKind `json:"kind"`
	Path        string     `json:"path"`
	Version     string     `json:"version"`
	Available   bool       `json:"available"`
	Source      string     `json:"source"`      // "env", "local", "path", "download"
	Downloadable bool     `json:"downloadable"` // whether auto-download is supported on this platform
	InstallHint string    `json:"installHint,omitempty"` // manual install instructions if not downloadable
}

type BinaryManager struct {
	binDir string
}

func NewBinaryManager() *BinaryManager {
	bm := &BinaryManager{
		binDir: filepath.Join(storage.Dir(), ".bin"),
	}
	bm.extractEmbedded()
	return bm
}

func (bm *BinaryManager) BinDir() string {
	return bm.binDir
}

func (bm *BinaryManager) Status(kind BinaryKind) BinaryInfo {
	info := BinaryInfo{Kind: kind, Version: bm.releaseVersion(kind)}
	path, source := bm.findBinary(kind)
	if path != "" {
		info.Path = path
		info.Available = true
		info.Source = source
	}
	asset := bm.platformAsset(kind)
	info.Downloadable = asset != nil || bm.canBuildFromSource(kind)
	if !info.Available && !info.Downloadable {
		info.InstallHint = bm.installHint(kind)
	}
	return info
}

func (bm *BinaryManager) canBuildFromSource(kind BinaryKind) bool {
	if kind != BinaryWhisperServer {
		return false
	}
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}

func (bm *BinaryManager) installHint(kind BinaryKind) string {
	return ""
}

func (bm *BinaryManager) Resolve(kind BinaryKind) (string, error) {
	p, _ := bm.findBinary(kind)
	if p == "" {
		info := bm.Status(kind)
		if info.InstallHint != "" {
			return "", fmt.Errorf("%s not found; install: %s", kind, info.InstallHint)
		}
		return "", fmt.Errorf("%s binary not found; use POST /api/binaries/%s/download to install", kind, kind)
	}
	return p, nil
}

func (bm *BinaryManager) Download(ctx context.Context, kind BinaryKind, onProgress func(download.Progress)) error {
	asset := bm.platformAsset(kind)
	if asset == nil {
		if bm.canBuildFromSource(kind) {
			return bm.buildFromSource(ctx, kind, onProgress)
		}
		return fmt.Errorf("no prebuilt %s binary available for %s/%s", kind, runtime.GOOS, runtime.GOARCH)
	}

	if err := os.MkdirAll(bm.binDir, 0755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	url := bm.assetURL(kind, asset)
	archivePath := filepath.Join(bm.binDir, asset.Name)

	if err := download.Download(ctx, []string{url}, archivePath, 0, download.Config{}, onProgress); err != nil {
		return fmt.Errorf("download %s: %w", kind, err)
	}

	if asset.SHA256 != "" {
		if err := verifySHA256(archivePath, asset.SHA256); err != nil {
			os.Remove(archivePath)
			return err
		}
	}

	if err := extractAll(archivePath, asset.Format, bm.binDir); err != nil {
		os.Remove(archivePath)
		return fmt.Errorf("extract %s: %w", kind, err)
	}

	os.Remove(archivePath)

	destPath := filepath.Join(bm.binDir, bm.exeName(kind))
	os.Chmod(destPath, 0755)

	return nil
}

func (bm *BinaryManager) buildFromSource(ctx context.Context, kind BinaryKind, onProgress func(download.Progress)) error {
	if err := os.MkdirAll(bm.binDir, 0755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	scriptPath, err := bm.findBuildScript(kind)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"OUT_DIR="+bm.binDir,
		"WHISPER_VERSION="+whisperCppVersion,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start build script: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "PROGRESS:") {
			stage := strings.TrimPrefix(line, "PROGRESS:")
			if onProgress != nil {
				onProgress(download.Progress{Status: stage})
			}
		} else if strings.HasPrefix(line, "ERROR:") {
			return fmt.Errorf("build %s: %s", kind, strings.TrimPrefix(line, "ERROR:"))
		}
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("build %s failed: %w", kind, err)
	}

	destPath := filepath.Join(bm.binDir, bm.exeName(kind))
	if _, err := os.Stat(destPath); err != nil {
		return fmt.Errorf("build completed but binary not found at %s", destPath)
	}

	return nil
}

func (bm *BinaryManager) findBuildScript(kind BinaryKind) (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find executable: %w", err)
	}
	exeDir := filepath.Dir(exePath)

	candidates := []string{
		filepath.Join(exeDir, "scripts", "build-whisper-server.sh"),
		filepath.Join(exeDir, "..", "scripts", "build-whisper-server.sh"),
	}

	wd, _ := os.Getwd()
	if wd != "" {
		candidates = append(candidates, filepath.Join(wd, "scripts", "build-whisper-server.sh"))
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("build script not found for %s (searched near executable and working directory)", kind)
}

func (bm *BinaryManager) findBinary(kind BinaryKind) (path string, source string) {
	envKey := bm.envVar(kind)
	if envPath := os.Getenv(envKey); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath, "env"
		}
	}

	localPath := filepath.Join(bm.binDir, bm.exeName(kind))
	if _, err := os.Stat(localPath); err == nil {
		return localPath, "local"
	}

	for _, dir := range bm.knownBinaryDirs(kind) {
		p := filepath.Join(dir, bm.exeName(kind))
		if _, err := os.Stat(p); err == nil {
			return p, "discovered"
		}
	}

	if p, err := exec.LookPath(string(kind)); err == nil {
		return p, "path"
	}

	return "", ""
}

func (bm *BinaryManager) knownBinaryDirs(kind BinaryKind) []string {
	return nil
}

func (bm *BinaryManager) exeName(kind BinaryKind) string {
	name := string(kind)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func (bm *BinaryManager) envVar(kind BinaryKind) string {
	switch kind {
	case BinaryLlamaServer:
		return "LLAMA_SERVER_PATH"
	case BinaryWhisperServer:
		return "WHISPER_SERVER_PATH"
	default:
		return ""
	}
}

func (bm *BinaryManager) releaseVersion(kind BinaryKind) string {
	switch kind {
	case BinaryLlamaServer:
		return llamaCppRelease
	case BinaryWhisperServer:
		return whisperCppVersion
	default:
		return ""
	}
}

func (bm *BinaryManager) assetURL(kind BinaryKind, asset *binaryAsset) string {
	switch kind {
	case BinaryLlamaServer:
		return fmt.Sprintf("%s/%s/%s", llamaCppBase, llamaCppRelease, asset.Name)
	case BinaryWhisperServer:
		return fmt.Sprintf("%s/%s/%s", whisperCppBase, whisperCppVersion, asset.Name)
	default:
		return ""
	}
}

func verifySHA256(path string, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("verify sha256: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("verify sha256: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, expected)
	}
	return nil
}

func extractAll(archivePath, format, destDir string) error {
	switch format {
	case "tar.gz":
		return extractAllTarGz(archivePath, destDir)
	case "zip":
		return extractAllZip(archivePath, destDir)
	default:
		return fmt.Errorf("unknown archive format: %s", format)
	}
}

func extractAllTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag == tar.TypeSymlink {
			base := filepath.Base(hdr.Name)
			linkDest := filepath.Join(destDir, base)
			os.Remove(linkDest)
			os.Symlink(hdr.Linkname, linkDest)
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		base := filepath.Base(hdr.Name)
		dest := filepath.Join(destDir, base)
		mode := os.FileMode(hdr.Mode)
		if mode == 0 {
			mode = 0644
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, tr)
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

func extractAllZip(archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(f.Name)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		dest := filepath.Join(destDir, base)
		mode := f.Mode()
		if mode == 0 {
			mode = 0644
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

// platformKey returns "GOOS-GOARCH" for the current platform.
func platformKey() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return fmt.Sprintf("%s-%s", runtime.GOOS, arch)
}

// llamaCppAssetName returns the asset filename with the release tag substituted.
func llamaCppAssetName(template string) string {
	return strings.ReplaceAll(template, "${REL}", llamaCppRelease)
}
