package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// searchTree writes a fixture tree inside the test's working directory, so that
// the paths jevgrep prints are the short ones a user would have typed.
func searchTree(t *testing.T, files map[string]string) {
	t.Helper()

	t.Chdir(t.TempDir())
	for name, content := range files {
		path := filepath.FromSlash(name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// at joins path elements the way jevgrep prints them, so that the expectations
// below read like the fixture and still hold on a platform whose separator is
// not "/".
func at(elem ...string) string { return filepath.Join(elem...) }

// recursiveTree is §5's fixture: a directory whose entries sort into the order
// the output has to come out in, plus the entries the filters have to leave out.
var recursiveTree = map[string]string{
	"d/x.txt":      "ERROR a1\n",
	"d/sub/y.txt":  "ERROR a2\n",
	"d/.dot.txt":   "ERROR a4\n",
	"d/.gitignore": "*.log\n",
	"d/noise.log":  "ERROR a5\n",
}

func TestRecursiveSearchWalksInNameOrder(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	searchTree(t, recursiveTree)

	got := exec(t, env, "-rn", "a failure", "d")

	// Every directory's entries by name, files and subdirectories in the one
	// sequence, so "sub" comes before "x.txt". The hidden file and the ignored
	// one are not there.
	want := at("d", "sub", "y.txt") + ":1:ERROR a2\n" + at("d", "x.txt") + ":1:ERROR a1\n"
	if got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want it to be empty", got.stderr)
	}
}

func TestRecursiveOptionsReachTheWalk(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "--hidden",
			args: []string{"--hidden"},
			want: at("d", ".dot.txt") + ":1:ERROR a4\n" + at("d", "sub", "y.txt") + ":1:ERROR a2\n" + at("d", "x.txt") + ":1:ERROR a1\n",
		},
		{
			name: "--no-ignore",
			args: []string{"--no-ignore"},
			want: at("d", "noise.log") + ":1:ERROR a5\n" + at("d", "sub", "y.txt") + ":1:ERROR a2\n" + at("d", "x.txt") + ":1:ERROR a1\n",
		},
		{
			name: "-g including",
			args: []string{"-g", "sub/**"},
			want: at("d", "sub", "y.txt") + ":1:ERROR a2\n",
		},
		{
			name: "-g excluding",
			args: []string{"-g", "!sub/**"},
			want: at("d", "x.txt") + ":1:ERROR a1\n",
		},
		{
			name: "-g repeated, last match wins",
			args: []string{"-g", "*.txt", "-g", "!x.txt"},
			want: at("d", "sub", "y.txt") + ":1:ERROR a2\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnv(t, scoringServer(t, mentionsError))
			searchTree(t, recursiveTree)

			args := append(append([]string{"-rn"}, tt.args...), "a failure", "d")
			got := exec(t, env, args...)
			if got.stdout != tt.want {
				t.Errorf("stdout = %q, want %q (stderr: %q)", got.stdout, tt.want, got.stderr)
			}
		})
	}
}

// A directory operand names the files even when it holds exactly one, which is
// what GNU grep does: how many files a walk turns up is the thing the user did
// not know either.
func TestADirectoryOperandNamesItsFilesEvenWhenThereIsOne(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	searchTree(t, map[string]string{"one/only.txt": "ERROR here\n"})

	got := exec(t, env, "-rn", "a failure", "one")

	if want := at("one", "only.txt") + ":1:ERROR here\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
}

// -r does not turn the default on for a file operand: nothing was expanded.
func TestAFileOperandUnderRecursiveIsStillOneInput(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	searchTree(t, map[string]string{"a.txt": "ERROR here\n"})

	got := exec(t, env, "-rn", "a failure", "a.txt")

	if want := "1:ERROR here\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
}

// -r with no PATH searches ".", and does not quietly search the pipe instead.
func TestRecursiveWithNoPathSearchesTheWorkingDirectory(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	env.stdin = strings.NewReader("ERROR from the pipe\n")
	searchTree(t, map[string]string{"a.txt": "ERROR from the tree\n"})

	got := exec(t, env, "-rn", "a failure")

	if want := "a.txt:1:ERROR from the tree\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
}

// Naming a path turns off the filters that pick which files to search: this is
// the one way to search a hidden file, an ignored one or a secret.
func TestANamedPathSkipsTheWalkFilters(t *testing.T) {
	for _, name := range []string{".env", ".hidden.txt", "noise.log"} {
		t.Run(name, func(t *testing.T) {
			env := newEnv(t, scoringServer(t, mentionsError))
			searchTree(t, map[string]string{
				".gitignore": "*.log\n",
				name:         "ERROR here\n",
			})

			got := exec(t, env, "a failure", name)
			if want := "ERROR here\n"; got.stdout != want {
				t.Errorf("stdout = %q, want %q", got.stdout, want)
			}
		})
	}
}

// The one filter a named path does not escape. It is a notice, not an error:
// nothing went wrong, so the exit code is the one the other file earned.
func TestANamedBinaryFileIsSkippedWithANotice(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	searchTree(t, map[string]string{
		"blob.bin": "ELF\x00ERROR inside\n",
		"a.txt":    "ERROR here\n",
	})

	got := exec(t, env, "-n", "a failure", "blob.bin", "a.txt")

	if want := "a.txt:1:ERROR here\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
	if want := "jevgrep: blob.bin: binary file, skipped\n"; got.stderr != want {
		t.Errorf("stderr = %q, want %q", got.stderr, want)
	}
	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d", got.code, ExitMatch)
	}
}

// A directory that cannot be read is the walk's version of a file that cannot
// be opened: reported, skipped, and the run ends at 2 with the rest searched.
func TestAnUnreadableDirectoryIsReportedAndTheRestIsSearched(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions that stop the user running the test")
	}

	env := newEnv(t, scoringServer(t, mentionsError))
	searchTree(t, map[string]string{
		"d/locked/x.txt": "ERROR hidden away\n",
		"d/open.txt":     "ERROR here\n",
	})
	locked := filepath.Join("d", "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	got := exec(t, env, "-rn", "a failure", "d")

	if want := at("d", "open.txt") + ":1:ERROR here\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
	if !strings.Contains(got.stderr, locked) {
		t.Errorf("stderr = %q, want it to name %q", got.stderr, locked)
	}
	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
}
