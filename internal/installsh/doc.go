// Package installsh holds the test that install.sh actually installs jevgrep.
//
// There is no code here, on purpose: install.sh is shell, and what is worth
// testing about it is the whole path a user takes -- resolve the latest
// release, download the archive GitHub has for this platform, check it against
// checksums.txt, put the binary somewhere and have it run. Nothing in that
// path is mocked. The test builds the real release archives with goreleaser
// (`make release-snapshot` in miniature, into a directory of its own) and
// serves them over a local HTTP server laid out the way GitHub lays releases
// out, which is what JEVGREP_BASE_URL in install.sh exists for.
//
// It lives behind the "installsh" build tag and runs as `make install-test`,
// which `make check` includes. The tag is what keeps `go test ./...` -- the
// inner loop -- from paying for a six-platform cross build every time; it is
// not an opt-in like the calibration tag, which guards the one thing CI must
// not run. Nothing here talks to the network beyond localhost.
package installsh
