package storage

import (
	"os"
	"path/filepath"
	"runtime"
)

// Dir returns the shared model storage directory.
func Dir() string {
	var base string
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "Library", "Application Support", "AIModels")
	case "windows":
		base = filepath.Join(os.Getenv("LOCALAPPDATA"), "AIModels")
	default:
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share", "AIModels")
	}
	return base
}

// EnsureDir creates the storage directory if it doesn't exist.
func EnsureDir() error {
	return os.MkdirAll(Dir(), 0755)
}

// SocketPath returns the daemon's Unix socket path.
func SocketPath() string {
	return filepath.Join(Dir(), ".daemon.sock")
}

// PIDPath returns the PID file path.
func PIDPath() string {
	return filepath.Join(Dir(), ".daemon.pid")
}

// TokenPath returns the auth token file path.
func TokenPath() string {
	return filepath.Join(Dir(), ".daemon.token")
}

// HTTPAddrPath returns the file path storing the daemon's HTTP listen address.
func HTTPAddrPath() string {
	return filepath.Join(Dir(), ".daemon.http")
}

// ModelDir returns the directory for a specific model.
func ModelDir(modelID string) string {
	return filepath.Join(Dir(), modelID)
}

// EnsureModelDir creates the model subdirectory.
func EnsureModelDir(modelID string) error {
	return os.MkdirAll(ModelDir(modelID), 0755)
}

// ModelFilePath returns the full path for a file within a model's directory.
func ModelFilePath(modelID, filename string) string {
	return filepath.Join(Dir(), modelID, filename)
}

// IsFileReady checks if a model file exists and has expected size (within 5% tolerance).
// If expectedBytes is 0, only checks that the file exists and is non-empty.
func IsFileReady(modelID, filename string, expectedBytes int64) bool {
	p := ModelFilePath(modelID, filename)
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	size := info.Size()
	if expectedBytes <= 0 {
		return size > 0
	}
	low := int64(float64(expectedBytes) * 0.95)
	high := int64(float64(expectedBytes) * 1.05)
	return size >= low && size <= high
}
