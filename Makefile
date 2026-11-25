VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
CROSS   := windows/amd64 windows/arm64 darwin/arm64 linux/arm64

.PHONY: build test lint cross
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
