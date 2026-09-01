.DEFAULT_GOAL := build

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build
build:
	go build -ldflags '$(LDFLAGS)' -o bin/lam ./cmd/lam

.PHONY: install
install:
	go install -ldflags '$(LDFLAGS)' ./cmd/lam

.PHONY: test
test:
	go test -race ./...

.PHONY: lint
lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run: gofmt -w ."; exit 1; }

.PHONY: snapshot
snapshot:
	goreleaser release --snapshot --clean

.PHONY: clean
clean:
	rm -rf bin dist
