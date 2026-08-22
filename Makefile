# Makefile for mysql-rpm-builder.
#
# The default target formats, vets, lints and then builds the binary, so a
# plain `make` both tidies the sources and verifies things are good.

BINARY  := mysql-rpm-builder
PKG     := ./go/cmd
VERSION := $(shell sed -n 's/.*Version = "\(.*\)"/\1/p' go/version/version.go)

.DEFAULT_GOAL := all

## all: fmt, vet, lint then build (default)
.PHONY: all
all: fmt vet lint test build

## fmt: format all Go sources in place
.PHONY: fmt
fmt:
	gofmt -w -s ./go

## vet: run go vet over all packages
.PHONY: vet
vet:
	go vet ./...

## lint: run golangci-lint (config in .golangci.yml)
.PHONY: lint
lint: lint-tools
	golangci-lint run ./...

# Where golangci-lint currently lives (so an update lands in the same place),
# falling back to the default `go install` location if it's not on PATH yet.
LINT_BIN_DIR := $(shell dirname "$$(command -v golangci-lint 2>/dev/null)" 2>/dev/null || go env GOPATH)/bin
LINT_VERSION_CACHE := .golangci-lint-latest

## lint-tools: ensure golangci-lint is installed and reasonably current.
## Checks the latest upstream release at most once per 24h (cached in
## .golangci-lint-latest, gitignored) to avoid a GitHub API call on every run.
.PHONY: lint-tools
lint-tools:
	@if [ ! -s $(LINT_VERSION_CACHE) ] || [ -z "$$(find $(LINT_VERSION_CACHE) -mmin -1440 2>/dev/null)" ]; then \
		latest=$$(curl -sSfL https://api.github.com/repos/golangci/golangci-lint/releases/latest 2>/dev/null \
			| sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p'); \
		[ -n "$$latest" ] && echo "$$latest" > $(LINT_VERSION_CACHE) || true; \
	fi; \
	latest=$$(cat $(LINT_VERSION_CACHE) 2>/dev/null); \
	installed=$$(golangci-lint --version 2>/dev/null | sed -n 's/.*version \([0-9.]*\).*/v\1/p'); \
	if [ -z "$$installed" ]; then \
		echo "golangci-lint not found, installing $${latest:-latest} into $(LINT_BIN_DIR)"; \
		GOBIN=$(LINT_BIN_DIR) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$${latest:-latest}; \
	elif [ -n "$$latest" ] && [ "$$installed" != "$$latest" ]; then \
		echo "golangci-lint $$installed is behind latest $$latest, updating in $(LINT_BIN_DIR)"; \
		GOBIN=$(LINT_BIN_DIR) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$$latest; \
	fi

## build: compile the static binary
.PHONY: build
build: $(BINARY)

# CGO_ENABLED=0 makes this a truly static binary (pure-Go os/user + net), with
# no glibc version dependency, so the one binary runs in every container
# regardless of the OS's glibc — e.g. el8 (glibc 2.28) / el7, not just the newer
# glibc on the build host and el9/el10.
#
# Required: ubuntu 26.04: sudo apt install musl-tools for bulding ol8 8.4.7
#
$(BINARY): go.mod go.sum $(wildcard go/*/*.go)
#   This works if there are no glibc issues
#	CGO_ENABLED=0 go build -o $(BINARY) $(PKG)
#	These 2 attempts failed
#	CGO_ENABLED=0 go build -ldflags "-s -w" -o $(BINARY) $(PKG)
#	CGO_ENABLED=0 go build -ldflags "-extldflags '-static'" -o $(BINARY) $(PKG)
	CGO_ENABLED=1 CC=musl-gcc go build -ldflags "-linkmode external -extldflags '-static'" -o $(BINARY) $(PKG)

## test: run the Go test suite (none yet, but wired up)
.PHONY: test
test:
	go test ./...

## tidy: prune and verify go.mod / go.sum
.PHONY: tidy
tidy:
	go mod tidy

## clean: remove the built binary
.PHONY: clean
clean:
	rm -f $(BINARY)

## version: print the embedded builder version
.PHONY: version
version:
	@echo $(VERSION)

## help: list available targets
.PHONY: help
help:
	@sed -n 's/^## //p' $(MAKEFILE_LIST)
