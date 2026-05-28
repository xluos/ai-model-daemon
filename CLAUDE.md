# ai-model-daemon

## Overview

本项目是桌面端 AI 能力的**基建层**。不追求自身打包为独立二进制，而是作为基础设施被其他 Electron 应用（clipiq、flashcull 等）集成打包。核心价值：多个桌面应用共享同一份模型文件、推理运行时和请求队列，通过 Unix socket HTTP API 通信，避免重复下载和多实例资源冲突。

支持的推理后端：
- **LLM** — llama-server (llama.cpp)，本地大语言模型 / 视觉语言模型推理
- **Whisper** — whisper-server (whisper.cpp)，语音转文字
- **faster-whisper** — Python faster-whisper，比 whisper.cpp 快 4× 且显存占用低 50%
- **PaddleOCR** — Python PaddleOCR，文字检测 + 识别 + 方向分类

运行时架构为插件式 `Runtime` 接口 + `map[string]Runtime` 注册表，新增后端只需实现接口并注册，Queue / Proxy / Server 通用路由自动生效。

附带嵌入式中文 Web UI 和 OpenAI 兼容代理。

## Project Structure

```
main.go                              CLI entry + process reuse detection
pkg/
  manifest/manifest.go               Model registry (LLM + Whisper + ONNX + OCR + faster-whisper)
  storage/storage.go                  Shared model file storage (~/.../AIModels/), tar dir detection
  download/
    download.go                      Resumable HTTP download with mirror fallback (EMA-smoothed speed)
    extract.go                       Post-download tar/tar.gz extraction (preserves directory structure)
  hardware/                          Platform-specific hardware detection
    hardware.go                      Core detection logic (CPU/RAM/GPU/backend)
    hardware_darwin.go               macOS: sysctl for memory, brand string
    hardware_linux.go                Linux: /proc/cpuinfo, sysinfo
    hardware_windows.go              Windows: GlobalMemoryStatusEx, wmic
  fit/fit.go                         Fit calculation, TPS estimation, model sorting
  runtime/
    iface.go                         Runtime interface (Kind, Ensure, Stop, Status, AcquireSlot, etc.)
    process.go                       Child process lifecycle (spawn, health check, graceful stop, crash recovery)
    binary.go                        Inference binary management (download/discover llama-server, whisper-server)
    binary_{darwin,linux,windows}.go  Platform-specific binary asset selection
    embed.go                         go:embed for Python scripts + whisper-server binary
    llm.go                           LLM runtime (llama-server)
    whisper.go                       Whisper runtime (whisper-server)
    faster_whisper.go                faster-whisper runtime (Python subprocess)
    ocr.go                           PaddleOCR runtime (Python subprocess)
    runtime.go                       RuntimeManager: plugin registry for all runtime kinds
    resources/
      ocr_server.py                  Minimal PaddleOCR HTTP wrapper (stdlib http.server)
      faster_whisper_server.py       Minimal faster-whisper HTTP wrapper (OpenAI-compatible)
  queue/queue.go                     Request queue with model-affinity scheduling, per-kind state
  proxy/proxy.go                     OpenAI-compatible reverse proxy + OCR endpoint
  clients/tracker.go                 Client reference counting + auto-shutdown
internal/
  server/server.go                   HTTP API handlers (Unix socket + optional TCP)
  server/listen_unix.go              Unix socket listener
  server/listen_windows.go           Windows TCP fallback
  webui/
    embed.go                         go:embed for Web UI
    index.html                       Single-file Chinese Web UI (vanilla HTML/CSS/JS)
```

## Development

### Prerequisites

- Go 1.22+
- Python 3.8+ (for OCR and faster-whisper runtimes, provided by integrating Electron app)

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
- `POST /models/{id}/download` — Download (SSE progress stream, auto-extracts tar archives)
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
- `POST /v1/audio/transcriptions` — Whisper transcription (whisper.cpp backend, multipart)
- `POST /v1/audio/transcriptions/faster` — Whisper transcription (faster-whisper backend, multipart)
- `POST /v1/ocr` — PaddleOCR text recognition (image body, optional `?model=` param)

#### Runtime Management
- `GET /api/runtime/status` — All runtime status (auto-includes every registered kind)
- `POST /api/runtime/{kind}/start` — Start any runtime (`{"modelId":"..."}`)
- `POST /api/runtime/{kind}/stop` — Stop any runtime
- `GET /api/runtime/{kind}/logs` — Process logs for any runtime (ring buffer, last 200 lines)
- Legacy routes still work: `/api/runtime/llm/start`, `/api/runtime/whisper/start`, etc.

#### Queue
- `GET /api/queue/status` — Queue depth, pending by model, batch count (per runtime kind)
- `POST /api/queue/config` — Update scheduler config

#### Binaries
- `GET /api/binaries/status` — Inference binary install status
- `POST /api/binaries/llama-server/download` — Download llama-server (SSE)
- `POST /api/binaries/whisper-server/download` — Download whisper-server (SSE)

#### Client Lifecycle
- `POST /api/clients/register` — Register client app (must include `pid`)
- `POST /api/clients/deregister` — Deregister client app
- `POST /api/clients/heartbeat` — Optional heartbeat
- `GET /api/clients` — List connected clients

#### Web UI
- `GET /` — Redirect to `/ui?token=<token>`
- `GET /ui` — Embedded Chinese management panel

## Architecture

### Project Positioning

本项目**不是**独立分发的可执行文件，而是 Electron 桌面应用的基建层。Python 运行时和 PaddlePaddle / faster-whisper 等依赖由上层 Electron 打包方负责打包分发，daemon 只负责子进程管理。集成方设置 `PYTHON_PATH` 环境变量指向打包的 Python 解释器即可。

### Extensible Runtime Architecture

RuntimeManager 使用插件式注册表：

```go
type Runtime interface {
    Kind() string
    Ensure(modelID string, opts any) error
    Stop() error
    IsReady() bool
    LoadedModel() string
    ProxyURL() string
    AcquireSlot() / ReleaseSlot() / InFlightCount()
    MaxParallel() int
    Logs() []string
    Status() any
}

// RuntimeManager holds map[string]Runtime
rm.Register(NewLLMRuntime(binMgr))        // "llm"
rm.Register(NewWhisperRuntime(binMgr))     // "whisper"
rm.Register(NewOCRRuntime(binDir))         // "ocr"
rm.Register(NewFasterWhisperRuntime(binDir)) // "faster-whisper"
```

新增运行时只需：实现 Runtime 接口 → 调用 Register → 在 manifest 里加模型条目。Queue 调度、通用 API 路由 (`/api/runtime/{kind}/*`)、Status 报告全部自动生效。

### Process Communication

所有运行时（无论 C++ 还是 Python）遵循统一的子进程通讯契约：

1. daemon spawn 子进程，传 `--host 127.0.0.1 --port PORT --model PATH` 等参数
2. 子进程启动后暴露 `GET /health` → 200
3. daemon 轮询健康检查（500ms 间隔，30-120s 超时）
4. 就绪后 daemon 代理请求到 `http://127.0.0.1:PORT`
5. 停止：SIGTERM → 5s grace → SIGKILL
6. 崩溃自动重启：指数退避 (1s, 2s, 4s…)，60s 内 3 次崩溃触发熔断

### Python Runtimes (OCR / faster-whisper)

Python wrapper 脚本嵌入在 Go 二进制中（`go:embed`），启动时自动解压到 `.bin/` 目录。使用 Python stdlib `http.server`，不依赖任何第三方 HTTP 框架。

- `ocr_server.py` — PaddleOCR wrapper，接收图片返回 JSON (boxes + text + confidence)
- `faster_whisper_server.py` — faster-whisper wrapper，OpenAI 兼容 transcription 接口

### Model Download & Storage

- 断点续传，多镜像源自动切换
- **tar 归档自动解压**：PaddleOCR 模型以 `.tar` 分发，下载完成后自动解压到同目录，保留内部目录结构
- `storage.IsFileReady` 同时检查原始文件和解压后的目录
- 模型文件按 ID 存放在 `AIModels/{modelID}/` 下

### Process Reuse
`serve` checks PID file before starting. If existing daemon is alive, returns its connection info (`reused: true`) and exits immediately.

### Client Lifecycle & Daemon Shutdown
- Apps register with PID on connect, deregister on exit.
- PID liveness detection (not heartbeat TTL) — sleep/wake safe.
- Reference counting: daemon auto-shuts down when ALL clients leave (30s grace).
- Inference child processes have their own idle timeout — auto-unload on no requests, auto-reload on next request.

### Request Queue
Model-affinity scheduling per runtime kind. Batches same-model requests to minimize model switches. Configurable parallel slots, idle timeout, anti-starvation.

### Fit Calculation

Memory estimation: `totalMem = quantSizeBytes + ctx × kvBytesPerToken × 1.05`

Fit levels against GPU memory cap (Apple Silicon: totalMem × 67%):
- `perfect`: < 50%
- `good`: 50-75%
- `marginal`: 75-95%
- `tight`: ≥ 95%

TPS estimation: `TPS = speedConstant / paramsB × quantSpeedMult`
- Metal: K=160, CUDA: K=220, ROCm: K=180, CPU: K=80
