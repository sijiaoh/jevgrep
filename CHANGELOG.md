# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the version is 0.x, command line options and the `--json` fields may
change in a minor release.

## [0.1.0] - 2026-09-20

First release.

### Added

- Search a file, a pipe, several paths, or a whole tree with `-r`, by meaning
  rather than by pattern: each line is scored by a remote model and the ones
  that match are printed.
- Meanings can be combined into an expression with `-e`, `--and` and `--not`,
  inverted with `-v`, and the match threshold set with `-t`.
- grep's output modes and decorations: `-l`, `-L`, `-c`, `-q`, `-m`, `-A`,
  `-B`, `-C`, `-n`, `-H`, `-h`, `-Z` and `--color`, with grep's exit codes.
- Two output modes of jevgrep's own: `-p` prints each line's score, and
  `--json` prints one NDJSON record per line of output.
- Control over which files a walk searches: `-g`, `--hidden` and `--no-ignore`,
  with `.gitignore` obeyed by default.
- `.git/`, credential-shaped files and `.ssh` directories are never searched by
  a walk, and no option reopens them. Binary content is skipped even when the
  path was named explicitly.
- Scores are cached on disk between runs, so the same search twice sends
  nothing the second time. The cache stores a hash, a probability and a day —
  no line, meaning or path — and `--no-cache` turns it off.
- `--dry-run` prices a search without sending any of it and without needing an
  API key; `--stats` reports on stderr what a run actually cost, however the
  run ended.
- `--login` stores an API key for later runs; `TYPESAFE_API_KEY` overrides it.
- `--model` selects the model that scores the lines.
- `-V` and `--help`.
- Prebuilt binaries for Linux, macOS and Windows on amd64 and arm64, with
  checksums and a build-provenance attestation.
- An `install.sh` that downloads the right archive for the machine, checks it
  against the release's checksums and installs the binary.

[0.1.0]: https://github.com/sijiaoh/jevgrep/releases/tag/v0.1.0
