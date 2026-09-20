# Single source of truth for this project's commands: CI and CLAUDE.md invoke
# these targets rather than restating the underlying command lines.

BINARY      := jevgrep
CMD_PKG     := ./cmd/jevgrep
DIST        := dist
SITE        := site
VERSION_PKG := github.com/sijiaoh/jevgrep/internal/buildinfo

# Overridable: `make build VERSION=v0.1.0`. Left empty outside a git checkout, in
# which case no -X is passed and the binary falls back to the version Go records
# in the build info (same path `go install` takes).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS := $(if $(VERSION),-X $(VERSION_PKG).version=$(VERSION))

.DEFAULT_GOAL := help
.PHONY: help build test calibrate demo install-test vet lint fmt shellcheck \
	readme readme-check goreleaser-check release-snapshot release site check clean

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk -F':.*?## ' '{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

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

# Opt-in for the same reasons calibrate is -- a key, the live API, real money --
# and run before a release rather than on every push, which is where CONTRIBUTING
# asks for it. It reruns the commands the README pins output for and fails on any
# difference, because output printed in a README is a promise that nothing else
# would ever check. It needs the built binary rather than `go run`: the demos are
# run with XDG_CACHE_HOME pinned to an empty directory, and that is also where Go
# keeps its build cache.
demo: build ## Rerun the README's pinned demos and diff their output (needs a key, costs money)
	go run ./internal/demo -bin $(DIST)/$(BINARY)

# The README's option table is rendered from cli.Options, so that an option's
# name and summary have one home and --help and the README cannot disagree.
# install.sh is POSIX sh and is piped into whatever /bin/sh a user has, which is
# exactly the setting where a bashism fails on someone else's machine and never
# on ours. shellcheck is pinned in mise.toml for Linux and macOS only, and there
# is no Windows archive to install anyway, so there the check has nothing to say.
shellcheck: ## Lint install.sh
	@case "$$(uname -s)" in \
	MINGW* | MSYS* | CYGWIN*) \
		echo 'skipping shellcheck: install.sh is not a Windows artifact' ;; \
	*) \
		echo 'shellcheck install.sh'; shellcheck install.sh ;; \
	esac

readme: ## Regenerate the option table in README.md from cli.Options
	go run ./internal/readme

readme-check: ## Fail if README.md's option table has drifted from cli.Options
	go run ./internal/readme -check

# The release archives are built by .goreleaser.yml; these are the only places
# goreleaser is invoked, so the flags it is run with have one home too. Note
# that --clean empties dist/ first: a snapshot also takes the `make build`
# binary with it.
goreleaser-check: ## Validate .goreleaser.yml
	goreleaser check

release-snapshot: ## Build the release archives locally, publishing nothing
	goreleaser release --snapshot --clean

# Run by .github/workflows/release.yml on a v* tag, and needs the GITHUB_TOKEN
# it puts in the environment; running it by hand would publish a real release.
release: ## Publish the release for the current tag (CI)
	goreleaser release --clean

# Everything the Pages site is made of, staged where jekyll-build-pages can see
# it: the README as the home page, and install.sh so that it has a short address
# to be curl'd from. install.sh is optional here on purpose -- the site is
# publishable before it lands, and picks it up on the push that adds it.
#
# The README is wrapped in {% raw %}: Jekyll would otherwise read {{ ... }} and
# {% ... %} inside it as Liquid and silently swallow them, and a README that
# renders one way on GitHub and another way here is exactly the second copy of
# the documentation this site exists to avoid.
#
# baseurl is appended rather than written into _config.yml because it is not
# ours to state: GitHub serves a project site under /<repo>, the theme links its
# stylesheet relative to baseurl, and getting it wrong publishes an unstyled
# page. Outside Actions it stays empty, which is what a local preview wants.
#
# The page is titled after the binary because the theme prints a heading of its
# own above the content whenever the site's title differs from the page's, and
# the site's title is the repository name, which jekyll-github-metadata fills in
# whether we ask it to or not -- leaving them to differ puts "jevgrep" on the
# page twice, once from the theme and once from the README's own first line.
site: ## Stage the GitHub Pages site into site/
	rm -rf $(SITE)
	mkdir -p $(SITE)
	cp .github/pages/_config.yml $(SITE)/
	[ -z "$$GITHUB_REPOSITORY" ] || printf '\nbaseurl: /%s\n' "$${GITHUB_REPOSITORY#*/}" >> $(SITE)/_config.yml
	{ printf -- '---\nlayout: default\ntitle: $(BINARY)\n---\n\n{%% raw %%}\n'; cat README.md; printf -- '\n{%% endraw %%}\n'; } > $(SITE)/index.md
	[ ! -f install.sh ] || cp install.sh $(SITE)/

# The one test that is not `go test ./...`: it cross builds the whole release
# with goreleaser, which is ~15s, so it sits behind the "installsh" build tag to
# keep the inner loop fast (see internal/installsh). It is part of check all the
# same -- install.sh is the first thing a user runs, and it is the only piece of
# this project no other test touches. Nothing in it leaves localhost.
install-test: ## Install a snapshot release with install.sh, for real, and run it
	go test -tags installsh -count=1 -timeout 10m ./internal/installsh/

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint (also reports formatting drift)
	golangci-lint run ./...

fmt: ## Apply the formatters configured in .golangci.yml
	golangci-lint fmt ./...

# vet is kept alongside lint even though golangci-lint's default set already
# includes govet: vet is a gate in its own right, and it stays a safety net if
# govet is ever narrowed in .golangci.yml. It costs about a second.
check: vet lint shellcheck test install-test readme-check goreleaser-check ## Everything CI must pass

clean: ## Remove build output
	rm -rf $(DIST) $(SITE)
