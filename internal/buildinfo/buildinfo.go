// Package buildinfo exposes the version string of the running binary.
package buildinfo

import "runtime/debug"

// version is set at link time with
// -X github.com/sijiaoh/jevgrep/internal/buildinfo.version=<tag>.
// It lives in its own package so that release tooling (goreleaser) targets a
// stable symbol path that does not move when cmd/ or internal/cli/ is
// restructured.
var version string

// devVersion is reported when the binary was neither stamped at link time nor
// built from a tagged module, i.e. a local `go build`.
const devVersion = "dev"

// Version reports the binary's version. Release builds stamp it via ldflags;
// `go install github.com/sijiaoh/jevgrep/cmd/jevgrep@v1.2.3` has no ldflags, so
// fall back to the module version the go command recorded in the binary.
func Version() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		// "(devel)" is what an untagged local build reports; it is noise to a user.
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return devVersion
}
