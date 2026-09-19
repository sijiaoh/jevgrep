# Single source of truth for this project's commands: CI and CLAUDE.md invoke
# these targets rather than restating the underlying command lines.

BINARY      := jevgrep
CMD_PKG     := ./cmd/jevgrep
DIST        := dist
VERSION_PKG := github.com/sijiaoh/jevgrep/internal/buildinfo

# Overridable: `make build VERSION=v0.1.0`. Left empty outside a git checkout, in
# which case no -X is passed and the binary falls back to the version Go records
# in the build info (same path `go install` takes).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS := $(if $(VERSION),-X $(VERSION_PKG).version=$(VERSION))

.DEFAULT_GOAL := help
.PHONY: help build test vet lint fmt check clean

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk -F':.*?## ' '{printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary into dist/
	go build -ldflags '$(LDFLAGS)' -o $(DIST)/$(BINARY) $(CMD_PKG)

test: ## Run tests with the race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint (also reports formatting drift)
	golangci-lint run ./...

fmt: ## Apply the formatters configured in .golangci.yml
	golangci-lint fmt ./...

# vet is kept alongside lint even though golangci-lint's default set already
# includes govet: vet is a gate in its own right, and it stays a safety net if
# govet is ever narrowed in .golangci.yml. It costs about a second.
check: vet lint test ## Everything CI must pass

clean: ## Remove build output
	rm -rf $(DIST)
