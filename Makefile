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
.PHONY: help build test calibrate vet lint fmt check clean

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk -F':.*?## ' '{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary into dist/
	go build -ldflags '$(LDFLAGS)' -o $(DIST)/$(BINARY) $(CMD_PKG)

test: ## Run tests with the race detector
	go test -race ./...

# Opt-in, and deliberately not part of check: it is the only target that talks
# to the live API, so it needs an API key (internal/apikey), it costs money, and
# it is the one thing CI must not run. It is what §11 asks for after a model
# update: re-run it and read both halves of what it prints -- the sweep over
# the labelled corpus and the share of ordinary source lines a threshold would
# select -- then confirm the shipped defaults still hold up against them.
calibrate: ## Score the labelled corpus against the live API (needs a key, costs money)
	go test -tags calibration -count=1 -v -timeout 20m ./internal/calibration/

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
