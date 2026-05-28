package download

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Progress reports the current download state.
type Progress struct {
	ModelID  string `json:"modelId"`
	FileRole string `json:"fileRole,omitempty"`
	Status   string `json:"status,omitempty"`
	Done     int64  `json:"done"`
	Total    int64  `json:"total"`
	Pct      int    `json:"pct"`
	Speed    int64  `json:"speed"`
	Mirror   string `json:"mirror"`
}

// Config holds download preferences.
type Config struct {
	PreferMirror     string        // "hf-mirror" | "huggingface" | "modelscope" | "" (auto)
	ProgressInterval time.Duration // SSE progress push interval; 0 means default (500ms)
}

// Download fetches a file with resumable support.
// Tries URLs in order; on failure of one, tries next.
// The context can be used to cancel the download; partial .part files are preserved for resume.
func Download(ctx context.Context, urls []string, destPath string, expectedBytes int64, cfg Config, onProgress func(Progress)) error {
	if len(urls) == 0 {
		return fmt.Errorf("no download URLs provided")
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	partPath := destPath + ".part"
	urlPath := destPath + ".part.url"

	var lastErr error
	for _, u := range urls {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := downloadFromURL(ctx, u, partPath, urlPath, destPath, expectedBytes, cfg, onProgress)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = err
	}
	return fmt.Errorf("all mirrors failed, last error: %w", lastErr)
}

func downloadFromURL(ctx context.Context, url, partPath, urlPath, destPath string, expectedBytes int64, cfg Config, onProgress func(Progress)) error {
	if data, err := os.ReadFile(urlPath); err == nil {
		if string(data) != url {
			os.Remove(partPath)
		}
	}

	var partSize int64
	if info, err := os.Stat(partPath); err == nil {
		partSize = info.Size()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if partSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", partSize))
	}

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		os.Remove(partPath)
		partSize = 0
		resp.Body.Close()
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return fmt.Errorf("build request (retry): %w", err)
		}
		resp, err = client.Do(req)
		if err != nil {
			return fmt.Errorf("http request (retry): %w", err)
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}

	var total int64
	if resp.StatusCode == http.StatusPartialContent {
		total = partSize + resp.ContentLength
	} else {
		total = resp.ContentLength
		partSize = 0
	}
	if total <= 0 && expectedBytes > 0 {
		total = expectedBytes
	}

	os.WriteFile(urlPath, []byte(url), 0644)

	var flag int
	if partSize > 0 && resp.StatusCode == http.StatusPartialContent {
		flag = os.O_WRONLY | os.O_APPEND
	} else {
		flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		partSize = 0
	}
	f, err := os.OpenFile(partPath, flag|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("open part file: %w", err)
	}
	defer f.Close()

	progressInterval := cfg.ProgressInterval
	if progressInterval <= 0 {
		progressInterval = 2 * time.Second
	}

	buf := make([]byte, 64*1024)
	done := partSize
	lastReport := time.Now()
	lastReportDone := done
	const emaAlpha = 0.3
	emaSpeed := float64(0)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := f.Write(buf[:n]); wErr != nil {
				return fmt.Errorf("write part file: %w", wErr)
			}
			done += int64(n)

			now := time.Now()
			elapsed := now.Sub(lastReport)
			if elapsed >= progressInterval {
				instantSpeed := float64(done-lastReportDone) / elapsed.Seconds()
				if emaSpeed == 0 {
					emaSpeed = instantSpeed
				} else {
					emaSpeed = emaAlpha*instantSpeed + (1-emaAlpha)*emaSpeed
				}
				pct := 0
				if total > 0 {
					pct = int(done * 100 / total)
				}
				if onProgress != nil {
					onProgress(Progress{
						Done:   done,
						Total:  total,
						Pct:    pct,
						Speed:  int64(emaSpeed),
						Mirror: url,
					})
				}
				lastReport = now
				lastReportDone = done
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("read body: %w", readErr)
		}
	}

	f.Close()

	if expectedBytes > 0 {
		info, err := os.Stat(partPath)
		if err != nil {
			return fmt.Errorf("stat part file: %w", err)
		}
		low := int64(float64(expectedBytes) * 0.95)
		high := int64(float64(expectedBytes) * 1.05)
		if info.Size() < low || info.Size() > high {
			return fmt.Errorf("size mismatch: got %d, expected ~%d", info.Size(), expectedBytes)
		}
	}

	if err := os.Rename(partPath, destPath); err != nil {
		return fmt.Errorf("rename part to final: %w", err)
	}
	os.Remove(urlPath)

	if onProgress != nil {
		if total <= 0 {
			total = done
		}
		onProgress(Progress{
			Done:  done,
			Total: total,
			Pct:   100,
			Speed: 0,
		})
	}

	return nil
}
