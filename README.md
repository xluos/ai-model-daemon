# ai-model-daemon

本地 AI 模型管理守护进程。多个桌面应用（clipiq、flashcull 等）通过 Unix socket 共享同一份模型文件，避免重复下载。

## 功能

- **模型存储** — 统一管理 GGUF / ONNX 模型文件，多应用共享
- **断点续传下载** — 多镜像源自动切换（hf-mirror / HuggingFace / ModelScope）
- **硬件检测** — CPU 型号、总内存、可用内存、GPU 后端（Metal / CUDA / ROCm / CPU）
- **内存预估** — 基于模型权重 + KV cache（随 context size 线性增长）估算实际内存占用
- **适配度评级** — 根据当前硬件计算 fit 等级：perfect / good / marginal / tight
- **推理速度估算** — TPS = 平台常数 / 参数量 × 量化系数
- **推荐排序** — 按适配度排序模型列表，优先展示最适合当前机器的模型

## 构建 & 安装

```bash
# 构建
go build -o ai-model-daemon .

# 安装到 PATH
go build -o ~/.local/bin/ai-model-daemon .
```

要求 Go 1.22+。

## CLI 命令

```bash
# 启动守护进程（HTTP API over Unix socket）
ai-model-daemon serve

# 查看守护进程状态
ai-model-daemon status

# 列出所有模型及下载状态
ai-model-daemon list

# 下载模型（阻塞，带进度）
ai-model-daemon download qwen3_5_4b_q4km

# 查看模型文件路径
ai-model-daemon path qwen3_5_4b_q4km

# 检测当前硬件
ai-model-daemon hardware

# 按适配度推荐模型
ai-model-daemon recommend
```

### 输出示例

#### `hardware`

```json
{
  "platform": "darwin",
  "arch": "arm64",
  "totalMemoryBytes": 38654705664,
  "availableMemoryBytes": 32212254720,
  "gpuMemoryCapBytes": 25898652794,
  "isAppleSilicon": true,
  "cpuModel": "Apple M3 Pro",
  "backend": "metal",
  "speedConstant": 160,
  "recommendedQuant": "Q5_K_M"
}
```

#### `recommend`

```json
{
  "machine": { "cpuModel": "Apple M3 Pro", "backend": "metal", "..." : "..." },
  "models": [
    { "id": "qwen3_5_0_8b_q4km", "fit": "perfect", "memPercent": 4,   "tps": 200 },
    { "id": "qwen3_5_2b_q4km",   "fit": "perfect", "memPercent": 9,   "tps": 80  },
    { "id": "qwen3_5_4b_q4km",   "fit": "perfect", "memPercent": 18,  "tps": 40  },
    { "id": "qwen3_5_9b_q4km",   "fit": "perfect", "memPercent": 43,  "tps": 18  },
    { "id": "qwen3_5_27b_q4km",  "fit": "tight",   "memPercent": 104, "tps": 6   }
  ]
}
```

## 鉴权

daemon 启动时自动生成一个随机 token，通过两种方式暴露给客户端：

1. **启动输出** — `serve` 命令的 ready JSON 包含 `token` 字段
2. **token 文件** — 写入 `.daemon.token`（权限 `0600`，仅 owner 可读），路径与 socket 同目录

所有 HTTP API 请求必须携带 `Authorization: Bearer <token>` 头，否则返回 `401 Unauthorized`。

### 客户端接入示例

**方式一：从启动输出获取（适用于父进程拉起 daemon）**

```javascript
const child = spawn("ai-model-daemon", ["serve"]);
child.stdout.on("data", (chunk) => {
  const ready = JSON.parse(chunk.toString());
  // ready.socket — Unix socket 路径
  // ready.token  — Bearer token
});
```

**方式二：从 token 文件读取（适用于独立客户端连接已运行的 daemon）**

```javascript
const token = fs.readFileSync(
  path.join(daemonStorageDir, ".daemon.token"),
  "utf-8"
).trim();
```

**发起请求**

```javascript
const http = require("node:http");
const req = http.request({
  socketPath: "/path/to/.daemon.sock",
  path: "/models/recommended?app=clipiq",
  headers: { "Authorization": "Bearer " + token },
});
```

```bash
# CLI 调试
curl --unix-socket ~/Library/Application\ Support/AIModels/.daemon.sock \
  -H "Authorization: Bearer $(cat ~/Library/Application\ Support/AIModels/.daemon.token)" \
  http://localhost/hardware
```

### 生命周期

- daemon 启动 → 生成 token → 写入 `.daemon.token` + ready JSON
- daemon 退出（SIGINT/SIGTERM）→ 删除 `.daemon.token` + `.daemon.sock` + `.daemon.pid`
- 每次重启生成新 token，旧 token 立即失效

## HTTP API

守护进程通过 Unix socket 提供 HTTP API，客户端（如 Electron 应用）通过 socket 连接调用。所有请求需携带 `Authorization: Bearer <token>` 头。

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/status` | 守护进程状态 |
| `GET` | `/models` | 模型列表（可选 `?app=clipiq` 过滤） |
| `GET` | `/models/{id}` | 单个模型状态 |
| `POST` | `/models/{id}/download` | 下载模型（SSE 流式进度） |
| `GET` | `/models/{id}/path` | 获取模型文件路径 |
| `DELETE` | `/models/{id}` | 删除模型文件 |
| `POST` | `/config` | 设置偏好（镜像源等） |
| `GET` | `/hardware` | 硬件检测信息 |
| `GET` | `/models/recommended` | 模型推荐列表（含 fit / memPercent / tps，按适配度排序） |
| `POST` | `/models/{id}/recompute-fit` | 用自定义 context size 重算 fit |

### 推荐 API 参数

`GET /models/recommended` 支持以下 query 参数：

- `app` — 按应用过滤（如 `clipiq`）
- `ctx.<modelId>` — 覆盖指定模型的 context size（如 `ctx.qwen3_5_4b_q4km=32768`）

### 重算 fit

```bash
TOKEN=$(cat ~/Library/Application\ Support/AIModels/.daemon.token)
curl --unix-socket ~/Library/Application\ Support/AIModels/.daemon.sock \
  -H "Authorization: Bearer $TOKEN" \
  -X POST http://localhost/models/qwen3_5_4b_q4km/recompute-fit \
  -d '{"contextSize": 32768}'
```

返回：

```json
{
  "fit": "perfect",
  "memPercent": 24,
  "tps": 40,
  "totalMemBytes": 5368709120,
  "weightBytes": 3407872000,
  "kvBytes": 1717567488,
  "memCapBytes": 25898652794,
  "effectiveCtx": 32768
}
```

## 存储路径

| 平台 | 路径 |
|------|------|
| macOS | `~/Library/Application Support/AIModels/` |
| Linux | `~/.local/share/AIModels/` |
| Windows | `%LOCALAPPDATA%/AIModels/` |

每个模型一个子目录（以模型 ID 命名），daemon socket 文件为 `.daemon.sock`。

## 适配度计算细节

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

| 平台 | 常数 |
|------|------|
| Metal (Apple Silicon) | 160 |
| CUDA (NVIDIA) | 220 |
| ROCm (AMD) | 180 |
| CPU | 80 |

| 量化档 | 速度系数 |
|--------|----------|
| Q2_K | 1.25 |
| Q3_K_M | 1.15 |
| Q4_K_M | 1.00 |
| Q5_K_M | 0.85 |
| Q6_K | 0.75 |
| Q8_0 | 0.60 |

## 项目结构

```
main.go                              CLI 入口
internal/
  manifest/manifest.go               模型注册表（含量化元数据）
  storage/storage.go                  共享模型文件存储
  download/download.go                断点续传下载
  hardware/
    hardware.go                       硬件检测核心逻辑
    hardware_darwin.go                macOS: sysctl 内存 + CPU
    hardware_linux.go                 Linux: /proc/cpuinfo + sysinfo
    hardware_windows.go               Windows: GlobalMemoryStatusEx + wmic
  fit/fit.go                          fit 计算 / TPS 估算 / 排序
  server/
    server.go                         HTTP API 处理器
    listen_unix.go                    Unix socket 监听
    listen_windows.go                 Windows TCP 回退
```
