# ai-model-daemon 集成指南（面向 Electron 桌面应用）

本文档面向把 `ai-model-daemon` 作为基建层集成进 Electron 桌面应用（如 clipiq、flashcull）的开发者。内容全部对照当前源码行为，未对照到的字段/接口不会出现在本文。

---

## 1. 定位与架构

`ai-model-daemon` 不是独立分发的可执行程序，而是**桌面端 AI 能力的基建层**。它被各个 Electron 应用集成打包，多个应用**共享同一份**：

- 模型文件（统一存放在系统级共享目录，见第 6 节）
- 推理运行时（llama-server / whisper-server / Python 运行时）
- 请求队列（按模型亲和性调度，减少模型切换）

应用与 daemon 之间通过**本地 IPC 上的 HTTP** 通信：

- macOS / Linux：Unix domain socket
- Windows：本机回环 TCP（`127.0.0.1:<动态端口>`，因为不依赖 go-winio）

无论哪个平台，通信协议都是标准 HTTP，鉴权用 `Authorization: Bearer <token>`。

支持的运行时 kind（已注册）：`llm`、`whisper`、`faster-whisper`、`ocr`（PaddleOCR）、`rapidocr`（RapidOCR）。

集成方典型职责：
1. 随应用一起打包 daemon 可执行文件（及所需的 Python 运行时，若用到 Python 后端）。
2. 应用启动时拉起或复用 daemon，拿到连接信息。
3. 用本机 HTTP 客户端调用 daemon 的 API（模型下载、OpenAI 兼容推理、运行时管理等）。
4. 注册/注销客户端，让 daemon 在所有应用退出后自动关停。

---

## 2. 启动 daemon

### 命令

```bash
# 仅 IPC（Unix socket / Windows 回环 TCP）
ai-model-daemon serve

# IPC + 额外开一个固定 TCP 端口（便于浏览器/调试访问 Web UI）
ai-model-daemon serve --http :9090

# 强制起新实例（不复用已有 daemon），并开 HTTP
ai-model-daemon serve --no-reuse --http :9090
```

参数：

| 参数 | 含义 |
| --- | --- |
| `--http <addr>` | 额外监听一个 TCP 地址（如 `:9090` 或 `127.0.0.1:9090`）。该端口同样走 Bearer 鉴权，并带 CORS 头。不传则只有 IPC 监听。 |
| `--no-reuse` | 跳过复用逻辑：若检测到已有存活 daemon，会先把它停掉（Unix 发 SIGTERM 优雅关停，Windows 走 TerminateProcess），再起新实例。 |

> 注意：`--http` 的地址会被写入 `.daemon.http` 文件，复用时会回填到 ready JSON 的 `http` 字段。

### 进程复用语义

`serve` 启动时会读取 PID 文件并判断是否已有可复用的 daemon。判定逻辑（`isExistingDaemonAlive`）很严格，避免 PID 被系统复用导致误判：

1. 读 `.daemon.pid`，PID 进程必须存活（跨平台存活检测，见第 4 节）。
2. `.daemon.token` 必须非空。
3. 读 `.daemon.endpoint` 拿到拨号信息，能真正连上 IPC 且 `GET /status` 返回 200。

通过判定后：
- 默认（未加 `--no-reuse`）：再探测旧 daemon 的 `GET /version`。若版本与当前可执行文件不同，会停掉旧实例改起新实例；版本一致则**复用**，打印带 `"reused": true` 的 ready JSON 后立即退出（exit 0）。
- 加了 `--no-reuse`：直接停掉旧实例起新的。

也就是说：**集成方每次启动应用都可以无脑执行 `serve`**。如果 daemon 已在跑且版本一致，命令会秒退并回吐现有连接信息；否则会拉起新实例。

---

## 3. 发现并连接 daemon（Mac vs Windows）

### ready JSON

`serve` 在 stdout 打印**一行** JSON（ready 行），这是集成方拿连接信息的首选方式。stderr 是人类可读日志，不要解析。

新起实例时的字段：

```json
{
  "socket": "/Users/me/Library/Application Support/AIModels/.daemon.sock",
  "endpoint": "unix:/Users/me/Library/Application Support/AIModels/.daemon.sock",
  "pid": 12345,
  "token": "a1b2c3...（64 hex）",
  "version": "1.0.0",
  "http": ":9090"
}
```

复用已有实例时额外带 `"reused": true`：

```json
{
  "socket": "...",
  "endpoint": "unix:...",
  "pid": 12345,
  "token": "...",
  "version": "1.0.0",
  "reused": true,
  "http": ":9090"
}
```

字段说明：

| 字段 | 说明 |
| --- | --- |
| `endpoint` | **推荐统一消费这个字段**。拨号信息 dialSpec，形如 `unix:<绝对路径>`（Mac/Linux）或 `tcp:127.0.0.1:<port>`（Windows）。按首个 `:` 切成 `network` + `address`。 |
| `socket` | Unix socket 路径。Windows 上该路径仅是占位（实际不用它连），连接请用 `endpoint`/`http`。 |
| `http` | 仅当 `serve --http` 指定时存在，为该 TCP 地址。 |
| `token` | 鉴权令牌，所有 API（除 `/`、`/ui`）都要带 `Authorization: Bearer <token>`。 |
| `pid` | daemon 进程号。 |
| `version` | daemon 版本。 |
| `reused` | 仅复用时为 `true`；新起实例不带此字段。 |

> 跨平台一致做法：**只读 `endpoint`**，用首个冒号切 network/address，再按 `unix` 还是 `tcp` 选择拨号方式。不要硬编码 socket 路径。

### 不解析 stdout 的备选方案

ready JSON 是首选，但若你无法捕获子进程 stdout（例如 daemon 已由别的进程拉起），可以直接读共享目录里的端点文件，等价于 ready JSON 的核心字段：

- `<存储目录>/.daemon.endpoint` —— 内容就是 dialSpec（如 `tcp:127.0.0.1:51234`）。文件缺失时按老实例兼容，回退到 `unix:<.daemon.sock>`。
- `<存储目录>/.daemon.token` —— token。
- `<存储目录>/.daemon.pid` —— pid。
- `<存储目录>/.daemon.http` —— `--http` 地址（可能不存在）。

存储目录见第 6 节。

### Node / Electron 侧连接伪代码

```js
const { spawn } = require("node:child_process");
const http = require("node:http");
const readline = require("node:readline");

// 1) 拉起（或复用）daemon，解析第一行 ready JSON
function startDaemon(daemonPath) {
  return new Promise((resolve, reject) => {
    const child = spawn(daemonPath, ["serve"], {
      // 若用到 Python 后端 / 自带 GPU 二进制，在这里注入对应环境变量（见第 5 节）
      env: { ...process.env /*, PYTHON_PATH, LLAMA_SERVER_PATH, ... */ },
    });
    const rl = readline.createInterface({ input: child.stdout });
    rl.on("line", (line) => {
      let info;
      try { info = JSON.parse(line); } catch { return; } // 非 JSON 行（日志）忽略
      if (info && info.endpoint && info.token) {
        rl.close();
        resolve(info); // { endpoint, token, pid, version, http?, reused? }
      }
    });
    child.on("exit", (code) => reject(new Error(`daemon exited: ${code}`)));
  });
}

// 2) 把 endpoint 解析成 Node http.request 的拨号参数
function dialOptions(info) {
  const idx = info.endpoint.indexOf(":");
  const network = info.endpoint.slice(0, idx);
  const address = info.endpoint.slice(idx + 1);
  if (network === "unix") {
    // Mac / Linux：Unix domain socket
    return { socketPath: address };
  }
  if (network === "tcp") {
    // Windows：127.0.0.1:<port>
    const [host, port] = address.split(":");
    return { host, port: Number(port) };
  }
  throw new Error(`unknown endpoint network: ${network}`);
}

// 3) 统一的请求封装（两种 dial 都走标准 http）
function daemonRequest(info, method, path, body) {
  return new Promise((resolve, reject) => {
    const opts = {
      ...dialOptions(info),
      method,
      path,
      headers: {
        Authorization: `Bearer ${info.token}`,
        "Content-Type": "application/json",
      },
    };
    const req = http.request(opts, (res) => {
      let data = "";
      res.on("data", (c) => (data += c));
      res.on("end", () => resolve({ status: res.statusCode, body: data }));
    });
    req.on("error", reject);
    if (body) req.write(JSON.stringify(body));
    req.end();
  });
}

// 用法
const info = await startDaemon("/path/to/ai-model-daemon");
await daemonRequest(info, "GET", "/status");
```

> 注意：`socketPath` 与 `host/port` 是 Node `http.request` 互斥的两套字段，按 endpoint 的 network 二选一即可。其余请求逻辑（header、body）完全一致。

---

## 4. 客户端生命周期

daemon 用**引用计数 + PID 存活检测**决定何时自动关停：所有客户端都离开后，等待 30s 宽限期，期间无人回来则进程退出。

### 注册 / 注销

注册（**必须带 `pid`**，否则无法做存活检测，离场不会触发自动关停）：

```js
await daemonRequest(info, "POST", "/api/clients/register", {
  id: "clipiq",            // 必填，客户端唯一标识
  name: "ClipIQ",          // 可选，展示用
  pid: process.pid,        // 强烈建议带上当前 Electron 主进程 PID
});
```

注销（应用正常退出时调用）：

```js
await daemonRequest(info, "POST", "/api/clients/deregister", { id: "clipiq" });
```

返回里有 `remaining`（剩余客户端数）。当 `remaining == 0` 且曾有客户端注册过，daemon 启动 30s 宽限计时；计时结束仍为 0 则关停。

### 引用计数与自动关停

- 多个应用各自 `register`，daemon 持有全部引用。
- 任一应用 `deregister` 后，若还有其它客户端，daemon 继续运行。
- 全部离开 → 30s 宽限 → 自动 `shutdown`（清理 socket / pid / token / http / endpoint 文件后退出）。
- 推理子进程有自己的空闲超时，无请求时自动卸载，下次请求自动重载，与客户端生命周期独立。

### 跨平台进程存活检测

daemon 后台每 30s 扫一遍已注册客户端，用 PID 存活检测（而非心跳 TTL）清理已死客户端，因此**电脑休眠/唤醒不会误判**：

- macOS / Linux：`signal(0)` 探测。
- Windows：`OpenProcess(SYNCHRONIZE | PROCESS_QUERY_LIMITED_INFORMATION)` + `WaitForSingleObject`，避免对存活进程误判为已死。

也就是说，即使应用崩溃没来得及 `deregister`，daemon 也会在下一轮扫描发现该 PID 已死并清理掉它，从而正确触发自动关停。

`POST /api/clients/heartbeat`（带 `id`）是可选的，仅用于刷新 `lastHeartbeat` 展示字段，**不参与**存活判定。集成方一般不需要发心跳。

---

## 5. 推理后端依赖与集成方职责

daemon 只负责子进程管理，**真实的推理二进制和 Python 依赖由集成方打包分发**。

### 5.1 llama-server / whisper-server（C++ 后端）

daemon 解析二进制的优先级（`findBinary`）：

1. 环境变量指定路径（`LLAMA_SERVER_PATH` / `WHISPER_SERVER_PATH`），文件存在即用，source 标记 `env`。
2. 共享目录下的 `.bin/` 自动下载目录，source `local`。
3. （预留的已知安装目录，当前为空）source `discovered`。
4. 系统 `PATH` 中的同名可执行文件，source `path`。

自动下载（`POST /api/binaries/{llama-server,whisper-server}/download`，SSE 进度）的覆盖范围：

| 平台 | llama-server | whisper-server |
| --- | --- | --- |
| macOS (arm64 / x64) | 自动下载官方 release（Metal） | 走源码编译（`buildFromSource`，需要本机有 bash + 构建脚本） |
| Linux (arm64 / x64) | 自动下载官方 release（Ubuntu 构建） | 走源码编译 |
| Windows x64 | 自动下载，**仅 CPU** 版 | 自动下载 `whisper-bin-x64.zip` |
| Windows arm64 | 自动下载，**仅 CPU** 版 | 不提供 |

**关键约束（Windows GPU）**：Windows 上 llama-server 只自动下载 **CPU** 构建。若需要 CUDA / Vulkan 等 GPU 加速，集成方需自带 GPU 版 `llama-server.exe` 并设置 `LLAMA_SERVER_PATH` 指向它。`GET /api/binaries/status` 返回的 `downloadable` 字段会反映当前平台能否自动下载。

### 5.2 Python 运行时（PaddleOCR / RapidOCR / faster-whisper）

这三个后端都是 daemon spawn 一个 Python 子进程跑内置 wrapper 脚本（脚本由 `go:embed` 内置，启动时解压到共享目录的 `.bin/`）。**Python 解释器和第三方依赖（paddleocr / rapidocr / faster-whisper 等）由集成方的 Electron 打包方负责打包分发。**

daemon 找 Python 的逻辑（`findPython`）：

1. 环境变量 `PYTHON_PATH`（最高优先级，直接用作解释器路径）。
2. 否则按 `python3`、`python` 在 `PATH` 里找（Windows 会自动尝试 `.exe` 等可执行后缀）。

集成方推荐做法：把打包好的 Python 解释器路径通过 `PYTHON_PATH` 传给 daemon 进程（指向 `python` 或 Windows 上的 `python.exe`），不要依赖系统 PATH。

> 若没装好对应依赖，运行时 `Ensure` / 子进程会启动失败或健康检查超时；可通过 `GET /api/runtime/{kind}/logs` 看子进程日志排查。

### 5.3 环境变量清单

| 环境变量 | 作用 | 谁来设 |
| --- | --- | --- |
| `LLAMA_SERVER_PATH` | 指定 llama-server 可执行文件路径，优先于自动下载/PATH。Windows 想要 GPU 必设。 | 集成方（按需） |
| `WHISPER_SERVER_PATH` | 指定 whisper-server 可执行文件路径，优先于自动构建/PATH。 | 集成方（按需） |
| `PYTHON_PATH` | 指定 Python 解释器，供 OCR / RapidOCR / faster-whisper 使用。 | 集成方（用到 Python 后端时建议必设） |

这些变量在 `spawn` daemon 时通过子进程 `env` 注入即可（见第 3 节伪代码）。

---

## 6. 平台差异速查表

| 维度 | macOS | Windows |
| --- | --- | --- |
| 共享存储目录 | `~/Library/Application Support/AIModels/` | `%LOCALAPPDATA%\AIModels\` |
| IPC 方式 | Unix domain socket（`.daemon.sock`，权限 0600） | 本机回环 TCP `127.0.0.1:<动态端口>` |
| `endpoint` 形态 | `unix:<绝对路径>` | `tcp:127.0.0.1:<port>` |
| 连接发现 | 读 `endpoint`（首选）/ `socket` | 读 `endpoint`（必须，socket 路径不可用于连接） |
| llama-server 自动下载 | arm64/x64，Metal | x64/arm64，仅 CPU（GPU 需自带 + `LLAMA_SERVER_PATH`） |
| whisper-server 自动获取 | 源码编译 | x64 下载预编译；arm64 不提供 |
| Python 后端 | 集成方打包 python + 依赖，设 `PYTHON_PATH` | 集成方打包 python.exe + 依赖，设 `PYTHON_PATH` |
| 进程存活检测 | `signal(0)` | `OpenProcess` + `WaitForSingleObject` |

> Linux（参考，非主要目标）：存储目录 `~/.local/share/AIModels/`，IPC 走 Unix socket，llama-server 自动下载 Ubuntu 构建，whisper-server 源码编译。

共享目录内的关键文件：

```
<存储目录>/
  .daemon.sock        Unix socket（仅 Mac/Linux）
  .daemon.pid         daemon PID
  .daemon.token       鉴权 token
  .daemon.http        --http 地址（可能不存在）
  .daemon.endpoint    IPC 拨号信息 dialSpec
  .bin/               自动下载/编译的推理二进制 + 解压出的 Python wrapper 脚本
  <modelID>/          各模型文件（tar 包会自动解压成同名目录）
```

---

## 7. 常见问题排查

### daemon 起不来 / 立即退出

- 看 stderr 日志。`serve` 在 stdout 只输出一行 ready JSON，错误信息都在 stderr。
- 若提示已有 daemon 在跑且版本不同，属正常行为（会停旧起新）。想强制新实例用 `--no-reuse`。
- 确认共享存储目录可写（`EnsureDir` 失败会直接退出）。

### `serve` 秒退且打印 `"reused": true`

这是**预期行为**：已有同版本 daemon 在运行，命令复用它并回吐连接信息。直接用 ready JSON 里的 `endpoint` + `token` 连接即可，不要重复拉起。

### 连接被拒 / 连不上

- 优先用 `endpoint` 字段拨号，不要硬编码 socket 路径（Windows 上 socket 路径连不通）。
- 检查 `Authorization: Bearer <token>` 是否带对。token 不匹配返回 `401 {"error":"unauthorized"}`。`/` 和 `/ui` 两个路径免鉴权（Web UI 入口）。
- Windows 端口是**动态**的，每次新实例可能变化，务必从 `endpoint` / `.daemon.endpoint` 实时读取，不要缓存上次端口。
- 若读到的是老实例残留文件，复用探测会因 `/status` 连不通而判定不可复用，自动起新实例并改写 endpoint 文件。

### `--http` 端口被占用

- daemon 主 IPC 监听与 `--http` 是独立的两个监听器。`--http` 端口被占用会在 stderr 打印 `http server error`，但 IPC 监听仍正常工作。
- 换一个 `--http` 端口，或者干脆不开 `--http`，集成方只用 IPC 即可（`--http` 主要为浏览器访问 Web UI 服务）。

### 模型未就绪 / 推理报错 model not ready

- 用 `GET /models` 或 `GET /models/{id}` 看 `ready` 字段；未就绪先 `POST /models/{id}/download`（SSE 进度，tar 包会自动解压）。
- OpenAI 兼容接口（`/v1/chat/completions` 等）会通过队列自动加载模型；但模型文件本身没下载好仍会失败。
- OCR 模型需要 det + rec 角色都就绪（cls 可选），否则 `Ensure` 报 detection/recognition not ready。

### 推理二进制找不到

- `GET /api/binaries/status` 查看 `available` / `source` / `downloadable`。
- `downloadable: true` 时可 `POST /api/binaries/{kind}/download` 自动安装；`false`（如 Windows arm64 的 whisper-server）需集成方自带并设对应 `*_PATH`。

### Python 子进程起不来（OCR / faster-whisper）

- 确认 `PYTHON_PATH` 指向的解释器存在且能跑，且已装好对应第三方包。
- `GET /api/runtime/{kind}/logs` 看子进程 stdout/stderr（环形缓冲，最近若干行）。
- 健康检查默认有超时（OCR 为 120s），依赖加载太慢或缺包都会超时失败。
