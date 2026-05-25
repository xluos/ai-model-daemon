# ai-model-daemon

Shared AI model management daemon. Multiple desktop apps (flashcull, clipiq) connect via Unix socket to download, manage, and locate model files stored once on disk.

## Build

```bash
go build -o ai-model-daemon .
```

## Usage

```bash
# Start background daemon (HTTP API over Unix socket)
ai-model-daemon serve

# Check daemon status
ai-model-daemon status

# List all models with readiness
ai-model-daemon list

# Download a model (blocking, with progress)
ai-model-daemon download dinov2-small

# Get absolute path of a ready model
ai-model-daemon path dinov2-small
```

## Storage

Models are stored in `~/Library/Application Support/AIModels/` (macOS) or `%APPDATA%/AIModels/` (Windows). The daemon socket is `.daemon.sock` in the same directory.
