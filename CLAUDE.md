# ai-model-daemon

## Overview

Shared local AI model management + inference daemon. Multiple desktop apps share model files, inference runtimes (llama-server, whisper-server), and a request queue via Unix socket HTTP API. Includes an embedded Chinese Web UI and OpenAI-compatible proxy.

## Project Structure

```
main.go                              CLI entry + process reuse detection
pkg/
  manifest/manifest.go               Model registry (LLM + Whisper + ONNX, with RuntimeKind field)
  storage/storage.go                  Shared model file storage (~/.../AIModels/)
  download/download.go                Resumable HTTP download with mirror fallback (EMA-smoothed speed)
  hardware/                           Platform-specific hardware detection
    hardware.go                       Core detection logic (CPU/RAM/GPU/backend)
    hardware_darwin.go                macOS: sysctl for memory, brand string
    hardware_linux.go                 Linux: /proc/cpuinfo, sysinfo
    hardware_windows.go               Windows: GlobalMemoryStatusEx, wmic
  fit/fit.go                          Fit calculation, TPS estimation, model sorting
  runtime/
    process.go                        Child process lifecycle (spawn, health check, graceful stop, crash recovery with circuit breaker)
    binary.go                         Inference binary management (download/discover llama-server, whisper-server)
    binary_{darwin,linux,windows}.go   Platform-specific binary asset selection
    llm.go                            LLM runtime (llama-server: start, stop, model switch, slot tracking)
    whisper.go                         Whisper runtime (whisper-server lifecycle)
    runtime.go                         RuntimeManager: top-level coordinator for LLM + Whisper
  queue/queue.go                       Request queue with model-affinity scheduling, idle timeout, anti-starvation
  proxy/proxy.go                       OpenAI-compatible reverse proxy (/v1/chat/completions, /v1/models, /v1/audio/transcriptions)
  clients/tracker.go                   Client reference counting + auto-shutdown when all clients leave
internal/
  server/server.go                     HTTP API handlers (40+ endpoints, Unix socket + optional TCP)
  server/listen_unix.go                Unix socket listener
  server/listen_windows.go             Windows TCP fallback
  webui/
    embed.go                           go:embed for Web UI
    index.html                         Single-file Chinese Web UI (vanilla HTML/CSS/JS)
```

## Development

### Prerequisites

- Go 1.22+

### Commands

- Build: `go build -o ai-model-daemon .`
- Install: `go build -o ~/.local/bin/ai-model-daemon .`
- Run CLI: `go run . <command>`

### CLI Commands

- `serve [--http addr]` — Start daemon (Unix socket + optional TCP, auto-reuses existing instance)
- `status` — Print daemon status
- `list` — List all models (JSON)
- `download <id>` — Download a model
- `path <id>` — Print model file paths
- `hardware` — Detect and print hardware info
- `recommend` — List models sorted by fit for this machine

### HTTP API Endpoints

#### Model Management
- `GET /status` — Daemon status
- `GET /models` — List models (optional `?app=` filter)
- `GET /models/{id}` — Get single model status
- `POST /models/{id}/download` — Download (SSE progress stream, optional `?progressInterval=` ms)
- `POST /models/{id}/cancel-download` — Cancel active download
- `GET /models/{id}/path` — Get file paths
- `DELETE /models/{id}` — Delete model files
- `POST /config` — Set preferences (mirror)
- `GET /hardware` — Hardware detection info
- `GET /models/recommended` — Models with fit annotation, sorted by fit
- `POST /models/{id}/recompute-fit` — Recompute fit with custom context size

#### OpenAI-Compatible Proxy
- `GET /v1/models` — Downloaded models in OpenAI format
- `POST /v1/chat/completions` — Chat completion (auto-loads model via queue)
- `POST /v1/completions` — Text completion
- `POST /v1/audio/transcriptions` — Whisper transcription (multipart)

#### Runtime Management
- `GET /api/runtime/status` — Runtime status (LLM + Whisper + binaries)
- `POST /api/runtime/llm/start` — Start LLM runtime
- `POST /api/runtime/llm/stop` — Stop LLM runtime
- `POST /api/runtime/whisper/start` — Start Whisper runtime
- `POST /api/runtime/whisper/stop` — Stop Whisper runtime
- `GET /api/runtime/llm/logs` — LLM process logs (ring buffer, last 200 lines)
- `GET /api/runtime/whisper/logs` — Whisper process logs

#### Queue
- `GET /api/queue/status` — Queue depth, pending by model, batch count
- `POST /api/queue/config` — Update scheduler config

#### Binaries
- `GET /api/binaries/status` — Inference binary install status (with downloadable/installHint fields)
- `POST /api/binaries/llama-server/download` — Download llama-server (SSE)
- `POST /api/binaries/whisper-server/download` — Download whisper-server (SSE, Windows only)

#### Client Lifecycle
- `POST /api/clients/register` — Register client app (must include `pid` for liveness detection)
- `POST /api/clients/deregister` — Deregister client app (triggers ref-count check)
- `POST /api/clients/heartbeat` — Optional heartbeat
- `GET /api/clients` — List connected clients

#### Web UI
- `GET /` — Redirect to `/ui?token=<token>`
- `GET /ui` — Embedded Chinese management panel (no auth required for HTML, token in URL param)

## Architecture

### Process Reuse
`serve` checks PID file before starting. If existing daemon is alive, returns its connection info (`reused: true`) and exits immediately. No duplicate instances.

### Client Lifecycle & Daemon Shutdown
- Apps register with PID on connect, deregister on exit.
- Daemon uses **PID liveness detection** (not heartbeat TTL) to track clients — computer sleep/wake won't cause false disconnects.
- Reference counting: daemon only auto-shuts down when ALL registered clients have left (30s grace period).
- If no client ever registers (e.g., manual `serve --http :9090` for debugging), daemon runs indefinitely.
- Inference child processes (llama-server/whisper-server) have their own idle timeout — they auto-unload when no requests come in, and auto-reload on the next request. Daemon itself stays alive.

### Request Queue
Model-affinity scheduling: batches same-model requests to minimize expensive model switches. Configurable parallel slots, idle timeout (unload inference process after N minutes of no requests), anti-starvation (maxBatchBeforeSwitch).

### Inference Runtime
llama-server/whisper-server managed as child processes. Health check polling, graceful stop (SIGTERM → 5s → SIGKILL), crash auto-restart with exponential backoff and circuit breaker (3 crashes in 60s → stop retrying).

### Binary Discovery
Search order: env var → daemon .bin/ dir → known app dirs (clipiq resources) → PATH. Discovered binaries from external locations are auto-copied to .bin/ for self-containment.

### Fit Calculation

Memory estimation: `totalMem = quantSizeBytes + ctx × kvBytesPerToken × 1.05`

Fit levels against GPU memory cap (Apple Silicon: totalMem × 67%):
- `perfect`: < 50%
- `good`: 50-75%
- `marginal`: 75-95%
- `tight`: ≥ 95%

TPS estimation: `TPS = speedConstant / paramsB × quantSpeedMult`
- Metal: K=160, CUDA: K=220, ROCm: K=180, CPU: K=80
