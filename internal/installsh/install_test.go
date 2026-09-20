//go:build installsh

package installsh

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// The release the tests install, built once for all of them: goreleaser cross
// builds six platforms and that is the slow part of this file.
type release struct {
	dir     string // holds the archives and checksums.txt, as dist/ would
	tag     string // v0.0.1-snapshot
	version string // 0.0.1-snapshot, the form that appears in archive names
}

var (
	buildOnce sync.Once
	built     release
	buildErr  error
	buildDir  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return root
}

// snapshotRelease builds the real release archives, or skips the test on a
// platform install.sh has no release for -- there is nothing there for it to
// download, and pretending otherwise would test the skip, not the installer.
func snapshotRelease(t *testing.T) release {
	t.Helper()
	skipUnsupportedHost(t)

	root := repoRoot(t)
	buildOnce.Do(func() { built, buildErr = buildSnapshot(root) })
	if buildErr != nil {
		t.Fatalf("building the snapshot release: %v", buildErr)
	}
	return built
}

func skipUnsupportedHost(t *testing.T) {
	t.Helper()
	switch runtime.GOOS {
	case "linux", "darwin":
	default:
		t.Skipf("install.sh installs the Linux and macOS archives; this is %s", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64", "arm64":
	default:
		t.Skipf("no release archive for %s", runtime.GOARCH)
	}
}

// buildSnapshot runs goreleaser the way `make release-snapshot` does, against
// the project's own .goreleaser.yml so that what is installed here is what a
// tag would publish. The only edit is where the output goes: a copy of the
// config with its dist directory pointed at a temporary one, so that a test run
// does not quietly delete the binary `make build` left in dist/.
func buildSnapshot(root string) (release, error) {
	var err error
	buildDir, err = os.MkdirTemp("", "jevgrep-installsh-*")
	if err != nil {
		return release{}, err
	}

	config, err := os.ReadFile(filepath.Join(root, ".goreleaser.yml"))
	if err != nil {
		return release{}, err
	}
	dist := filepath.Join(buildDir, "dist")
	config = append(config, fmt.Sprintf("\ndist: %s\n", dist)...)
	configPath := filepath.Join(buildDir, "goreleaser.yml")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		return release{}, err
	}

	cmd := exec.Command("goreleaser", "release", "--snapshot", "--clean", "-f", configPath)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return release{}, fmt.Errorf("goreleaser: %w\n%s", err, out)
	}

	// The version is goreleaser's to decide (snapshot.version_template), so it
	// is read back off the archive it named rather than guessed at here.
	suffix := fmt.Sprintf("_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	matches, err := filepath.Glob(filepath.Join(dist, "jevgrep_*"+suffix))
	if err != nil || len(matches) != 1 {
		return release{}, fmt.Errorf("expected one %s archive in %s, found %v (%v)", suffix, dist, matches, err)
	}
	name := filepath.Base(matches[0])
	version := strings.TrimSuffix(strings.TrimPrefix(name, "jevgrep_"), suffix)
	return release{dir: dist, tag: "v" + version, version: version}, nil
}

// newReleaseServer serves the archives the way GitHub serves a release: a
// /releases/latest that redirects to the tag, and the files underneath
// /releases/download/<tag>/. tamper, when set, gets a chance to corrupt a file
// on its way out.
func newReleaseServer(t *testing.T, rel release, tamper func(name string, body []byte) []byte) *httptest.Server {
	t.Helper()
	prefix := "/releases/download/" + rel.tag + "/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases/latest":
			http.Redirect(w, r, "/releases/tag/"+rel.tag, http.StatusFound)
		case r.URL.Path == "/releases/tag/"+rel.tag:
			fmt.Fprintln(w, rel.tag)
		case strings.HasPrefix(r.URL.Path, prefix):
			name := strings.TrimPrefix(r.URL.Path, prefix)
			body, err := os.ReadFile(filepath.Join(rel.dir, filepath.Base(name)))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			if tamper != nil {
				body = tamper(name, body)
			}
			if _, err := w.Write(body); err != nil {
				t.Errorf("serving %s: %v", r.URL.Path, err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type result struct {
	stdout string
	stderr string
	code   int
}

// run executes install.sh with a controlled environment: its own HOME, and only
// the variables named here, so that whatever the developer has set cannot
// change what is being tested.
func run(t *testing.T, env map[string]string) result {
	t.Helper()
	return runWith(t, []string{"sh"}, env)
}

// runWith is run for a named shell: install.sh claims to be POSIX sh, and the
// shell a user's /bin/sh happens to be is the whole risk in that claim.
func runWith(t *testing.T, shell []string, env map[string]string) result {
	t.Helper()
	cmd := exec.Command(shell[0], append(shell[1:], "install.sh")...)
	cmd.Dir = repoRoot(t)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"SHELL=/bin/bash",
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("running install.sh: %v", err)
	}
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func (r result) mustContain(t *testing.T, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(r.stderr, w) {
			t.Errorf("install.sh did not say %q. it said:\n%s", w, r.stderr)
		}
	}
}

// installed home, and the paths a successful install leaves behind.
func newHome(t *testing.T) (home, bin string) {
	t.Helper()
	home = t.TempDir()
	return home, filepath.Join(home, ".local", "bin")
}

func TestInstallsAReleaseThatRuns(t *testing.T) {
	rel := snapshotRelease(t)
	srv := newReleaseServer(t, rel, nil)
	home, bin := newHome(t)

	got := run(t, map[string]string{
		"HOME":                home,
		"JEVGREP_BASE_URL":    srv.URL,
		"JEVGREP_INSTALL_DIR": bin,
	})
	if got.code != 0 {
		t.Fatalf("install.sh exited %d:\n%s", got.code, got.stderr)
	}
	// stdout belongs to whoever pipes install.sh somewhere; every word of ours
	// goes to stderr.
	if got.stdout != "" {
		t.Errorf("install.sh wrote to stdout: %q", got.stdout)
	}
	got.mustContain(t,
		"platform: "+runtime.GOOS+"/"+runtime.GOARCH,
		"latest release: "+rel.tag,
		"downloading jevgrep_"+rel.version+"_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz",
		"checksum ok",
		"installed "+filepath.Join(bin, "jevgrep"),
		"jevgrep --login",
	)

	out, err := exec.Command(filepath.Join(bin, "jevgrep"), "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("running the installed binary: %v\n%s", err, out)
	}
	if !strings.HasPrefix(string(out), "jevgrep ") {
		t.Errorf("installed binary reported %q", out)
	}
}

// curl is what most machines reach for, so the other paths only exist on the
// machines that have nothing else -- and are exactly the paths nobody notices
// are broken. busybox's wget is the one on an Alpine image, and it has neither
// GNU wget's flags nor its wording for a failure.
func TestTheOtherDownloadersInstallAndReportAFailureToo(t *testing.T) {
	rel := snapshotRelease(t)
	downloaders := map[string]func(t *testing.T) string{
		"wget": func(t *testing.T) string {
			if _, err := exec.LookPath("wget"); err != nil {
				t.Skip("wget is not installed")
			}
			return pathWithout(t, "curl")
		},
		"busybox wget": func(t *testing.T) string {
			busybox, err := exec.LookPath("busybox")
			if err != nil {
				t.Skip("busybox is not installed")
			}
			shims := t.TempDir()
			shim := "#!/bin/sh\nexec " + busybox + " wget \"$@\"\n"
			if err := os.WriteFile(filepath.Join(shims, "wget"), []byte(shim), 0o755); err != nil {
				t.Fatal(err)
			}
			return shims + string(os.PathListSeparator) + pathWithout(t, "curl", "wget")
		},
	}

	for name, pathFor := range downloaders {
		t.Run(name, func(t *testing.T) {
			path := pathFor(t)
			srv := newReleaseServer(t, rel, nil)
			home, bin := newHome(t)

			got := run(t, map[string]string{
				"HOME":                home,
				"PATH":                path,
				"JEVGREP_BASE_URL":    srv.URL,
				"JEVGREP_INSTALL_DIR": bin,
			})
			if got.code != 0 {
				t.Fatalf("install.sh exited %d:\n%s", got.code, got.stderr)
			}
			// Resolving the latest release is what differs most between the
			// downloaders, so the tag it found is the thing worth asserting.
			got.mustContain(t, "latest release: "+rel.tag, "checksum ok")
			if _, err := os.Stat(filepath.Join(bin, "jevgrep")); err != nil {
				t.Fatalf("nothing was installed: %v", err)
			}

			// A failed download has to be reported in our words whichever
			// downloader produced it, and none of them says it the same way.
			missing := run(t, map[string]string{
				"HOME":                home,
				"PATH":                path,
				"JEVGREP_BASE_URL":    srv.URL,
				"JEVGREP_INSTALL_DIR": bin,
				"JEVGREP_VERSION":     "v99.0.0",
			})
			if missing.code != 1 {
				t.Fatalf("install.sh exited %d, want 1:\n%s", missing.code, missing.stderr)
			}
			// Including the status: each downloader words a 404 differently,
			// and reading it back out of their wording is the fiddly half.
			missing.mustContain(t, "could not download", "jevgrep_99.0.0_", "(HTTP 404)")
		})
	}
}

func TestExplicitVersionIsInstalledWithOrWithoutTheV(t *testing.T) {
	rel := snapshotRelease(t)
	for _, asked := range []string{rel.tag, rel.version} {
		t.Run(asked, func(t *testing.T) {
			srv := newReleaseServer(t, rel, nil)
			home, bin := newHome(t)
			got := run(t, map[string]string{
				"HOME":                home,
				"JEVGREP_BASE_URL":    srv.URL,
				"JEVGREP_INSTALL_DIR": bin,
				"JEVGREP_VERSION":     asked,
			})
			if got.code != 0 {
				t.Fatalf("install.sh exited %d:\n%s", got.code, got.stderr)
			}
			got.mustContain(t, "requested release: "+rel.tag)
			if strings.Contains(got.stderr, "latest release") {
				t.Errorf("install.sh went looking for the latest release anyway:\n%s", got.stderr)
			}
		})
	}
}

// The checksum is the whole reason a release has one: a tampered archive must
// not reach the install directory, and the user must be told nothing was
// installed rather than left wondering.
func TestARewrittenArchiveIsNotInstalled(t *testing.T) {
	rel := snapshotRelease(t)
	srv := newReleaseServer(t, rel, func(name string, body []byte) []byte {
		if strings.HasSuffix(name, ".tar.gz") {
			return append(body, "tampered"...)
		}
		return body
	})
	home, bin := newHome(t)

	got := run(t, map[string]string{
		"HOME":                home,
		"JEVGREP_BASE_URL":    srv.URL,
		"JEVGREP_INSTALL_DIR": bin,
	})
	if got.code != 1 {
		t.Fatalf("install.sh exited %d, want 1:\n%s", got.code, got.stderr)
	}
	got.mustContain(t, "checksum mismatch", "nothing was installed")
	if strings.Contains(got.stderr, "checksum ok") {
		t.Errorf("install.sh said the checksum was fine:\n%s", got.stderr)
	}
	if _, err := os.Stat(filepath.Join(bin, "jevgrep")); !os.IsNotExist(err) {
		t.Errorf("something was installed after all (%v)", err)
	}
}

func TestAMissingReleaseSaysWhatCouldNotBeDownloaded(t *testing.T) {
	rel := snapshotRelease(t)
	srv := newReleaseServer(t, rel, nil)
	home, bin := newHome(t)

	got := run(t, map[string]string{
		"HOME":                home,
		"JEVGREP_BASE_URL":    srv.URL,
		"JEVGREP_INSTALL_DIR": bin,
		"JEVGREP_VERSION":     "v99.0.0",
	})
	if got.code != 1 {
		t.Fatalf("install.sh exited %d, want 1:\n%s", got.code, got.stderr)
	}
	got.mustContain(t, "could not download", "jevgrep_99.0.0_", "HTTP 404")
}

// A platform with no archive has to be told so before anything is downloaded,
// and told where to look instead.
func TestAnUnsupportedPlatformIsRefusedUpFront(t *testing.T) {
	rel := snapshotRelease(t)
	srv := newReleaseServer(t, rel, nil)
	home, bin := newHome(t)

	shims := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n-s) echo Haiku ;;\n-m) echo riscv64 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(shims, "uname"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	got := run(t, map[string]string{
		"HOME":                home,
		"PATH":                shims + string(os.PathListSeparator) + os.Getenv("PATH"),
		"JEVGREP_BASE_URL":    srv.URL,
		"JEVGREP_INSTALL_DIR": bin,
	})
	if got.code != 1 {
		t.Fatalf("install.sh exited %d, want 1:\n%s", got.code, got.stderr)
	}
	got.mustContain(t, "no release for haiku/riscv64", srv.URL+"/releases")
	if strings.Contains(got.stderr, "downloading") {
		t.Errorf("install.sh downloaded something anyway:\n%s", got.stderr)
	}
}

// Installing something the shell cannot find is half an install, so the PATH
// advice is part of the contract -- and it has to stay quiet when the directory
// is already there, or it trains people to ignore it.
func TestPathAdviceAppearsOnlyWhenItIsNeeded(t *testing.T) {
	rel := snapshotRelease(t)

	t.Run("not on PATH", func(t *testing.T) {
		srv := newReleaseServer(t, rel, nil)
		home, bin := newHome(t)
		got := run(t, map[string]string{
			"HOME":                home,
			"JEVGREP_BASE_URL":    srv.URL,
			"JEVGREP_INSTALL_DIR": bin,
		})
		got.mustContain(t, bin+" is not on your PATH", `export PATH="$HOME/.local/bin:$PATH"`, "~/.bashrc")
	})

	t.Run("on PATH", func(t *testing.T) {
		srv := newReleaseServer(t, rel, nil)
		home, bin := newHome(t)
		got := run(t, map[string]string{
			"HOME":                home,
			"PATH":                bin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"JEVGREP_BASE_URL":    srv.URL,
			"JEVGREP_INSTALL_DIR": bin,
		})
		if strings.Contains(got.stderr, "not on your PATH") {
			t.Errorf("install.sh gave PATH advice for a directory already on PATH:\n%s", got.stderr)
		}
	})
}

func TestAnUnwritableInstallDirectorySaysWhatToDo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory")
	}
	rel := snapshotRelease(t)
	srv := newReleaseServer(t, rel, nil)
	home, _ := newHome(t)

	locked := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(locked, 0o555); err != nil {
		t.Fatal(err)
	}

	got := run(t, map[string]string{
		"HOME":                home,
		"JEVGREP_BASE_URL":    srv.URL,
		"JEVGREP_INSTALL_DIR": locked,
	})
	if got.code != 1 {
		t.Fatalf("install.sh exited %d, want 1:\n%s", got.code, got.stderr)
	}
	got.mustContain(t, "cannot write to "+locked, "JEVGREP_INSTALL_DIR")
}

// pathWithout mirrors the real PATH into one directory of symlinks, leaving one
// command out of it. Mirroring rather than listing what install.sh needs: the
// list would be this test's guess at the script's dependencies, and a wrong
// guess fails as "wget is broken" somewhere far from the cause.
func pathWithout(t *testing.T, commands ...string) string {
	t.Helper()
	hidden := map[string]bool{}
	for _, command := range commands {
		hidden[command] = true
	}
	mirror := t.TempDir()
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if hidden[entry.Name()] {
				continue
			}
			link := filepath.Join(mirror, entry.Name())
			if _, err := os.Lstat(link); err == nil {
				continue // the first PATH entry wins, as it would in a shell
			}
			if err := os.Symlink(filepath.Join(dir, entry.Name()), link); err != nil {
				t.Fatalf("mirroring PATH: %v", err)
			}
		}
	}
	for command := range hidden {
		if _, err := os.Lstat(filepath.Join(mirror, command)); err == nil {
			t.Fatalf("%s is still on the mirrored PATH", command)
		}
	}
	return mirror
}

// Piped into sh from a cron job or a bare container, there may be no $HOME to
// default the install directory to. Saying so beats installing into /.local/bin.
func TestWithoutAHomeItSaysWhereToInstallInstead(t *testing.T) {
	skipUnsupportedHost(t)
	got := run(t, nil)
	if got.code != 1 {
		t.Fatalf("install.sh exited %d, want 1:\n%s", got.code, got.stderr)
	}
	got.mustContain(t, "no $HOME to install into", "JEVGREP_INSTALL_DIR")
}

// /bin/sh is dash on Debian, bash on macOS and busybox ash on Alpine, and the
// installer is piped straight into it. A bashism costs the user the install.
func TestItRunsUnderEveryShellOnThisMachine(t *testing.T) {
	rel := snapshotRelease(t)
	for _, shell := range [][]string{{"dash"}, {"bash"}, {"busybox", "sh"}, {"ksh"}} {
		if _, err := exec.LookPath(shell[0]); err != nil {
			continue
		}
		t.Run(strings.Join(shell, " "), func(t *testing.T) {
			srv := newReleaseServer(t, rel, nil)
			home, bin := newHome(t)
			got := runWith(t, shell, map[string]string{
				"HOME":                home,
				"JEVGREP_BASE_URL":    srv.URL,
				"JEVGREP_INSTALL_DIR": bin,
			})
			if got.code != 0 {
				t.Fatalf("install.sh exited %d:\n%s", got.code, got.stderr)
			}
			got.mustContain(t, "checksum ok", "installed "+filepath.Join(bin, "jevgrep"))
		})
	}
}

// The two tools install.sh cannot do without. Both checks happen before the
// download, so that a machine that is missing one is told in a second rather
// than after fetching six megabytes it cannot check.
func TestMissingToolsAreReportedBeforeAnythingIsDownloaded(t *testing.T) {
	rel := snapshotRelease(t)
	tests := map[string]struct {
		hide []string
		want string
	}{
		"no downloader": {[]string{"curl", "wget"}, "needs curl or wget"},
		"no hasher":     {[]string{"sha256sum", "shasum", "openssl"}, "needs sha256sum, shasum or openssl"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := newReleaseServer(t, rel, nil)
			home, bin := newHome(t)
			got := run(t, map[string]string{
				"HOME":                home,
				"PATH":                pathWithout(t, tc.hide...),
				"JEVGREP_BASE_URL":    srv.URL,
				"JEVGREP_INSTALL_DIR": bin,
			})
			if got.code != 1 {
				t.Fatalf("install.sh exited %d, want 1:\n%s", got.code, got.stderr)
			}
			got.mustContain(t, tc.want)
			if strings.Contains(got.stderr, "downloading") {
				t.Errorf("install.sh downloaded something anyway:\n%s", got.stderr)
			}
		})
	}
}

// An archive that checksums.txt says nothing about is as unchecked as one that
// fails the check, and has to be refused the same way.
func TestAnUnlistedArchiveIsNotInstalled(t *testing.T) {
	rel := snapshotRelease(t)
	srv := newReleaseServer(t, rel, func(name string, body []byte) []byte {
		if name == "checksums.txt" {
			return []byte("0000000000000000000000000000000000000000000000000000000000000000  something-else.tar.gz\n")
		}
		return body
	})
	home, bin := newHome(t)

	got := run(t, map[string]string{
		"HOME":                home,
		"JEVGREP_BASE_URL":    srv.URL,
		"JEVGREP_INSTALL_DIR": bin,
	})
	if got.code != 1 {
		t.Fatalf("install.sh exited %d, want 1:\n%s", got.code, got.stderr)
	}
	got.mustContain(t, "is not listed in checksums.txt", "nothing was installed")
	if _, err := os.Stat(filepath.Join(bin, "jevgrep")); !os.IsNotExist(err) {
		t.Errorf("something was installed after all (%v)", err)
	}
}
