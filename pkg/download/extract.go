package download

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractTarIfNeeded checks if destPath ends with .tar or .tar.gz and extracts
// it into the same parent directory, preserving internal directory structure.
// Returns the path of the extracted directory, or empty if no extraction was done.
func ExtractTarIfNeeded(destPath string) (string, error) {
	lower := strings.ToLower(destPath)
	var isTarGz bool
	if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") {
		isTarGz = true
	} else if strings.HasSuffix(lower, ".tar") {
		isTarGz = false
	} else {
		return "", nil
	}

	parentDir := filepath.Dir(destPath)
	extractedDir, err := extractTarPreservingStructure(destPath, parentDir, isTarGz)
	if err != nil {
		return "", fmt.Errorf("extract %s: %w", filepath.Base(destPath), err)
	}
	return extractedDir, nil
}

func extractTarPreservingStructure(archivePath, destDir string, isGzipped bool) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var reader io.Reader = f
	if isGzipped {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return "", err
		}
		defer gz.Close()
		reader = gz
	}

	tr := tar.NewReader(reader)
	var topDir string

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		clean := filepath.Clean(hdr.Name)
		if strings.Contains(clean, "..") {
			continue
		}

		target := filepath.Join(destDir, clean)

		// Track the top-level directory
		parts := strings.SplitN(clean, string(filepath.Separator), 2)
		if topDir == "" && len(parts) > 0 {
			topDir = parts[0]
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			mode := os.FileMode(hdr.Mode)
			if mode == 0 {
				mode = 0644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return "", err
			}
			_, copyErr := io.Copy(out, tr)
			out.Close()
			if copyErr != nil {
				return "", copyErr
			}
		}
	}

	if topDir != "" {
		return filepath.Join(destDir, topDir), nil
	}
	return "", nil
}
