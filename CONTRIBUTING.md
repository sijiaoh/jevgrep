# Contributing to jevgrep

Thanks for wanting to help. This file covers how to get a working checkout,
what the quality gate is, and how a release is made. What jevgrep *does* is in
the [README](README.md); conventions for the code itself are in
[CLAUDE.md](CLAUDE.md).

## Getting set up

The toolchain is pinned in [`mise.toml`](mise.toml) — Go and every tool the
checks and a release need, at the exact versions CI uses. Install [mise], then:

```sh
mise install
```

Nothing else is needed: `golang.org/x/term` is the module's only direct
dependency.

[mise]: https://mise.jdx.dev

## Running the tests

Every command this project has is a `make` target, and

```sh
make help
```

prints all of them with one line each. That list is the single source of truth;
this file does not repeat it.

The one you need is `make check`. It is the whole quality gate — the same
target CI runs on Linux, macOS and Windows, with no command lines of its own in
the workflow files, so green locally and green in CI mean the same thing. Run
it before you open a pull request.

Two targets are deliberately outside it, for the same reason: they talk to the
live API, so they need an API key and spend real money. `make calibrate`
re-measures the numbers the default threshold and the batch size were chosen
from, and lives behind the `calibration` build tag. `make demo` reruns the
commands the README pins output for and is part of making a release, below.
Nothing else in the repository talks to the network from a test, and a test that
must talk to it belongs behind the `calibration` tag too.

## Making a change

- Work on a branch, open a pull request against `master`.
- New behavior comes with tests next to the code it lives in.
- User-facing behavior is documented in the README and nowhere else. The option
  table in it is generated from `cli.Options` — change the option there, never
  the table, and regenerate it (see `make help`); `make check` fails when the
  two have drifted.
- Comments explain *why*, not what.
- Adding a dependency is a deliberate decision, and the reason goes in a
  comment where it is imported. The standard library is the default.
- Never write a build, test, lint or release command — or a version number —
  into a workflow under `.github/workflows/`. Each workflow runs a `make`
  target and sets up only what the runner cannot provide itself. That is what
  keeps local and CI results from drifting apart. Add a command → add a
  Makefile target. Add or bump a tool → edit `mise.toml`.

## Releasing

Only a maintainer can do this, and pushing a tag is a public, irreversible
action: the tag is what makes CI build and publish the release.

1. **Update the changelog.** In [CHANGELOG.md](CHANGELOG.md), replace
   `Unreleased` in the top section's heading with the release date, and check
   that what the section claims is what actually ships.
2. **`make check` must be green** on a clean checkout of `master`.
3. **Rerun the README's pinned demos** with `make demo`. Every command the
   README shows output for is run again and diffed against what is printed
   there, because output in a README is a promise nothing else checks. It needs
   an API key and costs a fraction of a cent, which is why it is not in `make
   check`. If it reports a difference, update the README to what it printed.
4. **Rehearse the build** with `make release-snapshot`. It produces the real
   archives in `dist/` and publishes nothing. Unpack one and run the binary in
   it: that it starts at all on this platform is what a broken release gets
   wrong first. Its version string is a snapshot one, not the version you are
   about to tag — that check is step 7. (The rehearsal wipes `dist/`, including
   anything a previous build left there.)
5. **Tag and push**, e.g. `git tag v0.1.0 && git push origin v0.1.0`. The tag
   must match `v*`; the version in the binary and in the archive names comes
   from it and from nowhere else.
6. **Watch CI publish it.** The release workflow builds the archives, the
   checksums and a build-provenance attestation, and creates the GitHub
   release.
7. **Verify on a clean machine** with the published `install.sh`, exactly the
   way the README tells a user to. Then run the installed binary's
   `--version`: if that number is wrong, nothing else about the release is
   worth checking.
8. **Put the changelog in the release notes.** CI creates the release with
   notes generated from the commit log; edit it and put this version's
   changelog section above them. Do not write a third, different summary.

The project's home page is published from `master` by its own workflow; see
`make help` for how to stage it locally. Neither the release nor the site is
ever built by hand.

## Reporting things

- A bug or a feature request: open an issue. The bug template asks for
  `--stats` and `--version` output — please include it, since almost every
  question about a wrong result is really a question about what was actually
  sent.
- A security problem: **do not open an issue.** Follow
  [SECURITY.md](SECURITY.md).
- Everyone taking part is expected to follow the
  [Code of Conduct](CODE_OF_CONDUCT.md).
