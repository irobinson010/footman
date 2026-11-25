VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
CROSS   := windows/amd64 windows/arm64 darwin/arm64 linux/arm64

# The native build (ADR-0027): whisper.cpp from scripts/native/whispercpp.sh
# with the backends in ACCELS, plus sherpa-onnx's prebuilt libraries.
ACCELS       ?= cpu vulkan
CPU_VARIANTS ?= all
NATIVE_ENV    = ACCELS="$(ACCELS)" CPU_VARIANTS="$(CPU_VARIANTS)"

.PHONY: build test lint cross native native-libs test-native
build:
	mkdir -p bin
	go build -ldflags '$(LDFLAGS)' -o bin/footman ./cmd/footman

test:
	go test ./...

lint:
	go vet ./...
	golangci-lint run

cross:
	@for t in $(CROSS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags '$(LDFLAGS)' -o bin/footman-$$os-$$arch$$ext ./cmd/footman || exit 1; \
	done

native-libs:
	$(NATIVE_ENV) scripts/native/whispercpp.sh

# bin/footman with the real speech engines. Runs from the source tree: the
# library paths are baked in as rpaths.
native: native-libs
	mkdir -p bin
	eval "$$($(NATIVE_ENV) scripts/native/whispercpp.sh env)" && \
		go build -tags native -ldflags '$(LDFLAGS)' -o bin/footman ./cmd/footman

# The native engine tests; set FOOTMAN_TEST_MODELS to a models folder.
test-native: native-libs
	eval "$$($(NATIVE_ENV) scripts/native/whispercpp.sh env)" && \
		go test -tags native ./internal/speech/... ./internal/api/... ./cmd/...
