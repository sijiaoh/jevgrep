package buildinfo

import "testing"

// setVersion overrides the linker-stamped value for one test.
func setVersion(t *testing.T, v string) {
	t.Helper()

	original := version
	t.Cleanup(func() { version = original })
	version = v
}

// Release binaries get their version from ldflags, so the stamped path is the
// one goreleaser depends on; it is only reachable from inside the package.
func TestVersionUsesLinkerStamp(t *testing.T) {
	setVersion(t, "v1.2.3")

	if got := Version(); got != "v1.2.3" {
		t.Errorf("Version() = %q, want %q", got, "v1.2.3")
	}
}

// Without a stamp the value comes from the build info the go command embedded,
// which differs between `go test`, `go install` and a VCS-stamped build. Only
// the invariant every caller relies on is pinned: never empty, never the raw
// "(devel)" placeholder.
func TestVersionWithoutLinkerStampIsUsable(t *testing.T) {
	setVersion(t, "")

	got := Version()
	if got == "" {
		t.Error("Version() is empty, want a usable version string")
	}
	if got == "(devel)" {
		t.Errorf("Version() = %q, want the placeholder replaced by %q", got, devVersion)
	}
}
