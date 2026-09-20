# jevgrep

[![CI](https://github.com/sijiaoh/jevgrep/actions/workflows/ci.yml/badge.svg)](https://github.com/sijiaoh/jevgrep/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/sijiaoh/jevgrep)](https://github.com/sijiaoh/jevgrep/releases/latest)

**grep lines by what they mean.**

`jevgrep` scores every line with a language model instead of matching it against a
pattern, so you can search an English log in Chinese, find *"a line that reports an
error to the user"* without guessing how it was worded, and pipe the result into
anything that already reads grep.

It is not static analysis. There are no rules, no AST, no language support to wait
for — if you want to match code *structure*, reach for semgrep; jevgrep reads a line
the way you do when you skim a log.

> Every non-blank line you search is sent to api.typesafe.ai to be scored.
> jevgrep never sends `.git/`, credential-shaped files or binaries — see [Privacy and cost](#privacy-and-cost).

```console
$ jevgrep -n "a disk or filesystem error" examples/app.log
4:2026-09-18 03:12:19 ERROR write /var/lib/pg/base/16384: input/output error
6:2026-09-18 03:13:44 WARN  smartd: 1 uncorrectable sector on /dev/nvme0n1
9:2026-09-18 03:16:33 ERROR no space left on device while flushing WAL
```

`grep ERROR` finds three lines in that file. It prints the one about a declined
payment, which is not a disk error, and it misses line 6 — a `WARN` about a disk
about to fail — which is. And the meaning does not have to be written in the same
language as the lines:

```console
$ jevgrep -n "a problem signing in or with authentication" examples/tickets.txt
3:#814 パスワードを再設定するメールが届かない
5:#816 二段階認証のコードが毎回エラーになる
```

Both commands run as they are written, against files in this repository.

## Install

```sh
curl -fsSL https://sijiaoh.github.io/jevgrep/install.sh | sh
```

It picks the archive for your platform, checks it against the release's
`checksums.txt`, installs nothing if that check fails, and tells you where the
binary went. Two environment variables change what it does:

- `JEVGREP_INSTALL_DIR` — where to install, default `$HOME/.local/bin`.
- `JEVGREP_VERSION` — install this release instead of the latest, `v0.1.0` or `0.1.0`.

With Go:

```sh
go install github.com/sijiaoh/jevgrep/cmd/jevgrep@latest
```

Or download an archive for Linux, macOS or Windows from
[Releases](https://github.com/sijiaoh/jevgrep/releases/latest) and put the binary on
your PATH — this is the way to install it on Windows.

Then, whichever way you installed it:

```sh
jevgrep --version
```

If that says the command is not found, the directory it was installed into is not on
your PATH. `install.sh` says so when it happens, and prints the line to add and the
file to add it to.

Releases also carry a GitHub build provenance attestation, which says the archive
came out of this repository's release workflow — something `checksums.txt`, published
alongside the archives, cannot tell you. The command to check it is in
[SECURITY.md](https://github.com/sijiaoh/jevgrep/blob/master/SECURITY.md).

## Quick start

The commands below search `examples/app.log` and `examples/tickets.txt`, two small
files kept in this repository so that the output shown here is reproducible. Clone it
to follow along, or point them at a log of your own — nothing depends on the fixture
except the exact lines that come back.

**1. Get a key.** jevgrep talks to the [TypeSafe](https://console.typesafe.ai/settings/keys)
API, and needs a key of your own.

<!-- demo: skip: this block prompts for a key, so `make demo` cannot rerun it -->

```console
$ jevgrep --login
jevgrep: paste a key from https://console.typesafe.ai/settings/keys (it will not be echoed)
TypeSafe API key: 
jevgrep: key saved to /home/you/.config/jevgrep/api_key
```

`export TYPESAFE_API_KEY=…` works too, and takes precedence over the stored file.

**2. Price it before you spend it.** `--dry-run` sends nothing at all, and does not
even need a key:

```console
$ jevgrep --dry-run "a disk or filesystem error" examples/app.log
dry run: nothing was sent
  files                 1
  lines read           10
  blank                 0  never sent
  questions            10  1 meaning per line
  cached                0  already scored
  to send              10
  batches              ~1
  input tokens       ~743
  cost         ~$0.000031  at $0.042 per 1M input tokens
```

On a cache that already has these lines, `cached` and `to send` swap over and the
cost is `$0` — that is the point of the cache, not a bug in the estimate.

**3. Search a log.** `-p` prints the probability the model gave each line, which is
the number `-t` is compared against:

<!-- demo: scores: probabilities move between runs, so `make demo` allows them 0.1 -->

```console
$ jevgrep -np "a disk or filesystem error" examples/app.log
4:0.96:2026-09-18 03:12:19 ERROR write /var/lib/pg/base/16384: input/output error
6:0.80:2026-09-18 03:13:44 WARN  smartd: 1 uncorrectable sector on /dev/nvme0n1
9:0.90:2026-09-18 03:16:33 ERROR no space left on device while flushing WAL
```

> Scores come from a model that gets updated. The lines a search selects are stable;
> the exact probabilities may drift by a few hundredths.

**4. Search in another language.** The meaning and the lines never have to agree on
one:

```console
$ jevgrep -n "a problem signing in or with authentication" examples/tickets.txt
3:#814 パスワードを再設定するメールが届かない
5:#816 二段階認証のコードが毎回エラーになる
```

**5. Search a tree.** `-r` obeys `.gitignore`, and skips hidden files, binaries and
credential-shaped files. Price it first — a tree is a great many more lines than a
log file:

```console
$ jevgrep --dry-run -rn "code that retries a failed request" .
$ jevgrep -rn "code that retries a failed request" .
```

**6. Read a pipe.** Pass `-` as the PATH and jevgrep reads standard input:

```console
$ head -3 examples/app.log | jevgrep "a request that succeeded" -
2026-09-18 03:11:07 INFO  GET /healthz 200 3ms
```

**7. Run it again for free.** Scores are cached on disk, so the second run of the
same search sends nothing. `--stats` says what a run actually cost:

<!-- demo: warm: this is the second run, so `make demo` runs it twice -->

```console
$ jevgrep --stats -n "a disk or filesystem error" examples/app.log
4:2026-09-18 03:12:19 ERROR write /var/lib/pg/base/16384: input/output error
6:2026-09-18 03:13:44 WARN  smartd: 1 uncorrectable sector on /dev/nvme0n1
9:2026-09-18 03:16:33 ERROR no space left on device while flushing WAL
jevgrep: stats
  files                1
  lines read          10
  blank                0  never sent
  questions           10
  cached              10  100% of the questions
  sent                 0
  requests             0  0 batches, 0 retried
  failed               0
  input tokens         0
  cost                $0  at $0.042 per 1M input tokens
  elapsed           0.0s
  cache        /home/you/.cache/jevgrep  (delete it to clear)
```

## Privacy and cost

### What leaves your machine

Every non-blank line of every file you search, and the meaning you searched for.
A line is cleaned up before it is sent — bytes that are not UTF-8 are replaced, and
a line longer than 4 KB is truncated — but none of that reaches the output: what
jevgrep prints is the line exactly as it was read. Blank lines are never sent, and
never match.

### What never leaves it

A walk never opens `.git/`, `.ssh/`, `.env`, `.env.*`, `*.pem`, `*.key`, `*.p12`,
`*.pfx`, or an extensionless `id_*` file such as `id_rsa`. **No option reopens them**
— `-g` is a filter, not a key, and neither `--hidden` nor `--no-ignore` is consulted
before those rules have had their say. Naming such a file on the command line
yourself is the only way to search one, which is a thing you can only do on purpose.
Binary content is skipped even when the path was named explicitly.

### The cache

The cache holds a hash of each (line, meaning), the probability that came back, and
the day it arrived — no lines, no meanings, no file names. `--stats` prints the
directory it is in so that you can delete it; `--no-cache` skips it entirely for a
run. A cached answer is believed for 30 days, then asked again — the model behind
`jev-latest` moves, and nothing else would ever wash a stale answer out, so "the
second run is free" stops being true on the 31st day.

### Cost

You are billed per line searched. `--dry-run` prices a search without sending any of
it, and `--stats` reports what a run really cost, however it ended — including after
Ctrl-C.

As of 2026-09-20, `--stats` reported `$0.000036` for the 10-line log file above, and
`--dry-run` priced one meaning against this repository's own tree — 16,315 non-blank
lines across 100 files — at `~$0.0310`. The two numbers for one search do not have to
agree: `--dry-run` is jevgrep's own estimate and marks every figure it works out with
a `~`, while `--stats` prefers what the API said it charged. When they differ by a
few percent, the reported one is the one that is right.

## Options

<!-- BEGIN OPTIONS: generated from cli.Options by `make readme`; do not edit -->

| Option | Description |
| --- | --- |
| `-e, --meaning MEANING` | Add a meaning; repeat to match any of them |
| `--and MEANING` | Also require this meaning on the same line |
| `--not MEANING` | Reject the lines that have this meaning |
| `-v, --invert-match` | Select the lines that do not match |
| `-t, --threshold NUM` | Match at a score of NUM or above (default 0.5) |
| `-r, --recursive` | Search the files under each directory |
| `-g, --glob GLOB` | Search only the paths matching GLOB, or skip !GLOB |
| `--hidden` | Also search hidden files and directories |
| `--no-ignore` | Do not obey .gitignore and .ignore files |
| `-n, --line-number` | Prefix each output line with its line number |
| `-H, --with-filename` | Print the file name with each output line |
| `-h, --no-filename` | Never print the file name |
| `-l, --files-with-matches` | Print only the name of each file that matched |
| `-L, --files-without-match` | Print only the name of each file that did not match |
| `-c, --count` | Print only the number of matching lines per file |
| `-q, --quiet` | Print nothing; stop at the first match |
| `-m, --max-count NUM` | Stop after NUM matching lines per file |
| `-A, --after-context NUM` | Print NUM lines after each matching line |
| `-B, --before-context NUM` | Print NUM lines before each matching line |
| `-C, --context NUM` | Print NUM lines before and after each matching line |
| `-p, --score` | Print each line's score before its text |
| `--json` | Print one JSON object per line of output |
| `-Z, --null` | Terminate each file name with a NUL byte |
| `--color[=WHEN]` | Color output: always, never, auto (default "auto") |
| `--dry-run` | Estimate what would be sent and what it costs |
| `--stats` | Report lines, requests, cost and time on stderr |
| `--no-cache` | Do not read or write the local score cache |
| `--model NAME` | Model that scores the lines (default "jev-latest") |
| `--login` | Store an API key for later runs, then exit |
| `-V, --version` | Print the version and exit |
| `--help` | Print this help and exit |

<!-- END OPTIONS -->

`-h` is grep's `--no-filename`, not help. Ask for help with `--help`.

With no PATH, jevgrep reads standard input, or searches `.` under `-r`. With PATH as
`-`, it reads standard input. Exit status is `0` if a line matched, `1` if none did,
`2` on error — so `if jevgrep -q …; then` works the way it does with grep.

`-e` may be repeated to match any of several meanings; `--and` requires another
meaning on the same line, and `--not` rejects one. Every meaning is scored and
compared against `-t` separately, but `-p` has room for one number: it prints the
highest of the `-e` meanings' scores, not the `--and` and `--not` ones, which qualify
a match rather than being what you asked to see. `-v` does not change it either —
inverting happens to the verdict, not to what the model measured. `--json` reports
every meaning's score.

Changing `-t` costs nothing: the threshold is compared locally, against scores that
are already cached, so tightening or loosening a search you have already run sends no
requests at all.

## Combining with other tools

Print the names of the files that matched, NUL-separated, and act on them:

```sh
jevgrep -rlZ "code that writes to the database directly" . | xargs -0 ls -l
```

`--json` prints one object per line of output — `type`, `file`, `line`, `text`,
`score`, `scores`, `selected` — so that a program can read what a terminal would have
shown. `score` is absent on a line that has none, and `selected` is false on a
context line:

```sh
jevgrep -r --json "a TODO that admits something is broken" . \
  | jq -r 'select(.selected) | "\(.file):\(.line) \(.text)"'
```

Pick a result interactively, and open it:

```sh
jevgrep -rn "the place this program parses its command line" . \
  | fzf --delimiter=: --preview 'sed -n "{2}p" {1}'
```

Give an AI coding agent a way to find code by description instead of by name: point
it at `jevgrep -r --json MEANING .` as a retrieval tool, and it gets file, line, text
and score per hit without having to guess identifiers.

## How it works

Every line becomes a yes/no question — *does this line mean X?* — and the model
answers with a probability. From there it is arithmetic and plumbing:

1. Lines are batched and sent; blank lines never are.
2. Each line comes back with a probability between 0 and 1.
3. `-t` compares it **locally**, so changing your mind about the threshold is free.
4. Batches finish out of order; jevgrep prints every line in input order regardless.
5. Every score is written to the on-disk cache, so the next run asks for less.

This is why searching across languages works at all: the model is reading the
meaning, not the bytes, and a Japanese support ticket and an English meaning are the
same question to it.

The default threshold of 0.5 and the number of lines per request are measurements,
not preferences — they were picked from a labelled corpus and re-derived by
`make calibrate`. The corpus, and the reasoning about why a balanced corpus alone
cannot pick a threshold for unbalanced real input, is in
[`internal/calibration`](https://github.com/sijiaoh/jevgrep/blob/master/internal/calibration).

## Limits

- **It costs money and needs a network.** Every non-blank line searched is billed,
  and a search is as fast as the round trips it makes. There is no offline mode.
- **It needs an API key.** Anything that searches does; `--dry-run`, `--help` and
  `--version` do not.
- **It is not static analysis.** It reads one line at a time, with no knowledge of
  the file around it, so it cannot follow a call, a type or a definition. For
  structure, use semgrep or your language server.
- **Scores near the threshold are not stable**, particularly across languages. If a
  search feels wrong, run it with `-p` and look at the numbers before reaching for a
  different wording.
- **Binaries, `.git/` and credential-shaped files are not searched by a walk**, and
  no option changes that.
- **0.x:** options and `--json` fields may change in a minor release.
  [CHANGELOG.md](https://github.com/sijiaoh/jevgrep/blob/master/CHANGELOG.md) records what did.

## Contributing, security, license

- [CONTRIBUTING.md](https://github.com/sijiaoh/jevgrep/blob/master/CONTRIBUTING.md) — how to build, test and release it.
- [SECURITY.md](https://github.com/sijiaoh/jevgrep/blob/master/SECURITY.md) — reporting a vulnerability, and what jevgrep does
  and does not send.
- [CODE_OF_CONDUCT.md](https://github.com/sijiaoh/jevgrep/blob/master/CODE_OF_CONDUCT.md).
- [LICENSE](https://github.com/sijiaoh/jevgrep/blob/master/LICENSE) — MIT.
