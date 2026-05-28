# ai-model-daemon

桌面端 AI 能力的基建层。本项目作为基础设施被 Electron 桌面应用（clipiq、flashcull 等）集成打包，多个应用通过 Unix socket 共享同一份模型文件、推理运行时和请求队列，避免重复下载和多实例资源冲突。

## 功能

- **模型存储** — 统一管理 GGUF / ONNX / Whisper / PaddleOCR / CTranslate2 模型文件，多应用共享
- **断点续传下载** — 多镜像源自动切换（hf-mirror / HuggingFace），tar 归档自动解压
- **硬件检测** — CPU 型号、总内存、可用内存、GPU 后端（Metal / CUDA / ROCm / CPU）
- **适配度评级** — 根据当前硬件计算 fit 等级 + TPS 估算 + 推荐排序
- **推理运行时管理** — 插件式架构，支持 4 种推理后端：
  - **LLM** — llama-server (llama.cpp)，大语言模型 / 视觉语言模型
  - **Whisper** — whisper-server (whisper.cpp)，语音转文字
  - **faster-whisper** — Python faster-whisper，速度更快、显存占用更低
  - **PaddleOCR** — Python PaddleOCR，中文 OCR（文字检测 + 识别 + 方向分类）
- **请求队列** — model-affinity 调度，按运行时类型独立队列，自动模型切换
- **OpenAI 兼容 API** — `/v1/chat/completions`、`/v1/audio/transcriptions`、`/v1/ocr`
- **客户端生命周期** — 引用计数 + PID 存活检测，所有客户端退出后 daemon 自动关闭
- **内置 Web UI** — 中文管理面板，嵌入二进制，零外部依赖

## 项目定位

本项目**不追求自身打包为独立可执行文件**。Python 运行时和 PaddlePaddle / faster-whisper 等依赖由上层 Electron 打包方负责分发，daemon 只做子进程管理和请求调度。集成方设置 `PYTHON_PATH` 环境变量指向打包的 Python 即可。

## 构建 & 安装

```bash
# 构建
go build -o ai-model-daemon .

# 安装到 PATH
go build -o ~/.local/bin/ai-model-daemon .
```

要求 Go 1.22+。

## 快速开始

```bash
# 启动（Unix socket + HTTP 端口）
./ai-model-daemon serve --http :9090

# 浏览器打开 http://localhost:9090 → 自动跳转 Web UI（带 token）
```

启动后终端输出 ready JSON：

```json
{
  "socket": "/Users/xxx/Library/Application Support/AIModels/.daemon.sock",
  "pid": 12345,
  "token": "abc123...",
  "http": ":9090"
}
```

如果已有 daemon 在跑，`serve` 会自动复用（不重复启动）：

```json
{
  "socket": "...",
  "pid": 12345,
  "token": "abc123...",
  "reused": true
}
```

## CLI 命令

```bash
ai-model-daemon serve [--http :9090]   # 启动守护进程（可选 TCP 端口）
ai-model-daemon status                  # 查看守护进程状态
ai-model-daemon list                    # 列出所有模型及下载状态
ai-model-daemon download <id>           # 下载模型（阻塞，带进度）
ai-model-daemon path <id>               # 查看模型文件路径
ai-model-daemon hardware                # 检测当前硬件
ai-model-daemon recommend               # 按适配度推荐模型
```

## Web UI

启动时加 `--http :9090`，浏览器访问 `http://localhost:9090`，自动跳转到带 token 的管理面板。

| 页面 | 功能 |
|------|------|
| 总览 | 守护进程状态、硬件信息、运行时状态、推理引擎安装情况、队列 |
| 模型管理 | 全部/LLM/Whisper/OCR 过滤，下载/删除/详情 |
| 推理运行时 | 各运行时启停控制、模型选择、参数配置、查看日志 |
| 请求队列 | 队列状态监控、调度配置 |
| 对话测试 | 选择模型 → 发消息 → 流式输出 |
| 接口调试 | 全部 API 端点可展开调用 |

Web UI 为纯 HTML/CSS/JS 单文件，通过 `go:embed` 嵌入，不需要 npm 构建。

## HTTP API

所有请求需携带 `Authorization: Bearer <token>` 头（或 `?token=<token>` 查询参数）。

### 模型管理

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/status` | 守护进程状态 |
| `GET` | `/models` | 模型列表（可选 `?app=clipiq` 过滤） |
| `GET` | `/models/{id}` | 单个模型状态 |
| `POST` | `/models/{id}/download` | 下载模型（SSE 进度流，tar 归档自动解压） |
| `POST` | `/models/{id}/cancel-download` | 取消下载 |
| `GET` | `/models/{id}/path` | 获取模型文件路径 |
| `DELETE` | `/models/{id}` | 删除模型文件 |
| `POST` | `/config` | 设置偏好（镜像源等） |
| `GET` | `/hardware` | 硬件检测信息 |
| `GET` | `/models/recommended` | 模型推荐列表（含 fit / TPS，按适配度排序） |
| `POST` | `/models/{id}/recompute-fit` | 用自定义 context size 重算 fit |

### OpenAI 兼容 API

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/v1/models` | 已下载模型列表（OpenAI 格式） |
| `POST` | `/v1/chat/completions` | 对话补全（自动加载 LLM 模型） |
| `POST` | `/v1/completions` | 文本补全 |
| `POST` | `/v1/audio/transcriptions` | 语音转写 — whisper.cpp 后端（multipart） |
| `POST` | `/v1/audio/transcriptions/faster` | 语音转写 — faster-whisper 后端（multipart） |
| `POST` | `/v1/ocr` | OCR 文字识别（图片 body，可选 `?model=ppocr-v4-mobile`） |

请求中的 `model` 字段对应模型 ID。如果模型未加载，daemon 自动启动推理进程并加载模型，客户端无需手动管理。

### 推理运行时管理

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/runtime/status` | 所有运行时状态 |
| `POST` | `/api/runtime/{kind}/start` | 启动运行时（`{"modelId":"..."}`）—— kind: llm / whisper / ocr / faster-whisper |
| `POST` | `/api/runtime/{kind}/stop` | 停止运行时 |
| `GET` | `/api/runtime/{kind}/logs` | 运行时进程日志（最近 200 行） |

兼容旧路由：`/api/runtime/llm/start`、`/api/runtime/whisper/stop` 等继续可用。

### 请求队列

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/queue/status` | 队列状态（按运行时类型分别展示） |
| `POST` | `/api/queue/config` | 更新调度配置 |

队列配置参数：

```json
{
  "maxQueueSize": 100,
  "maxWaitTimeSec": 300,
  "idleTimeoutSec": 600,
  "maxBatchBeforeSwitch": 10
}
```

### 推理引擎管理

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/binaries/status` | 推理引擎安装状态 |
| `POST` | `/api/binaries/llama-server/download` | 下载 llama-server（SSE 进度） |
| `POST` | `/api/binaries/whisper-server/download` | 下载 whisper-server（SSE 进度） |

Python 运行时（PaddleOCR / faster-whisper）由 Electron 集成方打包，daemon 通过 `PYTHON_PATH` 环境变量定位 Python 解释器。

### 客户端生命周期

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/clients/register` | 注册客户端（`{"id":"clipiq","name":"ClipIQ","pid":12345}`） |
| `POST` | `/api/clients/deregister` | 注销客户端 |
| `POST` | `/api/clients/heartbeat` | 心跳续期（可选） |
| `GET` | `/api/clients` | 当前在线客户端列表 |

**生命周期规则**：

- **注册时必须带 `pid`** — daemon 通过 PID 存活检测判断连接状态，电脑休眠再唤醒不会误判
- **引用计数** — daemon 只在所有客户端都离开后自动关闭（30 秒 grace period）
- **崩溃容错** — 应用崩溃后 daemon 每 30 秒检测 PID，自动清理死亡客户端
- **不注册也能用** — 不调 `register` 则 daemon 不会自动关闭，适合手动调试

**空闲资源释放**：推理子进程在无请求时自动卸载（默认 10 分钟），新请求到来时自动重新加载。daemon 本身始终保持运行。

## 鉴权

daemon 启动时自动生成随机 token，写入 `.daemon.token` 文件。请求方式：`Authorization: Bearer <token>` 或 `?token=<token>`。

### Electron 集成示例

```javascript
const { spawn } = require("child_process");
const http = require("http");

// 1. 拉起 daemon（自动复用已有实例）
const child = spawn("ai-model-daemon", ["serve"]);
let daemonInfo;

child.stdout.on("data", (chunk) => {
  daemonInfo = JSON.parse(chunk.toString());

  // 2. 注册客户端
  daemonRequest("POST", "/api/clients/register", {
    id: "my-app", name: "My App", pid: process.pid
  });
});

// 3. 退出时注销
process.on("exit", () => {
  daemonRequest("POST", "/api/clients/deregister", { id: "my-app" });
});

// 4. 使用推理 API
async function chat(message) {
  return daemonRequest("POST", "/v1/chat/completions", {
    model: "qwen3_5_4b_q4km",
    messages: [{ role: "user", content: message }]
  });
}

// 5. 使用 OCR
async function ocr(imageBuffer) {
  return new Promise((resolve, reject) => {
    const req = http.request({
      socketPath: daemonInfo.socket,
      path: "/v1/ocr?model=ppocr-v4-mobile",
      method: "POST",
      headers: {
        "Authorization": "Bearer " + daemonInfo.token,
        "Content-Type": "application/octet-stream"
      }
    }, (res) => {
      let data = "";
      res.on("data", (c) => data += c);
      res.on("end", () => resolve(JSON.parse(data)));
    });
    req.on("error", reject);
    req.write(imageBuffer);
    req.end();
  });
}

function daemonRequest(method, path, body) {
  return new Promise((resolve, reject) => {
    const req = http.request({
      socketPath: daemonInfo.socket, path, method,
      headers: {
        "Authorization": "Bearer " + daemonInfo.token,
        "Content-Type": "application/json"
      }
    }, (res) => {
      let data = "";
      res.on("data", (c) => data += c);
      res.on("end", () => resolve(JSON.parse(data)));
    });
    req.on("error", reject);
    if (body) req.write(JSON.stringify(body));
    req.end();
  });
}
```

### Python 依赖打包

Electron 集成方需要打包以下 Python 依赖：

```bash
# PaddleOCR
pip install paddleocr paddlepaddle

# faster-whisper
pip install faster-whisper
```

设置 `PYTHON_PATH` 环境变量指向打包的 Python 解释器，daemon 会自动使用。

## 存储路径

| 平台 | 路径 |
|------|------|
| macOS | `~/Library/Application Support/AIModels/` |
| Linux | `~/.local/share/AIModels/` |
| Windows | `%LOCALAPPDATA%/AIModels/` |

```
AIModels/
├── .daemon.sock                  # Unix socket
├── .daemon.pid                   # PID 文件
├── .daemon.token                 # 鉴权 token
├── .bin/                         # 推理引擎 + Python 脚本
│   ├── llama-server
│   ├── whisper-server
│   ├── ocr_server.py
│   └── faster_whisper_server.py
├── qwen3_5_4b_q4km/              # LLM 模型
│   ├── Qwen3.5-4B-Q4_K_M.gguf
│   └── mmproj-F16.gguf
├── whisper-large-v3-turbo/       # Whisper 模型
│   └── ggml-large-v3-turbo.bin
├── ppocr-v4-mobile/              # PaddleOCR 模型
│   ├── ch_PP-OCRv4_det_infer.tar
│   ├── ch_PP-OCRv4_det_infer/    # 自动解压
│   ├── ch_PP-OCRv4_rec_infer.tar
│   ├── ch_PP-OCRv4_rec_infer/
│   ├── ch_ppocr_mobile_v2.0_cls_infer.tar
│   └── ch_ppocr_mobile_v2.0_cls_infer/
├── faster-whisper-large-v3/      # faster-whisper 模型
│   ├── model.bin
│   ├── config.json
│   ├── tokenizer.json
│   └── vocabulary.json
└── ...
```

## 适配度计算

### 内存估算

```
总占用 = (模型权重大小 + ctx × KV bytes/token) × 1.05
```

### Fit 等级

以 GPU 可用内存（Apple Silicon: 总内存 × 67%）为分母：

| 占用比 | 等级 | 含义 |
|--------|------|------|
| < 50% | `perfect` | 推荐，富余充足 |
| 50–75% | `good` | 可用，有余量 |
| 75–95% | `marginal` | 紧张，可能卡顿 |
| ≥ 95% | `tight` | 不可用，内存不足 |

### TPS 估算

```
TPS = 平台常数 / 参数量(B) × 量化速度系数
```

| 平台 | 常数 |   | 量化档 | 速度系数 |
|------|------|---|--------|----------|
| Metal (Apple Silicon) | 160 |   | Q2_K | 1.25 |
| CUDA (NVIDIA) | 220 |   | Q3_K_M | 1.15 |
| ROCm (AMD) | 180 |   | Q4_K_M | 1.00 |
| CPU | 80 |   | Q5_K_M | 0.85 |
| | |   | Q6_K | 0.75 |
| | |   | Q8_0 | 0.60 |

## 运行时架构

```
┌─────────────────────────────────────────────────┐
│              ai-model-daemon (Go)                │
│                                                  │
│  RuntimeManager (map[string]Runtime)             │
│  ├── "llm"            → LLMRuntime               │
│  ├── "whisper"        → WhisperRuntime           │
│  ├── "faster-whisper" → FasterWhisperRuntime     │
│  └── "ocr"            → OCRRuntime               │
│                                                  │
│  Scheduler (per-kind queues + model affinity)    │
│                                                  │
│  Proxy (OpenAI-compat + /v1/ocr)                 │
└───────┬──────────┬──────────┬──────────┬─────────┘
        │          │          │          │
   llama-server  whisper   python3    python3
   (C++ binary)  -server   faster_    ocr_
                 (C++)     whisper_   server.py
                           server.py
```

每种运行时都遵循相同的子进程契约：spawn → health check → proxy → graceful stop → crash recovery。

## 与 Open WebUI 集成

daemon 的 OpenAI 兼容 API 可以直接对接 [Open WebUI](https://github.com/open-webui/open-webui)：

```bash
# 启动 daemon
./ai-model-daemon serve --http :9090

# 启动 Open WebUI
docker run -d -p 3000:8080 \
  -e OPENAI_API_BASE_URL=http://host.docker.internal:9090/v1 \
  -e OPENAI_API_KEY=$(cat ~/Library/Application\ Support/AIModels/.daemon.token) \
  ghcr.io/open-webui/open-webui:main
```

## 项目结构

```
main.go                              CLI 入口 + 进程复用检测
pkg/
  manifest/manifest.go               模型注册表（LLM + Whisper + ONNX + OCR + faster-whisper）
  storage/storage.go                  共享模型存储（含 tar 目录检测）
  download/
    download.go                      断点续传下载（EMA 平滑速度）
    extract.go                       tar/tar.gz 解压（保留目录结构）
  hardware/                          硬件检测（跨平台）
  fit/fit.go                         fit 计算 / TPS 估算 / 排序
  runtime/
    iface.go                         Runtime 接口定义
    process.go                       子进程管理（spawn / health check / stop / crash recovery）
    binary.go                        推理引擎下载/发现
    embed.go                         go:embed 资源嵌入
    llm.go                           LLM 运行时（llama-server）
    whisper.go                       Whisper 运行时（whisper-server）
    faster_whisper.go                faster-whisper 运行时（Python）
    ocr.go                           PaddleOCR 运行时（Python）
    runtime.go                       RuntimeManager 插件注册表
    resources/                       嵌入资源
      ocr_server.py                  PaddleOCR HTTP wrapper
      faster_whisper_server.py       faster-whisper HTTP wrapper
  queue/queue.go                     请求队列 + model-affinity 调度
  proxy/proxy.go                     OpenAI 兼容代理 + OCR 端点
  clients/tracker.go                 客户端引用计数
internal/
  server/server.go                   HTTP API（Unix socket + TCP）
  webui/                             嵌入式中文 Web UI
```
