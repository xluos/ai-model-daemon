package runtime

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed resources
var embeddedResources embed.FS

func (bm *BinaryManager) extractEmbedded() {
	entries := []struct {
		embedPath string
		binName   string
	}{
		{"resources/whisper-server", "whisper-server"},
		{"resources/ocr_server.py", "ocr_server.py"},
		{"resources/faster_whisper_server.py", "faster_whisper_server.py"},
	}

	os.MkdirAll(bm.binDir, 0755)
	for _, e := range entries {
		data, err := embeddedResources.ReadFile(e.embedPath)
		if err != nil || len(data) == 0 {
			continue
		}
		dest := filepath.Join(bm.binDir, e.binName)
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		os.WriteFile(dest, data, 0755)
	}
}
