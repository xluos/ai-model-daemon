#!/usr/bin/env bash
# 从源码编译 whisper-server 静态二进制，产物放到指定 OUT_DIR。
# macOS: Metal + Accelerate, embedded Metal shader
# Linux: OpenMP
#
# 用法:
#   OUT_DIR=/path/to/bin ./scripts/build-whisper-server.sh
#   WHISPER_VERSION=v1.8.4 OUT_DIR=... ./scripts/build-whisper-server.sh

set -euo pipefail

WHISPER_VERSION="${WHISPER_VERSION:-v1.8.4}"
BUILD_DIR="${WHISPER_BUILD_DIR:-/tmp/whisper-cpp-build}"
OUT_DIR="${OUT_DIR:?OUT_DIR is required}"

PLATFORM="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

echo "PROGRESS:checking dependencies"

for tool in cmake git; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "ERROR:missing dependency: $tool" >&2
    case "$PLATFORM" in
      darwin) echo "  install: xcode-select --install && brew install cmake" >&2 ;;
      linux)  echo "  install: apt install cmake git build-essential" >&2 ;;
    esac
    exit 1
  fi
done

echo "PROGRESS:cloning whisper.cpp $WHISPER_VERSION"

if [ ! -d "$BUILD_DIR/.git" ]; then
  git clone --depth 1 --branch "$WHISPER_VERSION" \
    https://github.com/ggml-org/whisper.cpp.git "$BUILD_DIR" 2>&1
else
  cd "$BUILD_DIR"
  git fetch --tags --depth 1 origin "$WHISPER_VERSION" 2>/dev/null || true
  git checkout -q "$WHISPER_VERSION"
  cd - >/dev/null
fi

cd "$BUILD_DIR"

CMAKE_ARGS=(
  -B build
  -DCMAKE_BUILD_TYPE=Release
  -DBUILD_SHARED_LIBS=OFF
  -DWHISPER_BUILD_EXAMPLES=ON
  -DWHISPER_BUILD_TESTS=OFF
)

if [ "$PLATFORM" = "darwin" ]; then
  CMAKE_ARGS+=(
    -DGGML_METAL=ON
    -DGGML_METAL_EMBED_LIBRARY=ON
    -DGGML_ACCELERATE=ON
    -DGGML_BLAS=OFF
  )
elif [ "$PLATFORM" = "linux" ]; then
  CMAKE_ARGS+=(
    -DGGML_OPENMP=ON
  )
fi

echo "PROGRESS:configuring cmake"
cmake "${CMAKE_ARGS[@]}" 2>&1

JOBS="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)"
echo "PROGRESS:compiling (jobs=$JOBS)"
cmake --build build -j "$JOBS" --config Release --target whisper-server 2>&1

cd - >/dev/null

echo "PROGRESS:installing"
mkdir -p "$OUT_DIR"

EXT=""
src="$BUILD_DIR/build/bin/whisper-server${EXT}"
if [ ! -f "$src" ]; then
  echo "ERROR:build product not found: $src" >&2
  ls -la "$BUILD_DIR/build/bin/" >&2 || true
  exit 1
fi

cp "$src" "$OUT_DIR/whisper-server${EXT}"
chmod +x "$OUT_DIR/whisper-server${EXT}"

if [ "$PLATFORM" = "darwin" ]; then
  xattr -dr com.apple.quarantine "$OUT_DIR/whisper-server" 2>/dev/null || true
fi

echo "PROGRESS:done"
echo "OK:$OUT_DIR/whisper-server${EXT}"
