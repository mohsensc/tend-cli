VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build test e2e lint record release-local clean demo

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/tend ./cmd/tend

test:
	go test ./...

e2e:
	go test ./internal/e2e -count=1 -v

lint:
	golangci-lint run ./...
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

record:
	TEND_RECORD=1 go test ./... -run Record -count=1

release-local:
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; \
		[ $$os = windows ] && ext=.exe; \
		echo "dist/tend_$${os}_$${arch}$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/tend_$${os}_$${arch}$$ext ./cmd/tend || exit 1; \
	done

clean:
	rm -rf bin dist

demo:
	go run ./examples/demo
