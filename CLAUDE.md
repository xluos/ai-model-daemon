# ai-model-daemon

## Overview

Shared local AI model management daemon. Provides model file storage, download (resumable, multi-mirror), hardware detection, fit/TPS estimation, and model recommendation via Unix socket HTTP API and CLI.

## Project Structure

```
main.go                         CLI entry (serve/status/list/download/path/hardware/recommend)
internal/
  manifest/manifest.go          Model registry with quantization metadata
  storage/storage.go            Shared model file storage (~/.../AIModels/)
  download/download.go          Resumable HTTP download with mirror fallback
  hardware/                     Platform-specific hardware detection
    hardware.go                 Core detection logic (CPU/RAM/GPU/backend)
    hardware_darwin.go           macOS: sysctl for memory, brand string
    hardware_linux.go            Linux: /proc/cpuinfo, sysinfo
    hardware_windows.go          Windows: GlobalMemoryStatusEx, wmic
  fit/fit.go                    Fit calculation, TPS estimation, model sorting
  server/
    server.go                   HTTP API handlers (Unix socket)
    listen_unix.go              Unix socket listener
    listen_windows.go           Windows TCP fallback
```

## Development

### Prerequisites

- Go 1.22+

### Commands

- Build: `go build -o ai-model-daemon .`
- Install: `go build -o ~/.local/bin/ai-model-daemon .`
- Run CLI: `go run . <command>`

### CLI Commands

- `serve` — Start daemon (HTTP over Unix socket)
- `status` — Print daemon status
- `list` — List all models (JSON)
- `download <id>` — Download a model
- `path <id>` — Print model file paths
- `hardware` — Detect and print hardware info
- `recommend` — List models sorted by fit for this machine

### HTTP API Endpoints

- `GET /status` — Daemon status
- `GET /models` — List models (optional `?app=` filter)
- `GET /models/{id}` — Get single model status
- `POST /models/{id}/download` — Download (SSE progress stream)
- `GET /models/{id}/path` — Get file paths
- `DELETE /models/{id}` — Delete model files
- `POST /config` — Set preferences (mirror)
- `GET /hardware` — Hardware detection info
- `GET /models/recommended` — Models with fit annotation, sorted by fit
- `POST /models/{id}/recompute-fit` — Recompute fit with custom context size

## Architecture

### Fit Calculation

Memory estimation: `totalMem = quantSizeBytes + ctx × kvBytesPerToken × 1.05`

Fit levels against GPU memory cap (Apple Silicon: totalMem × 67%):
- `perfect`: < 50%
- `good`: 50-75%
- `marginal`: 75-95%
- `tight`: ≥ 95%

TPS estimation: `TPS = speedConstant / paramsB × quantSpeedMult`
- Metal: K=160, CUDA: K=220, ROCm: K=180, CPU: K=80
