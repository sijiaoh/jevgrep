package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files from the current output")

// goldenDir is resolved once, at start-up: the tests run from a fixture
// directory of their own, so by the time one of them looks for a golden file
// the relative path no longer points at the package.
var goldenDir = func() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Join(dir, "testdata")
}()

// The fixture the output modes are all written against: f1.txt has three
// matching lines with a gap between the last two, f2.txt has none, f3.txt has
// one at the very start, and empty.txt has no lines at all -- which -c still
// has to count and -L still has to list.
const (
	f1 = "a1\nb\na2\nc\nd\ne\na3\n"
	f2 = "b\nc\n"
	f3 = "a9\nz\n"
)

// startsWithA is the fixture's idea of a meaning: the lines written as matches
// are the ones that match.
func startsWithA(line string) float64 {
	if strings.HasPrefix(line, "a") {
		return 0.9
	}
	return 0.1
}

// inFixture puts the test in a directory holding the fixture, so that the
// paths in the golden files are the short ones the examples are written with.
func inFixture(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	for name, content := range map[string]string{
		"f1.txt": f1, "f2.txt": f2, "f3.txt": f3, "empty.txt": "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
}

// TestOutputModes is the whole of §2 as a user sees it: one case per shape of
// output, each run end to end through Run.
func TestOutputModes(t *testing.T) {
	tests := []struct {
		golden string
		args   []string
		code   int
	}{
		{golden: "lines", args: []string{"-n", "a", "f1.txt"}},
		{golden: "context-C1", args: []string{"-n", "-C1", "a", "f1.txt"}},
		{golden: "context-two-files", args: []string{"-n", "-C1", "a", "f1.txt", "f3.txt"}},
		{golden: "count", args: []string{"-c", "a", "f1.txt", "f3.txt"}},
		{golden: "count-empty", args: []string{"-c", "a", "empty.txt", "f1.txt"}},
		{golden: "files-with-matches", args: []string{"-l", "a", "f1.txt", "f2.txt", "f3.txt"}},
		// -L printed a name and still exits 1: the exit code answers whether a
		// line was selected, and none was. grep does this too.
		{golden: "files-without-match", args: []string{"-L", "a", "f2.txt"}, code: ExitNoMatch},
		{golden: "files-without-match-empty", args: []string{"-L", "a", "empty.txt", "f1.txt", "f2.txt"}},
		{golden: "quiet", args: []string{"-q", "a", "f1.txt"}},
		{golden: "max-count", args: []string{"-n", "-m2", "a", "f1.txt"}},
		// The two lines after the last one -m allowed are printed as context,
		// even though one of them matches: it is context, not a match.
		{golden: "max-count-context", args: []string{"-n", "-m1", "-A2", "a", "f1.txt"}},
		{golden: "null-files-with-matches", args: []string{"-lZ", "a", "f1.txt", "f3.txt"}},
		{golden: "null-lines", args: []string{"-HnZ", "a", "f1.txt"}},
		{golden: "null-lines-context", args: []string{"-HnZ", "-C1", "a", "f1.txt"}},
		{golden: "null-count", args: []string{"-cZ", "a", "f1.txt", "f3.txt"}},
		{golden: "color", args: []string{"--color=always", "-Hn", "a", "f1.txt"}},
		{golden: "color-context", args: []string{"--color=always", "-Hn", "-C1", "a", "f1.txt"}},
	}

	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			inFixture(t)
			env := newEnv(t, scoringServer(t, startsWithA))

			got := exec(t, env, tt.args...)

			if got.code != tt.code {
				t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, tt.code, got.stderr)
			}
			if got.stderr != "" {
				t.Errorf("stderr = %q, want it to be empty", got.stderr)
			}
			compareGolden(t, tt.golden, []byte(got.stdout))
		})
	}
}

func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join(goldenDir, name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./internal/cli -update` to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output does not match %s:\n got %q\nwant %q", path, got, want)
	}
}

// §2.1's order, which is not the order the options were written in.
func TestOutputModesOutrankEachOther(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "-l wins over -c whichever came first", args: []string{"-c", "-l"}, want: "f1.txt\n"},
		{name: "and the other way round", args: []string{"-l", "-c"}, want: "f1.txt\n"},
		{name: "the later of -l and -L wins", args: []string{"-l", "-L"}, want: ""},
		{name: "and the later of -L and -l wins", args: []string{"-L", "-l"}, want: "f1.txt\n"},
		{name: "-q silences -c", args: []string{"-c", "-q"}, want: ""},
		{name: "-q silences -l", args: []string{"-l", "-q"}, want: ""},
		// Context only means something where lines are printed.
		{name: "-c drops the context", args: []string{"-c", "-C2"}, want: "3\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inFixture(t)
			env := newEnv(t, scoringServer(t, startsWithA))

			got := exec(t, env, append(tt.args, "a", "f1.txt")...)

			if got.stdout != tt.want {
				t.Errorf("stdout = %q, want %q", got.stdout, tt.want)
			}
			if got.code != ExitMatch {
				t.Errorf("exit code = %d, want %d", got.code, ExitMatch)
			}
		})
	}
}

// -m, -l and -q are paid for in requests, not only in printing: the lines a
// file has left once the output has what it needs are never sent.
func TestQuotasStopSendingTheRestOfTheFile(t *testing.T) {
	// Long enough that the reader cannot have the whole file in hand before
	// the first verdict comes back: search lets it run ahead, and on a short
	// file everything is sent before anything is decided.
	const total = 3000
	var b strings.Builder
	b.WriteString("a1\n")
	for i := range total - 1 {
		fmt.Fprintf(&b, "b%d\n", i)
	}

	tests := []struct {
		name string
		args []string
		// most is the largest number of lines the run may send. The reader
		// runs ahead of the verdicts, so a quota costs some lines it turns out
		// not to need; what it must not do is read on to the end of the file.
		most int64
		code int
	}{
		{name: "-m1 stops at the first match", args: []string{"-m1"}, most: total / 2},
		{name: "-m0 sends nothing at all", args: []string{"-m0"}, most: 0, code: ExitNoMatch},
		{name: "-l stops at the first match", args: []string{"-l"}, most: total / 2},
		{name: "-q stops at the first match", args: []string{"-q"}, most: total / 2},
		// -c and -L cannot stop: neither knows its answer before the end.
		{name: "-c reads every line", args: []string{"-c"}, most: total},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFile(t, "long.txt", b.String())
			var sent atomic.Int64
			env := newEnv(t, countingServer(t, &sent, startsWithA))

			got := exec(t, env, append(tt.args, "a", path)...)

			if got.code != tt.code {
				t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, tt.code, got.stderr)
			}
			if sent.Load() > tt.most {
				t.Errorf("scored %d lines of %d, want at most %d", sent.Load(), total, tt.most)
			}
		})
	}
}

// -q answers one question, and a file it could not read does not change the
// answer it already has.
func TestQuietExitsZeroOnAMatchEvenWithAnError(t *testing.T) {
	inFixture(t)
	env := newEnv(t, scoringServer(t, startsWithA))

	got := exec(t, env, "-q", "a", "f1.txt", "gone.txt")

	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d", got.code, ExitMatch)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing", got.stdout)
	}
	// The error is still reported: the run did fail to read that file.
	if !strings.Contains(got.stderr, "gone.txt") {
		t.Errorf("stderr = %q, want the unreadable file named", got.stderr)
	}
}

func TestQuietWithoutAMatchStillFailsOnAnError(t *testing.T) {
	inFixture(t)
	env := newEnv(t, scoringServer(t, startsWithA))

	got := exec(t, env, "-q", "zzz", "f2.txt", "gone.txt")

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
}

// A file that could not be opened has no count to print and is not a file that
// did not match: it is a file that was not searched.
func TestUnreadableFilesAreNotCountedOrListed(t *testing.T) {
	for _, mode := range []string{"-c", "-L"} {
		t.Run(mode, func(t *testing.T) {
			inFixture(t)
			env := newEnv(t, scoringServer(t, startsWithA))

			got := exec(t, env, mode, "a", "gone.txt", "f1.txt")

			if strings.Contains(got.stdout, "gone.txt") {
				t.Errorf("stdout = %q, want no mention of the file that would not open", got.stdout)
			}
			if got.code != ExitError {
				t.Errorf("exit code = %d, want %d", got.code, ExitError)
			}
		})
	}
}

// --color=auto is the default, and the default has to keep escapes out of a
// pipe: what jevgrep writes there is read by the next program.
func TestColorAutoFollowsTheTerminal(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		isTTY bool
		want  bool
	}{
		{name: "a pipe gets no color", args: nil, want: false},
		{name: "a terminal does", args: nil, isTTY: true, want: true},
		{name: "--color=never on a terminal", args: []string{"--color=never"}, isTTY: true, want: false},
		{name: "--color=always into a pipe", args: []string{"--color=always"}, want: true},
		{name: "--color on its own means auto", args: []string{"--color"}, isTTY: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inFixture(t)
			env := newEnv(t, scoringServer(t, startsWithA))
			env.terminal = fakeTerminal{stdoutIsTTY: tt.isTTY}

			// With -H and -n there are fields to color; the line's own text is
			// never colored, so a run without them has nothing to show.
			got := exec(t, env, append(tt.args, "-Hn", "a", "f1.txt")...)

			if colored := strings.Contains(got.stdout, "\x1b["); colored != tt.want {
				t.Errorf("colored = %v, want %v (stdout %q)", colored, tt.want, got.stdout)
			}
		})
	}
}

// --color takes its value with "=" only, because the word after it is the
// MEANING far more often than it is "never".
func TestColorDoesNotSwallowTheMeaning(t *testing.T) {
	inFixture(t)
	env := newEnv(t, scoringServer(t, startsWithA))

	got := exec(t, env, "--color", "a", "f1.txt")

	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}
	if got.stdout != "a1\na2\na3\n" {
		t.Errorf("stdout = %q, want the matching lines", got.stdout)
	}
}

func TestBadOptionArgumentsAreUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		// grep reads a negative -m as infinity. Here that would turn "stop
		// after one line" into "read the whole file", and the bill would be
		// the first anyone heard of it.
		{name: "-m rejects a negative", args: []string{"-m", "-1"}, want: "--max-count"},
		{name: "-m rejects a word", args: []string{"-m", "lots"}, want: "--max-count"},
		{name: "-A rejects a negative", args: []string{"-A", "-1"}, want: "--after-context"},
		{name: "-C rejects a fraction", args: []string{"-C", "1.5"}, want: "--context"},
		{name: "--color rejects anything else", args: []string{"--color=bad"}, want: "--color"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnv(t, "")

			got := exec(t, env, append(tt.args, "a", "f1.txt")...)

			if got.code != ExitError {
				t.Errorf("exit code = %d, want %d", got.code, ExitError)
			}
			if !strings.Contains(got.stderr, tt.want) {
				t.Errorf("stderr = %q, want it to name %s", got.stderr, tt.want)
			}
			if got.stdout != "" {
				t.Errorf("stdout = %q, want nothing", got.stdout)
			}
		})
	}
}

// -C fills in only what -A and -B did not say, whichever order they came in.
func TestContextOptionsMerge(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		after, before int
		context       bool
	}{
		{name: "-C sets both", args: []string{"-C2"}, after: 2, before: 2, context: true},
		{name: "-A0 keeps its zero after -C2", args: []string{"-C2", "-A0"}, after: 0, before: 2, context: true},
		{name: "and before it", args: []string{"-A0", "-C2"}, after: 0, before: 2, context: true},
		{name: "-B0 keeps its zero", args: []string{"-C1", "-B0"}, after: 1, before: 0, context: true},
		{name: "the later -A wins", args: []string{"-A2", "-A1"}, after: 1, context: true},
		{name: "no context option at all", args: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parse(append(tt.args, "a"))
			if err != nil {
				t.Fatalf("parse(%q) failed: %v", tt.args, err)
			}
			if cfg.after != tt.after || cfg.before != tt.before || cfg.context != tt.context {
				t.Errorf("after = %d, before = %d, context = %v; want %d, %d, %v",
					cfg.after, cfg.before, cfg.context, tt.after, tt.before, tt.context)
			}
		})
	}
}

func TestModeFollowsThePriorityTable(t *testing.T) {
	tests := []struct {
		args []string
		want mode
	}{
		{args: nil, want: modeLines},
		{args: []string{"-c"}, want: modeCount},
		{args: []string{"-l"}, want: modeFilesWithMatches},
		{args: []string{"-L"}, want: modeFilesWithoutMatch},
		{args: []string{"-q"}, want: modeQuiet},
		{args: []string{"-c", "-l"}, want: modeFilesWithMatches},
		{args: []string{"-l", "-c"}, want: modeFilesWithMatches},
		{args: []string{"-l", "-L"}, want: modeFilesWithoutMatch},
		{args: []string{"-L", "-l"}, want: modeFilesWithMatches},
		{args: []string{"-q", "-c", "-l"}, want: modeQuiet},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			cfg, err := parse(append(tt.args, "a"))
			if err != nil {
				t.Fatalf("parse(%q) failed: %v", tt.args, err)
			}
			if got := cfg.mode(); got != tt.want {
				t.Errorf("mode = %d, want %d", got, tt.want)
			}
		})
	}
}

// The two -Z shapes are what `xargs -0` reads, so the bytes are the contract.
func TestNullFileNamesHaveNoNewline(t *testing.T) {
	inFixture(t)
	env := newEnv(t, scoringServer(t, startsWithA))

	got := exec(t, env, "-lZ", "a", "f1.txt", "f3.txt")

	if want := "f1.txt\x00f3.txt\x00"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
}

// The same path given twice is two inputs, and every per-file mode has to
// treat it as two, the way grep does. Nothing in the stream of lines says so
// but the line numbers starting over.
func TestTheSamePathTwiceIsTwoInputs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "-c", args: []string{"-c"}, want: "f1.txt:3\nf1.txt:3\n"},
		{name: "-l", args: []string{"-l"}, want: "f1.txt\nf1.txt\n"},
		{name: "-m1", args: []string{"-m1", "-Hn"}, want: "f1.txt:1:a1\nf1.txt:1:a1\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inFixture(t)
			env := newEnv(t, scoringServer(t, startsWithA))

			got := exec(t, env, append(tt.args, "a", "f1.txt", "f1.txt")...)

			if got.stdout != tt.want {
				t.Errorf("stdout = %q, want %q", got.stdout, tt.want)
			}
		})
	}
}

// A file with no lines at all is still a file that was searched: -L lists it
// and -c counts it, and only a stream of lines would leave it out.
func TestEmptyInputsAreStillSettled(t *testing.T) {
	inFixture(t)
	env := newEnv(t, scoringServer(t, startsWithA))

	got := exec(t, env, "-c", "a", "empty.txt", "f1.txt", "empty.txt")

	if want := "empty.txt:0\nf1.txt:3\nempty.txt:0\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
}

// Every file a walk turns up is a file to count, in the order the walk found
// them, and the empty one is in there too.
func TestCountsCoverEveryFileAWalkFinds(t *testing.T) {
	inFixture(t)
	env := newEnv(t, scoringServer(t, startsWithA))

	got := exec(t, env, "-rc", "a", ".")

	want := "empty.txt:0\nf1.txt:3\nf2.txt:0\nf3.txt:1\n"
	if got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}
}
