# CLAUDE.md

Conventions for AI agents working in this repository. This file holds only what
an agent needs and cannot get elsewhere; anything already stated by `make help`,
the Makefile, the config files or (once it exists) the README is referenced from
here, not copied.

## What this is

`jevgrep` is a single static Go binary that greps lines by meaning: it scores
each line with a probability from a remote model instead of matching a pattern.
Its command line is meant to be compatible with grep's.

Current state: searching works, and so does most of grep's command line around
it. `jevgrep "a disk error" app.log` scores the file's lines against the meaning
and prints the ones that match; input can also come from a pipe, from several
PATHs, or from a whole tree with `-r`. On top of that: the expression
(`-e` / `--and` / `--not` / `-v` / `-t`), which files a walk searches
(`-g` / `--hidden` / `--no-ignore`), grep's output modes and decorations
(`-l` / `-L` / `-c` / `-q` / `-m`, `-A` / `-B` / `-C`, `-n` / `-H` / `-h` /
`-Z` / `--color`), jevgrep's own two (`-p` for the score, `--json` for NDJSON),
plus `--model`, `--login` to store an API key, `-V` and `--help`. The full list
is `cli.Options`; read it there rather than here.

Two traits of the walk are jevgrep's rather than grep's, and are load-bearing:
`.git/` and credential-shaped files are never searched by a walk — no option
reopens them — and binary content is skipped even when the path was named
explicitly. Every line searched is billed, and a credential sent to a remote API
cannot be recalled.

What still does not exist: no cache and no `--dry-run` / `--stats`. Do not
document or reference behavior that does not exist.

## Layout

```
cmd/jevgrep/         entry point; only os.Exit(cli.Run(...))
internal/cli/        command line: option table, parsing, wiring, exit codes
internal/buildinfo/  version string of the running binary
internal/apikey/     where the API key comes from, --login, terminal checks
internal/jev/        Jev API client: chunking, retry backoff, error kinds
internal/input/      reading lines from a file or stdin
internal/walk/       expanding a directory into the files worth searching
internal/output/     writing what was found: lines, context, counts, JSON
internal/search/     expression evaluation, concurrent scoring, ordered verdicts
```

New packages go under `internal/`. Create one when there is code to put in it —
no placeholder packages.

## Commands and toolchain

- `make help` lists every target. `make check` is the gate CI runs; it must be
  green before you report a task done.
- Tool versions come from `mise.toml` (`mise install`). Note that Go is pinned
  there to the module's *minimum* supported version, deliberately — read the
  comment before bumping it.
- Add a command → add a Makefile target. Add or bump a tool → edit `mise.toml`.
  **Never write a build, test or lint command, or a version number, into
  `.github/workflows/ci.yml`**; it runs `make check` and only sets up what the
  runner cannot provide itself, and that is what keeps local and CI results from
  drifting apart.

## Working agreements

- **Work directly on `master`. Do not create a branch** unless you were
  explicitly asked to.
- New behavior comes with tests next to the code it lives in.
- Do not reformat, restructure or "improve" files outside the task you were
  given.
- Deleting code and documentation that nothing uses is part of finishing a task,
  not a separate cleanup someone else will do.

## Writing code and docs

- DRY, and specifically: every fact has exactly one home. Tool versions live in
  `mise.toml`, commands in the `Makefile`, lint rules in `.golangci.yml`,
  user-facing behavior in the README. If you need one of them somewhere else,
  reference it.
- Comments explain **why**, not what. A comment that restates the code is
  deleted; a non-obvious decision without a recorded reason is what actually
  costs the next reader.
- `golang.org/x/term` is the module's only dependency, and the reason it earned
  that place is recorded where it is imported. Adding another is a deliberate
  decision that needs its reason recorded the same way — the stdlib is the
  default.
- The README is the single source of truth for user-facing behavior, and is
  written in English. Do not start a second document that explains the same
  thing.

## Internal contracts worth knowing before you touch them

Each of these has its reasoning recorded at the definition site — read there
rather than assuming.

- `cli.Run(args []string, stdout, stderr io.Writer) int` — all output goes
  through the passed writers and the exit code is the return value, so tests
  never touch the real stdout/stderr or spawn a subprocess. Keep this shape.
- Exit codes follow grep and are named: use `cli.ExitMatch` / `ExitNoMatch` /
  `ExitError` / `ExitInterrupt`, never a bare number.
- `cli.Options` is the one place every option's name, argument and summary
  lives: `--help` is rendered from it, and the README's option table has to be
  rendered from it as well. Change an option there, not in prose. An option
  whose argument may only be attached with `=` is marked `ArgOptional`.
- `input.Line` keeps the line twice on purpose: `Text` is the bytes as they were
  read and is what gets printed, while `Query()` returns the cleaned, truncated
  text sent to the API. Nothing done for the API may reach the output.
- `jev` splits its failures in two, told apart with `errors.As`:
  `*jev.AuthError` is terminal and stops the whole run, `*jev.APIError` costs
  one batch and the search carries on. Neither carries the server's message —
  that is where the scored lines would come back at us.
- `search.Run` reports *every* line, exactly once, in input order however the
  batches finish, and calls neither `Emit` nor `Fail` once it has returned. A
  line arrives with a `Verdict` and its scores, and nil scores mean it has none
  — never sent, or its batch failed — which is a different thing from its
  verdict. The return value maps straight onto the exit code.
- The version variable lives in `internal/buildinfo`, not `main`, because
  release tooling stamps it via ldflags and needs a symbol path that survives
  restructuring of `cmd/`.
- `output.Printer` owns the whole shape of what reaches stdout — line formats,
  the context window and its `--` separators, per-file names and counts, the
  NDJSON records — which is why `Print` takes every line and not only the
  selected ones. It does not buffer, so the writer handed to it must not either:
  wrapping stdout in a `bufio.Writer` turns streaming results into one burst at
  the end, and no test will notice. The only thing it holds back is the lines
  `-B` may still have to print.

## `.pockode/`

Planning and session state for the tool that drives this repo, above all
`.pockode/PLAN.md`. **It is internal: do not publish it, and do not quote or
paraphrase it into the README, release notes, commit messages or any other
public artifact.** `.gitignore` already keeps it out of the repository apart
from one deliberately tracked file; leave that arrangement alone.
