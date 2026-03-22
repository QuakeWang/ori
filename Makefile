VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildDate=$(DATE)

.PHONY: build dev test test-ci test-race test-race-cover clean

build:
	go build -ldflags "$(LDFLAGS)" -o ori ./cmd/ori

dev:
	go build -o ori ./cmd/ori

test:
	go test ./...

test-ci:
	go test -count=1 ./...

test-race:
	go test -race -count=1 ./...

test-race-cover:
	go test -race -coverprofile=coverage.out -count=1 ./...

clean:
	rm -f ori
