# Security Policy

## Supported versions

jevgrep is at 0.x. Only the latest release gets fixes; there are no maintenance
branches for older ones.

## Reporting a vulnerability

**Please do not open a public issue.** Report it privately through GitHub:

<https://github.com/sijiaoh/jevgrep/security/advisories/new>

Include what you did, what happened, and what you expected. If a proof of
concept sends anything to the API, say so — and please keep it to lines you
are happy to have left the machine.

You should get an acknowledgement within a week. jevgrep is maintained by one
person, so if you hear nothing after that, comment on the advisory to bump it.
If a report is confirmed, the fix ships in the next release and the advisory is
published with it; if it is not, you will get a reason rather than silence.
There is no bounty.

## What jevgrep sends, and what it never sends

Most reports about a tool like this one are really about data leaving the
machine, so here is what the code guarantees. Each of these has its reasoning
recorded at the definition site — a change that breaks one of them is a
security bug, and that is what to report.

- **Every non-blank line searched is sent to the API** (`https://api.typesafe.ai`
  by default) to be scored. This is not a leak; it is what jevgrep is. What
  goes out is the cleaned, truncated form of the line; what gets printed is the
  bytes as they were read. Nothing done for the API can reach the output.
- **`.git/` is never searched by a walk**, and neither are credential-shaped
  files: `.env`, `.env.*`, `*.pem`, `*.key`, `*.p12`, `*.pfx`, and extensionless
  files named like an ssh private key (`id_rsa`, `id_ed25519`). A `.ssh`
  directory is skipped whatever it holds. **No option reopens them** — `-g` is a
  filter, not a key, and `--no-ignore` and `--hidden` do not bring them back.
  The only way to search such a file is to name it on the command line
  yourself. A credential sent to a remote API cannot be recalled.
- **Binary content is skipped** even when the path was named explicitly: a
  binary holds the built form of whatever secrets went into it.
- **The on-disk cache stores no content.** A record is a SHA-256 of what was
  asked, the probability that came back, and the day it arrived — no line, no
  meaning, no path. The files are owner-only, and the directory is printed by
  `--stats` so that it can be deleted. `--no-cache` skips it entirely.
- **API errors never carry the server's message.** A failed request reports its
  status and error type only, because a validation message is exactly where the
  server would quote the scored lines back at us.
- **The API key** lives in a `0600` file under the config directory, or in the
  `TYPESAFE_API_KEY` environment variable. It is never printed — not by
  `--stats`, not by an error, not by `--login` echoing what you typed.
- **`--dry-run` sends nothing at all** and does not even load a key. It is the
  safe way to find out what a search would cost.

## Verifying a release

Release archives are built by CI from a tag, with a checksum file and a GitHub
build-provenance attestation. `install.sh` checks the checksum for you and
installs nothing if it does not match. The attestation is worth more than the
checksums — those are published from the same place as the archives — and you
can check it yourself with:

```sh
gh attestation verify <archive> --repo sijiaoh/jevgrep
```
