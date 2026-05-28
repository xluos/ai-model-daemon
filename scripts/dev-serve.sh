#!/usr/bin/env bash
# 本地开发用：确保 Python 虚拟环境就绪，然后启动 daemon。
# 用法: ./scripts/dev-serve.sh [额外参数传给 serve，如 --http :8080]
set -euo pipefail

VENV_DIR="$HOME/.ai-model-daemon-venv"
PYTHON_BIN="python3.12"
STAMP="$VENV_DIR/.deps-installed"
REQUIRED_PKGS=(paddleocr paddlepaddle faster-whisper)

# --- 找 Python 3.12 ---
if ! command -v "$PYTHON_BIN" &>/dev/null; then
  if [ -x "/opt/homebrew/bin/$PYTHON_BIN" ]; then
    PYTHON_BIN="/opt/homebrew/bin/$PYTHON_BIN"
  else
    echo "❌ 找不到 python3.12，请先: brew install python@3.12"
    exit 1
  fi
fi

# --- 创建虚拟环境 + 安装依赖（仅首次） ---
if [ ! -f "$STAMP" ]; then
  if [ ! -d "$VENV_DIR" ]; then
    echo "📦 创建虚拟环境: $VENV_DIR"
    "$PYTHON_BIN" -m venv "$VENV_DIR"
  fi
  echo "📦 安装依赖: ${REQUIRED_PKGS[*]}"
  "$VENV_DIR/bin/pip" install "${REQUIRED_PKGS[@]}"
  date > "$STAMP"
fi

# --- 启动 daemon ---
export PYTHON_PATH="$VENV_DIR/bin/python"
echo "🚀 PYTHON_PATH=$PYTHON_PATH"
exec go run . serve --no-reuse --http :9090 "$@"
