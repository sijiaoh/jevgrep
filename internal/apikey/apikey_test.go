package apikey_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sijiaoh/jevgrep/internal/apikey"
)

// The tests set XDG_CONFIG_HOME and TYPESAFE_API_KEY, so none of them may run
// in parallel: t.Setenv is process-wide.

// isolate points the key file at a fresh directory and clears the environment
// variable, so that a developer's own key can never make a test pass.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(apikey.EnvVar, "")
	return dir
}

func TestLoadPrefersTheEnvironmentOverTheKeyFile(t *testing.T) {
	isolate(t)
	if _, err := apikey.Save("from-file"); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	t.Setenv(apikey.EnvVar, "from-env")

	got, err := apikey.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got != "from-env" {
		t.Errorf("Load() = %q, want the environment's key", got)
	}
}

func TestLoadTrimsWhitespaceAroundTheKey(t *testing.T) {
	isolate(t)
	t.Setenv(apikey.EnvVar, "  spaced-key\n")

	got, err := apikey.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got != "spaced-key" {
		t.Errorf("Load() = %q, want the key without surrounding whitespace", got)
	}
}

func TestLoadReadsTheKeyFile(t *testing.T) {
	isolate(t)
	if _, err := apikey.Save("stored-key"); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	got, err := apikey.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got != "stored-key" {
		t.Errorf("Load() = %q, want the stored key", got)
	}
}

func TestLoadReportsNoKeyWhenThereIsNone(t *testing.T) {
	isolate(t)

	if _, err := apikey.Load(); !errors.Is(err, apikey.ErrNotFound) {
		t.Errorf("Load() = %v, want ErrNotFound", err)
	}
}

// A file holding only whitespace is a key file someone truncated or a --login
// that was interrupted. It is "no key", not an empty key sent to the API.
func TestLoadTreatsABlankKeyFileAsNoKey(t *testing.T) {
	dir := isolate(t)
	writeKeyFile(t, dir, "   \n")

	if _, err := apikey.Load(); !errors.Is(err, apikey.ErrNotFound) {
		t.Errorf("Load() = %v, want ErrNotFound", err)
	}
}

// An unreadable key file must not look like a missing one: the CLI would send
// the user to --login to create a file that is already there.
func TestLoadReportsAnUnreadableKeyFile(t *testing.T) {
	dir := isolate(t)
	path := keyPath(t)
	// A directory in the key file's place fails the read on every platform,
	// and unlike a chmod 0 file it still fails when the tests run as root.
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := apikey.Load()
	if err == nil || errors.Is(err, apikey.ErrNotFound) {
		t.Fatalf("Load() = %v, want a read error", err)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("Load() error is %T, want it to wrap *fs.PathError so the CLI can name the path", err)
	}
	if !strings.HasPrefix(pathErr.Path, dir) {
		t.Errorf("error path = %q, want the key file under %q", pathErr.Path, dir)
	}
}

func TestPathHonorsXDGConfigHomeOnEveryPlatform(t *testing.T) {
	dir := isolate(t)

	got, err := apikey.Path()
	if err != nil {
		t.Fatalf("Path() = %v", err)
	}
	want := filepath.Join(dir, "jevgrep", "api_key")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

// Without XDG_CONFIG_HOME the key belongs wherever the platform keeps user
// configuration, not in the working directory or next to the binary.
func TestPathFallsBackToThePlatformConfigDirectory(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", "")

	got, err := apikey.Path()
	if err != nil {
		t.Fatalf("Path() = %v", err)
	}

	if !filepath.IsAbs(got) {
		t.Errorf("Path() = %q, want an absolute path", got)
	}
	if want := filepath.Join("jevgrep", "api_key"); !strings.HasSuffix(got, want) {
		t.Errorf("Path() = %q, want it to end in %q", got, want)
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no platform config directory here: %v", err)
	}
	if !strings.HasPrefix(got, configDir) {
		t.Errorf("Path() = %q, want it under %q", got, configDir)
	}
}

// A key file whose location depends on the working directory would go missing
// the next time jevgrep is run from somewhere else, and --login could not print
// the absolute path it promises.
func TestPathRejectsARelativeConfigHome(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", "relative/config")

	if _, err := apikey.Path(); err == nil {
		t.Error("Path() = nil error, want a relative XDG_CONFIG_HOME to be refused")
	}
	// Load and Save answer with it too, rather than quietly writing the key
	// under the working directory.
	if _, err := apikey.Load(); err == nil || errors.Is(err, apikey.ErrNotFound) {
		t.Errorf("Load() = %v, want the same refusal", err)
	}
	if _, err := apikey.Save("some-key"); err == nil {
		t.Error("Save() = nil error, want the same refusal")
	}
}

func TestSaveWritesTheKeyReadableOnlyByItsOwner(t *testing.T) {
	isolate(t)

	path, err := apikey.Save("secret-key")
	if err != nil {
		t.Fatalf("Save() = %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "secret-key\n" {
		t.Errorf("key file = %q, want the key and one newline", b)
	}

	if runtime.GOOS == "windows" {
		// Permission bits there are a lie told by the syscall layer; the file
		// lives under the user's own AppData instead.
		return
	}
	assertMode(t, path, 0o600)
	assertMode(t, filepath.Dir(path), 0o700)
}

func TestSaveReplacesAnExistingKey(t *testing.T) {
	isolate(t)
	if _, err := apikey.Save("old-key"); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	if _, err := apikey.Save("new-key"); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	got, err := apikey.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got != "new-key" {
		t.Errorf("Load() = %q, want the key that was saved last", got)
	}
}

// The key is written to a temporary file first; a leftover one would be a
// second copy of the key sitting in the config directory.
func TestSaveLeavesNoTemporaryFileBehind(t *testing.T) {
	isolate(t)

	path, err := apikey.Save("secret-key")
	if err != nil {
		t.Fatalf("Save() = %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("config directory holds %v, want only %q", names, filepath.Base(path))
	}
}

// §6 of the plan: a key must never reach the terminal, and an error message is
// the easiest place to leak one by accident.
func TestErrorsNeverContainTheKey(t *testing.T) {
	isolate(t)
	const key = "sk-do-not-print-me"
	// A plain file where the config directory should be: the one failure Save
	// can be made to hit while it is holding the key.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", blocked)

	_, err := apikey.Save(key)
	if err == nil {
		t.Fatal("Save() = nil, want an error")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("Save() error = %q, want it not to contain the key", err)
	}
}

func writeKeyFile(t *testing.T, dir, contents string) {
	t.Helper()
	path := filepath.Join(dir, "jevgrep", "api_key")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func keyPath(t *testing.T) string {
	t.Helper()
	path, err := apikey.Path()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %04o, want %04o", path, got, want)
	}
}
