#!/usr/bin/env bash
# 本地开发用：确保 Python 虚拟环境就绪，然后启动 daemon。
# 用法: ./scripts/dev-serve.sh [额外参数传给 serve，如 --http :8080]
set -euo pipefail

VENV_DIR="$HOME/.ai-model-daemon-venv"
PYTHON_BIN="python3.12"
REQUIRED_PKGS=(paddleocr paddlepaddle faster-whisper rapidocr onnxruntime)
DEPS_FILE="$VENV_DIR/.deps-installed"
DEPS_LOCK="$VENV_DIR/.deps-required"

# --- 找 Python 3.12 ---
if ! command -v "$PYTHON_BIN" &>/dev/null; then
  if [ -x "/opt/homebrew/bin/$PYTHON_BIN" ]; then
    PYTHON_BIN="/opt/homebrew/bin/$PYTHON_BIN"
  else
    echo "❌ 找不到 python3.12，请先: brew install python@3.12"
    exit 1
  fi
fi

# --- 创建虚拟环境 + 安装依赖（依赖列表变化时自动补装） ---
if [ ! -d "$VENV_DIR" ]; then
  echo "📦 创建虚拟环境: $VENV_DIR"
  "$PYTHON_BIN" -m venv "$VENV_DIR"
fi

required_deps="$(printf '%s\n' "${REQUIRED_PKGS[@]}")"
installed_deps=""
if [ -f "$DEPS_LOCK" ]; then
  installed_deps="$(cat "$DEPS_LOCK")"
fi

if [ "$required_deps" != "$installed_deps" ]; then
  echo "📦 安装/更新依赖: ${REQUIRED_PKGS[*]}"
  "$VENV_DIR/bin/pip" install "${REQUIRED_PKGS[@]}"
  printf '%s\n' "${REQUIRED_PKGS[@]}" > "$DEPS_LOCK"
  date > "$DEPS_FILE"
fi

# --- 启动 daemon ---
export PYTHON_PATH="$VENV_DIR/bin/python"
echo "🚀 PYTHON_PATH=$PYTHON_PATH"
exec go run . serve --no-reuse --http :9090 "$@"
