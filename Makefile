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
lint:
	golangci-lint run ./...

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
