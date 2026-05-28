# ai-model-daemon

本地 AI 模型管理 + 推理守护进程。多个桌面应用（clipiq、flashcull 等）通过 Unix socket 共享同一份模型文件和推理运行时，避免重复下载和多实例冲突。

## 功能

- **模型存储** — 统一管理 GGUF / ONNX / Whisper 模型文件，多应用共享
- **断点续传下载** — 多镜像源自动切换（hf-mirror / HuggingFace / ModelScope）
- **硬件检测** — CPU 型号、总内存、可用内存、GPU 后端（Metal / CUDA / ROCm / CPU）
- **内存预估** — 基于模型权重 + KV cache（随 context size 线性增长）估算实际内存占用
- **适配度评级** — 根据当前硬件计算 fit 等级：perfect / good / marginal / tight
- **推理速度估算** — TPS = 平台常数 / 参数量 × 量化系数
- **推荐排序** — 按适配度排序模型列表，优先展示最适合当前机器的模型
- **推理运行时管理** — 自动启停 llama-server / whisper-server 子进程，支持模型热切换
- **请求队列** — model-affinity 调度，减少模型切换；可配并行数、空闲超时自动卸载
- **OpenAI 兼容 API** — `/v1/chat/completions`、`/v1/models`、`/v1/audio/transcriptions`
- **客户端生命周期** — 引用计数，所有客户端退出后 daemon 自动关闭
- **内置 Web UI** — 中文管理面板，嵌入二进制，零外部依赖

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
| 模型管理 | 全部/LLM/Whisper/ONNX 过滤，下载/删除/详情 |
| 推理运行时 | LLM/Whisper 启停控制、模型选择、参数配置、查看日志、推理引擎下载 |
| 请求队列 | 队列状态监控、调度配置（最大队列/空闲超时/批量切换） |
| 对话测试 | 选择模型 → 发消息 → 流式输出，支持多轮对话 |
| 接口调试 | 全部 API 端点可展开调用，填参数 → 执行 → 看 JSON 响应 |

Web UI 为纯 HTML/CSS/JS 单文件，通过 `go:embed` 嵌入二进制，不需要 npm 构建。

## HTTP API

所有请求需携带 `Authorization: Bearer <token>` 头（或 `?token=<token>` 查询参数）。

### 模型管理

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/status` | 守护进程状态 |
| `GET` | `/models` | 模型列表（可选 `?app=clipiq` 过滤） |
| `GET` | `/models/{id}` | 单个模型状态 |
| `POST` | `/models/{id}/download` | 下载模型（SSE 流式进度，可选 `?progressInterval=` 毫秒） |
| `POST` | `/models/{id}/cancel-download` | 取消正在进行的下载 |
| `GET` | `/models/{id}/path` | 获取模型文件路径 |
| `DELETE` | `/models/{id}` | 删除模型文件 |
| `POST` | `/config` | 设置偏好（镜像源等） |
| `GET` | `/hardware` | 硬件检测信息 |
| `GET` | `/models/recommended` | 模型推荐列表（含 fit / memPercent / tps，按适配度排序） |
| `POST` | `/models/{id}/recompute-fit` | 用自定义 context size 重算 fit |

### OpenAI 兼容 API

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/v1/models` | 已下载模型列表（OpenAI 格式） |
| `POST` | `/v1/chat/completions` | 对话补全（自动加载模型，支持流式） |
| `POST` | `/v1/completions` | 文本补全 |
| `POST` | `/v1/audio/transcriptions` | Whisper 语音转写（multipart） |

请求中的 `model` 字段对应模型 ID（如 `qwen3_5_4b_q4km`）。如果模型未加载，daemon 自动启动 llama-server 并加载，客户端无需手动管理推理进程。不同模型的请求会被队列调度，自动处理模型切换。

### 推理运行时管理

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/runtime/status` | 运行时状态（LLM + Whisper + 推理引擎安装情况） |
| `POST` | `/api/runtime/llm/start` | 手动启动 LLM（`{"modelId":"...","contextSize":0,"parallel":1}`） |
| `POST` | `/api/runtime/llm/stop` | 停止 LLM |
| `POST` | `/api/runtime/whisper/start` | 手动启动 Whisper（`{"modelId":"..."}`） |
| `POST` | `/api/runtime/whisper/stop` | 停止 Whisper |
| `GET` | `/api/runtime/llm/logs` | LLM 进程日志（最近 200 行） |
| `GET` | `/api/runtime/whisper/logs` | Whisper 进程日志 |

### 请求队列

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/queue/status` | 队列状态（排队数、当前模型、批次计数） |
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

llama-server 从 [llama.cpp releases](https://github.com/ggml-org/llama.cpp/releases) 自动下载。whisper-server 在 macOS/Linux 上需手动安装（`brew install whisper-cpp`），daemon 会自动发现已安装的二进制，也会搜索 clipiq 的 resources 目录并拷贝到自己的存储。

### 客户端生命周期

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/clients/register` | 注册客户端（`{"id":"clipiq","name":"ClipIQ","pid":12345}`） |
| `POST` | `/api/clients/deregister` | 注销客户端（`{"id":"clipiq"}`） |
| `POST` | `/api/clients/heartbeat` | 心跳续期（可选，非必须） |
| `GET` | `/api/clients` | 当前在线客户端列表 |

**生命周期规则**：

- **注册时必须带 `pid`** — daemon 通过检测客户端进程是否存活来判断连接状态，而非心跳 TTL。电脑休眠再唤醒不会误判断联。
- **引用计数** — 多个应用共享同一个 daemon。应用退出时调 `deregister`，daemon 只在所有已注册客户端都离开后才关闭。
- **崩溃容错** — 应用崩溃（没来得及 `deregister`），daemon 每 30 秒检测 PID 存活，发现进程死了自动清理。
- **Grace period** — 最后一个客户端离开后等 30 秒，期间有新客户端注册则取消关闭。
- **不注册也能用** — 不调 `register` 的话 daemon 永远不会自动关闭。适合手动启动 + Web UI 调试场景。

**空闲资源释放**：推理子进程（llama-server / whisper-server）在无请求时自动卸载（默认 10 分钟），新请求到来时自动重新加载。daemon 本身始终保持运行，几乎不占资源。

## 鉴权

daemon 启动时自动生成一个随机 token：

1. **启动输出** — `serve` 命令的 ready JSON 包含 `token` 字段
2. **token 文件** — 写入 `.daemon.token`（权限 `0600`），路径与 socket 同目录
3. **Web UI** — 访问根路径自动重定向到带 token 的 `/ui` 页面

请求方式：`Authorization: Bearer <token>` 头 或 `?token=<token>` 查询参数。

### 客户端接入示例

**典型流程：启动 → 注册 → 使用 → 退出注销**

```javascript
const { spawn } = require("child_process");
const http = require("http");

// 1. 拉起 daemon（自动复用已有实例）
const child = spawn("ai-model-daemon", ["serve"]);
let daemonInfo;

child.stdout.on("data", (chunk) => {
  daemonInfo = JSON.parse(chunk.toString());
  // daemonInfo.socket — Unix socket 路径
  // daemonInfo.token  — Bearer token
  // daemonInfo.reused — true 表示复用了已有 daemon

  // 2. 注册客户端（带 PID，用于崩溃检测）
  daemonRequest("POST", "/api/clients/register", {
    id: "my-app",
    name: "My App",
    pid: process.pid
  });
});

// 3. 应用退出时注销（正常退出和 SIGINT/SIGTERM）
function cleanup() {
  daemonRequest("POST", "/api/clients/deregister", { id: "my-app" });
}
process.on("exit", cleanup);
process.on("SIGINT", () => { cleanup(); process.exit(0); });
process.on("SIGTERM", () => { cleanup(); process.exit(0); });

// 4. 正常使用 API（推理请求自动管理模型加载/卸载）
async function chat(message) {
  return daemonRequest("POST", "/v1/chat/completions", {
    model: "qwen3_5_4b_q4km",
    messages: [{ role: "user", content: message }],
    stream: false
  });
}

function daemonRequest(method, path, body) {
  return new Promise((resolve, reject) => {
    const req = http.request({
      socketPath: daemonInfo.socket,
      path,
      method,
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

**多应用共享场景**：

```
应用 A 启动 → serve(复用) → register("app-a", pid=100)     → daemon 客户端: [A]
应用 B 启动 → serve(复用) → register("app-b", pid=200)     → daemon 客户端: [A, B]
应用 A 退出 → deregister("app-a")                           → daemon 客户端: [B]      ← daemon 继续运行
应用 B 退出 → deregister("app-b")                           → daemon 客户端: []        ← 30 秒后 daemon 自动关闭
```

应用崩溃时：

```
应用 A 崩溃（没来得及 deregister） → daemon 每 30 秒检测 PID 100 → 发现进程已死 → 自动清理
```

```bash
# CLI 调试（无需注册，daemon 不会自动关闭）
TOKEN=$(cat ~/Library/Application\ Support/AIModels/.daemon.token)
curl --unix-socket ~/Library/Application\ Support/AIModels/.daemon.sock \
  -H "Authorization: Bearer $TOKEN" \
  http://localhost/hardware
```

### 进程复用

多个应用调用 `ai-model-daemon serve` 不会重复启动。daemon 启动前检查 PID 文件，若已有存活实例则直接返回其连接信息（`reused: true`），调用方拿到完全相同的 socket 路径和 token。

## 存储路径

| 平台 | 路径 |
|------|------|
| macOS | `~/Library/Application Support/AIModels/` |
| Linux | `~/.local/share/AIModels/` |
| Windows | `%LOCALAPPDATA%/AIModels/` |

```
AIModels/
├── .daemon.sock              # Unix socket
├── .daemon.pid               # PID 文件
├── .daemon.token             # 鉴权 token
├── .bin/                     # 推理引擎二进制
│   ├── llama-server          # llama.cpp server
│   └── whisper-server        # whisper.cpp server
├── qwen3_5_4b_q4km/          # 模型目录（按 ID）
│   ├── Qwen3.5-4B-Q4_K_M.gguf
│   └── mmproj-F16.gguf
├── whisper-large-v3-turbo/
│   └── ggml-large-v3-turbo.bin
└── ...
```

## 适配度计算

### 内存估算

```
总占用 = (模型权重大小 + ctx × KV bytes/token) × 1.05
```

KV cache 按 Qwen3 系列经验值查表（按参数量近邻匹配），支持 manifest 级别的 `kvBytesPerToken` 覆盖。

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

## 项目结构

```
main.go                              CLI 入口 + 进程复用检测
pkg/
  manifest/manifest.go               模型注册表（LLM + Whisper + ONNX）
  storage/storage.go                  共享模型文件存储
  download/download.go                断点续传下载（EMA 平滑速度）
  hardware/                          硬件检测（跨平台）
  fit/fit.go                         fit 计算 / TPS 估算 / 排序
  runtime/
    process.go                       子进程管理（spawn/health check/stop/crash recovery）
    binary.go                        推理引擎下载/发现（llama-server, whisper-server）
    binary_{darwin,linux,windows}.go  平台特化资源选择
    llm.go                           LLM 运行时（llama-server 生命周期）
    whisper.go                       Whisper 运行时（whisper-server 生命周期）
    runtime.go                       RuntimeManager 顶层协调器
  queue/queue.go                     请求队列 + model-affinity 调度器
  proxy/proxy.go                     OpenAI 兼容反向代理
  clients/tracker.go                 客户端引用计数 + 自动关闭
internal/
  server/server.go                   HTTP API（40+ 端点）
  webui/
    embed.go                         go:embed 嵌入
    index.html                       Web UI 单文件（中文界面）
```

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
