#!/bin/sh
# Installs a released jevgrep binary: detect the platform, download the archive
# GitHub has for it, check it against the release's checksums.txt, and put the
# binary in a directory the user owns.
#
# Everything is wrapped in main() and called on the last line, because this
# script's headline use is `curl -fsSL .../install.sh | sh`: sh reads a piped
# script as it goes, so a connection that drops halfway would otherwise run the
# half it got. With main() last, a truncated download runs nothing at all.
#
# All output goes to stderr: stdout belongs to whoever pipes us somewhere.
#
# POSIX sh only -- macOS still ships bash 3.2, and this also runs under dash,
# busybox ash and whatever /bin/sh is on a container image.

set -eu

# The repository the archives come from. JEVGREP_BASE_URL is not for users: it
# is what lets the test in internal/installsh point this script at a local
# server serving real snapshot archives, so that the download, the checksum and
# the install are exercised for real rather than mocked.
base_url=${JEVGREP_BASE_URL:-https://github.com/sijiaoh/jevgrep}

# Under $HOME by default, and deliberately not /usr/local/bin even where that
# exists and is writable: installing into a system directory is something the
# user asks for, not something a piped-in script decides. Left empty when there
# is no $HOME either (a container, a cron job), so that main() can say so rather
# than install into /.local/bin.
if [ -n "${JEVGREP_INSTALL_DIR:-}" ]; then
	install_dir=$JEVGREP_INSTALL_DIR
elif [ -n "${HOME:-}" ]; then
	install_dir=$HOME/.local/bin
else
	install_dir=
fi

# Empty means "whatever GitHub calls latest".
version=${JEVGREP_VERSION:-}

say() {
	printf '> %s\n' "$*" >&2
}

die() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

# One of curl or wget, chosen once. Everything below goes through fetch() and
# redirect_target() so the difference between the two lives in one place.
downloader=

pick_downloader() {
	if command -v curl >/dev/null 2>&1; then
		downloader=curl
	elif command -v wget >/dev/null 2>&1; then
		downloader=wget
	else
		die 'needs curl or wget'
	fi
}

# Whatever curl or wget had to say about a failure, as a parenthesised tail for
# our own message. It is kept for the failures that have no status code -- a name
# that does not resolve, a refused connection -- where the downloader's sentence
# is the only thing that tells the user what to fix.
fetch_detail() {
	_detail=$(sed 's/^[a-z]*: ([0-9]*) //' "$1" | grep -v '^[[:space:]]*$' | tail -n 1)
	[ -z "$_detail" ] || printf ' (%s)' "$_detail"
}

# fetch URL DEST -- download URL to DEST, or die saying which URL and, when the
# server said so, with what status. The downloader's own stderr is captured
# rather than shown, so that a failure is reported once, in our words.
fetch() {
	_url=$1
	_dest=$2
	_errfile=$tmp/fetch.err
	_code=
	if [ "$downloader" = curl ]; then
		if _code=$(curl -fsSL -o "$_dest" -w '%{http_code}' "$_url" 2>"$_errfile"); then
			return
		fi
	else
		# No verbosity flags: wget's own noise is captured, not shown, and every
		# flag that quiets it down is one more thing busybox's wget -- the wget
		# on an Alpine image, where there is usually no curl to fall back to --
		# may not have.
		if wget -O "$_dest" "$_url" 2>"$_errfile"; then
			return
		fi
		# GNU wget says "ERROR 404: Not Found.", busybox says "server returned
		# error: HTTP/1.1 404 Not Found"; this is the only place either of them
		# puts the status code.
		_code=$(sed -n \
			-e 's/.*ERROR \([0-9][0-9]*\).*/\1/p' \
			-e 's#.*HTTP/[0-9.]* \([0-9][0-9][0-9]\).*#\1#p' \
			"$_errfile" | head -n 1)
	fi
	case $_code in
	'' | 000) die "could not download $_url$(fetch_detail "$_errfile")" ;;
	*) die "could not download $_url (HTTP $_code)" ;;
	esac
}

# redirect_target URL -- print where URL redirects to. This is how the latest
# release is resolved: /releases/latest redirects to /releases/tag/vX.Y.Z, which
# costs no API call and so cannot be turned away by GitHub's rate limit for
# unauthenticated requests -- a shared CI address would hit that routinely.
redirect_target() {
	if [ "$downloader" = curl ]; then
		# No -f and no --show-error: all we want is where we ended up, and a
		# repository with no releases at all answers this with a page we would
		# otherwise complain about twice -- once here and once in the caller,
		# which already says the useful half.
		curl -sL -o /dev/null -w '%{url_effective}' "$1" || true
	else
		# -S prints the response headers, and the first Location among them is
		# where we were sent. Letting wget follow the redirect rather than
		# refusing it (--max-redirect=0) keeps this working on busybox's wget,
		# which has no such option.
		wget -S -O /dev/null "$1" 2>&1 |
			sed -n 's/^[[:space:]]*[Ll]ocation:[[:space:]]*\([^[:space:]]*\).*/\1/p' |
			head -n 1
	fi
}

detect_platform() {
	os=$(uname -s | tr '[:upper:]' '[:lower:]')
	_machine=$(uname -m)
	case $_machine in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	# Unmapped on purpose: it only reaches the message below, and saying
	# riscv64 there beats saying "unknown".
	*) arch=$_machine ;;
	esac
	case $os/$arch in
	linux/amd64 | linux/arm64 | darwin/amd64 | darwin/arm64) ;;
	*) die "no release for $os/$arch. see $base_url/releases" ;;
	esac
	say "platform: $os/$arch"
}

resolve_version() {
	if [ -n "$version" ]; then
		# Accept both v0.1.0 and 0.1.0; the tag has the v, the archive name does not.
		case $version in
		v*) tag=$version ;;
		*) tag=v$version ;;
		esac
		say "requested release: $tag"
		return
	fi

	_final=$(redirect_target "$base_url/releases/latest")
	case $_final in
	*/releases/tag/*) tag=${_final##*/releases/tag/} ;;
	*) die "could not work out the latest release from $base_url/releases/latest" ;;
	esac
	tag=${tag%%[?#]*}
	[ -n "$tag" ] || die "could not work out the latest release from $base_url/releases/latest"
	say "latest release: $tag"
}

# sha256sum is GNU, shasum comes with macOS, openssl is the fallback for an
# image that has neither. Chosen before anything is downloaded, and not inside
# sha256_of: a die() in there would run in a command substitution, where it
# would kill that subshell and leave the caller comparing against an empty
# digest -- reporting a missing tool as a checksum mismatch.
hasher=

pick_hasher() {
	for _candidate in sha256sum shasum openssl; do
		if command -v "$_candidate" >/dev/null 2>&1; then
			hasher=$_candidate
			return
		fi
	done
	die 'needs sha256sum, shasum or openssl to check the download'
}

# Prints the sha256 of a file as a bare hex digest.
sha256_of() {
	case $hasher in
	sha256sum) sha256sum "$1" | cut -d' ' -f1 ;;
	shasum) shasum -a 256 "$1" | cut -d' ' -f1 ;;
	openssl) openssl dgst -sha256 "$1" | sed 's/.*= *//' ;;
	esac
}

verify() {
	# awk compares the file name, rather than sed matching it as a pattern: an
	# archive name is full of dots, and a pattern would accept a name that only
	# looks like this one.
	_want=$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1; exit }' "$tmp/checksums.txt")
	[ -n "$_want" ] || die "$archive is not listed in checksums.txt; nothing was installed"
	_got=$(sha256_of "$tmp/$archive")
	[ "$_want" = "$_got" ] || die "checksum mismatch for $archive; nothing was installed"
	# Said out loud on success too: a check nobody ever sees pass is a check
	# nobody has reason to believe ran.
	say 'checksum ok'
}

install_binary() {
	tar -xzf "$tmp/$archive" -C "$tmp" jevgrep 2>/dev/null ||
		die "could not unpack $archive"

	mkdir -p "$install_dir" 2>/dev/null ||
		die "cannot create $install_dir. set JEVGREP_INSTALL_DIR to a directory you own"

	# Written beside the target and renamed into place: rename is atomic, so a
	# jevgrep that is currently running is never a half-written file, and a
	# failure here leaves whatever was already installed alone.
	_staged=$install_dir/.jevgrep.install.$$
	cp "$tmp/jevgrep" "$_staged" 2>/dev/null ||
		die "cannot write to $install_dir. set JEVGREP_INSTALL_DIR=\$HOME/.local/bin, or re-run with sudo"
	chmod 755 "$_staged" || die "could not make $_staged executable"
	mv -f "$_staged" "$install_dir/jevgrep" ||
		die "cannot write to $install_dir. set JEVGREP_INSTALL_DIR=\$HOME/.local/bin, or re-run with sudo"

	# An absolute path from here on, so that what is printed -- and what the
	# PATH advice talks about -- is something the user can paste and run.
	install_dir=$(cd "$install_dir" && pwd) || die "cannot read $install_dir"
	say "installed $install_dir/jevgrep"
}

# The rc file to suggest, guessed from $SHELL. Getting this wrong costs the user
# nothing -- we only ever print the line, never edit the file.
#
# The tildes are meant to stay tildes: this is a path shown to a human, and
# expanding it here would print someone else's home directory to anyone who
# copies the suggestion out of a terminal recording.
# shellcheck disable=SC2088
rc_file() {
	case ${SHELL:-} in
	*/zsh) printf '~/.zshrc\n' ;;
	*/bash) printf '~/.bashrc\n' ;;
	*/ksh) printf '~/.kshrc\n' ;;
	*) printf '~/.profile\n' ;;
	esac
}

report_path() {
	_dir=$install_dir
	case ":${PATH:-}:" in
	*":$_dir:"*) return ;;
	esac

	# $HOME written back as $HOME so the suggested line keeps working for a user
	# who later reads their rc file on another machine.
	_shown=$_dir
	if [ -n "${HOME:-}" ]; then
		case $_dir in
		"$HOME"/*) _shown="\$HOME/${_dir#"$HOME"/}" ;;
		esac
	fi

	say "$_dir is not on your PATH. add it with:"
	# $PATH stays unexpanded on purpose: what is printed is the line the user
	# appends to their rc file, where it has to be expanded then, not now.
	# shellcheck disable=SC2016
	case ${SHELL:-} in
	*/fish) printf '    fish_add_path %s\n' "$_shown" >&2 ;;
	*) printf '    echo '\''export PATH="%s:$PATH"'\'' >> %s\n' "$_shown" "$(rc_file)" >&2 ;;
	esac
}

# Deliberately no signup URL here: --login prints where to get a key, and that
# URL has one home (internal/apikey.SignupURL). A copy in this file is a copy
# that can go stale without anything noticing.
report_next() {
	say 'next: jevgrep --login    (it will say where to get a key)'
	printf '        jevgrep --help\n' >&2
}

cleanup() {
	[ -z "${tmp:-}" ] || rm -rf "$tmp"
}

main() {
	tmp=
	# cleanup alone on a signal would run and then let the script carry on with
	# its temporary directory deleted; each signal exits with the 128+n the
	# shell would have reported had we not trapped it at all.
	trap cleanup EXIT
	trap 'cleanup; exit 129' HUP
	trap 'cleanup; exit 130' INT
	trap 'cleanup; exit 143' TERM

	say 'jevgrep installer'
	# $HOME stays literal: it is the name of the variable the user has to set.
	# shellcheck disable=SC2016
	[ -n "$install_dir" ] ||
		die 'there is no $HOME to install into. set JEVGREP_INSTALL_DIR to a directory you own'
	pick_downloader
	pick_hasher
	detect_platform
	resolve_version

	archive=jevgrep_${tag#v}_${os}_${arch}.tar.gz
	tmp=$(mktemp -d "${TMPDIR:-/tmp}/jevgrep-install.XXXXXX")

	say "downloading $archive"
	fetch "$base_url/releases/download/$tag/$archive" "$tmp/$archive"
	fetch "$base_url/releases/download/$tag/checksums.txt" "$tmp/checksums.txt"

	verify
	install_binary
	report_path
	report_next
}

main "$@"
