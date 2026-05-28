WHISPER_BIN = pkg/runtime/resources/whisper-server
UNAME_S := $(shell uname -s)

ifeq ($(UNAME_S),Darwin)
build: $(WHISPER_BIN)
	go build -o ai-model-daemon .
else
build:
	go build -o ai-model-daemon .
endif

$(WHISPER_BIN):
	OUT_DIR=pkg/runtime/resources ./scripts/build-whisper-server.sh

whisper:
	OUT_DIR=pkg/runtime/resources ./scripts/build-whisper-server.sh

whisper-clean:
	rm -f $(WHISPER_BIN)
	rm -rf /tmp/whisper-cpp-build

clean: whisper-clean
	rm -f ai-model-daemon

install: build
	cp ai-model-daemon ~/.local/bin/ai-model-daemon

.PHONY: build whisper whisper-clean clean install
