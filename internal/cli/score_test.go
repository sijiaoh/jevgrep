package cli

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The scores the §5 examples are written with, so that a golden file here is
// the example rather than a re-derivation of it. Every fixture line the
// examples print has its own number, which is also how a mixed-up field would
// show.
var exampleScores = map[string]float64{
	"a1": 0.91,
	"b":  0.12,
	"a2": 0.77,
	"c":  0.08,
	"d":  0.05,
	"e":  0.03,
	"a3": 0.88,

	"hit":       0.91,
	"hit again": 0.77,

	"starting up":       0.04,
	"I/O error on sda1": 0.91,
	"retrying":          0.11,
}

// byExample scores the fixture lines the way §5 does and everything else well
// below the threshold, so that only the named lines match.
func byExample(line string) float64 {
	if score, ok := exampleScores[line]; ok {
		return score
	}
	return 0.01
}

// The fixtures the score and JSON examples use: a file with a blank line in
// the middle, which is the line that has no score at all, and a log whose
// matching line sits far enough in to have a line number worth printing.
const (
	blanks = "hit\n\nhit again\n"
	appLog = "boot\nboot\nboot\nboot\nboot\nboot\nboot\nboot\nboot\nboot\n" +
		"starting up\nI/O error on sda1\nretrying\n"
)

func inScoreFixture(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	for name, content := range map[string]string{
		"f1.txt": f1, "g.txt": blanks, "app.log": appLog,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
}

// The -p and --json examples of §5, run end to end.
func TestScoreAndJSONMatchTheExamples(t *testing.T) {
	tests := []struct {
		golden string
		args   []string
	}{
		{golden: "score", args: []string{"-n", "-p", "a", "f1.txt"}},
		{golden: "score-context", args: []string{"-n", "-p", "-C1", "a", "f1.txt"}},
		// The blank line is never sent, so it has no score to print: "?", not
		// a zero this program made up.
		{golden: "score-blank", args: []string{"-n", "-p", "-C1", "hit", "g.txt"}},
		{golden: "json-context", args: []string{"--json", "-C1", "a disk error", "app.log"}},
	}

	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			inScoreFixture(t)
			env := newEnv(t, scoringServer(t, byExample))

			got := exec(t, env, tt.args...)

			if got.code != ExitMatch {
				t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
			}
			if got.stderr != "" {
				t.Errorf("stderr = %q, want it to be empty", got.stderr)
			}
			compareGolden(t, tt.golden, []byte(got.stdout))
		})
	}
}

// Every meaning is in the record, which is why --json exists: -p has room for
// one number and this has room for all of them.
func TestJSONReportsEveryMeaning(t *testing.T) {
	inScoreFixture(t)
	// Every line of the fixture in both maps: meaningServer reports a meaning
	// it has no scores for, which is what would catch a request going out
	// against the wrong one.
	disk := map[string]float64{}
	memory := map[string]float64{}
	for line := range strings.Lines(appLog) {
		line = strings.TrimSuffix(line, "\n")
		disk[line], memory[line] = byExample(line), 0.01
	}
	memory["I/O error on sda1"] = 0.02
	env := newEnv(t, meaningServer(t, map[string]map[string]float64{
		"a disk error":  disk,
		"out of memory": memory,
	}))

	got := exec(t, env, "--json", "-e", "a disk error", "-e", "out of memory", "app.log")

	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}
	compareGolden(t, "json-two-meanings", []byte(got.stdout))
}

// --json is a format: the options that shape a text line have nothing to shape
// and are ignored, rather than half applying to a record.
func TestJSONIgnoresTheTextShapingOptions(t *testing.T) {
	inScoreFixture(t)
	plain := func(t *testing.T) string {
		t.Helper()
		env := newEnv(t, scoringServer(t, byExample))
		return exec(t, env, "--json", "a", "f1.txt").stdout
	}
	want := plain(t)

	for _, args := range [][]string{
		{"-n"}, {"-H"}, {"-h"}, {"-p"}, {"-Z"}, {"--color=always"},
		{"-c"}, {"-l"}, {"-L"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			inScoreFixture(t)
			env := newEnv(t, scoringServer(t, byExample))

			got := exec(t, env, append(append([]string{"--json"}, args...), "a", "f1.txt")...)

			if got.stdout != want {
				t.Errorf("stdout = %q, want the records unchanged: %q", got.stdout, want)
			}
		})
	}
}

// The options that decide which lines there are still decide it under --json.
func TestJSONKeepsTheSelectionOptions(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		lines []int
	}{
		{name: "-m stops after the first", args: []string{"-m1"}, lines: []int{1}},
		{name: "-v selects the others", args: []string{"-v"}, lines: []int{2, 4, 5, 6}},
		{name: "-t moves the bar", args: []string{"-t", "0.9"}, lines: []int{1}},
		{name: "-C brings the context in", args: []string{"-C1", "-m1"}, lines: []int{1, 2}},
		// -q outranks --json, as it outranks every other way of printing.
		{name: "-q prints nothing", args: []string{"-q"}, lines: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inScoreFixture(t)
			env := newEnv(t, scoringServer(t, byExample))

			got := exec(t, env, append(tt.args, "--json", "a", "f1.txt")...)

			var lines []int
			for _, rec := range records(t, got.stdout) {
				lines = append(lines, rec.Line)
			}
			if len(lines) != len(tt.lines) {
				t.Fatalf("lines = %v, want %v (stdout %q)", lines, tt.lines, got.stdout)
			}
			for i, num := range lines {
				if num != tt.lines[i] {
					t.Errorf("lines = %v, want %v", lines, tt.lines)
					break
				}
			}
		})
	}
}

// A line with no score says so, in both formats, and the two say the same
// thing: -p writes "?" where JSON leaves "score" out and "scores" empty.
func TestALineWithoutScoresSaysSoInBothFormats(t *testing.T) {
	inScoreFixture(t)
	env := newEnv(t, scoringServer(t, byExample))

	text := exec(t, env, "-n", "-p", "-C1", "hit", "g.txt")
	if want := "2-?-\n"; !strings.Contains(text.stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", text.stdout, want)
	}

	inScoreFixture(t)
	env = newEnv(t, scoringServer(t, byExample))
	got := exec(t, env, "--json", "-C1", "hit", "g.txt")

	blank, ok := recordAt(t, got.stdout, 2)
	if !ok {
		t.Fatalf("stdout = %q, want a record for the blank line", got.stdout)
	}
	if blank.Score != nil {
		t.Errorf("score = %v, want it absent", *blank.Score)
	}
	if len(blank.Scores) != 0 {
		t.Errorf("scores = %v, want it empty", blank.Scores)
	}
	// The verdict is still reported: a blank line is decided, it just has
	// nothing measured behind the decision.
	if blank.Selected {
		t.Errorf("selected = true, want false")
	}
}

// A batch that failed leaves its lines without a score too, and neither format
// may pass that off as a zero.
func TestFailedBatchesHaveNoScoreEither(t *testing.T) {
	inScoreFixture(t)
	env := newEnv(t, failingServer(t, http.StatusInternalServerError, ""))

	got := exec(t, env, "-n", "-p", "-C1", "a", "f1.txt")

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	// Nothing was selected, so nothing is printed at all -- the point is that
	// the run did not invent scores to select lines with.
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing", got.stdout)
	}
	if !strings.Contains(got.stderr, "could not be scored") {
		t.Errorf("stderr = %q, want the failure reported", got.stderr)
	}
}

// -p has no line of its own to sit on in the per-file modes, so it is ignored
// rather than printed somewhere new.
func TestScoreIsIgnoredByThePerFileModes(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"-c"}, want: "3\n"},
		{args: []string{"-l"}, want: "f1.txt\n"},
		{args: []string{"-L"}, want: ""},
		{args: []string{"-q"}, want: ""},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			inScoreFixture(t)
			env := newEnv(t, scoringServer(t, byExample))

			got := exec(t, env, append(tt.args, "-p", "a", "f1.txt")...)

			if got.stdout != tt.want {
				t.Errorf("stdout = %q, want %q", got.stdout, tt.want)
			}
		})
	}
}

// JSON is UTF-8, so a byte that is not valid UTF-8 cannot survive as itself.
// That is the one place jevgrep's output is not the input byte for byte, and
// the text is still the line as read -- not the cleaned-up text the model saw.
func TestJSONTextReplacesInvalidUTF8(t *testing.T) {
	inScoreFixture(t)
	if err := os.WriteFile("odd.txt", []byte("a1 \xff\xfe tail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := newEnv(t, scoringServer(t, func(string) float64 { return 0.9 }))

	got := exec(t, env, "--json", "a", "odd.txt")

	rec, ok := recordAt(t, got.stdout, 1)
	if !ok {
		t.Fatalf("stdout = %q, want one record", got.stdout)
	}
	if want := "a1 �� tail"; rec.Text != want {
		t.Errorf("text = %q, want %q", rec.Text, want)
	}
}

// jsonRecord is what a reader of the output sees, spelled out here rather than
// shared with the package that writes it: a test that reuses the writer's own
// struct cannot notice a renamed field.
type jsonRecord struct {
	Type     string             `json:"type"`
	File     string             `json:"file"`
	Line     int                `json:"line"`
	Text     string             `json:"text"`
	Score    *float64           `json:"score"`
	Scores   map[string]float64 `json:"scores"`
	Selected bool               `json:"selected"`
}

func records(t *testing.T, stdout string) []jsonRecord {
	t.Helper()

	var out []jsonRecord
	for line := range strings.Lines(stdout) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec jsonRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decoding %q: %v", line, err)
		}
		if rec.Type != "line" {
			t.Errorf("type = %q, want \"line\"", rec.Type)
		}
		out = append(out, rec)
	}
	return out
}

func recordAt(t *testing.T, stdout string, num int) (jsonRecord, bool) {
	t.Helper()

	for _, rec := range records(t, stdout) {
		if rec.Line == num {
			return rec, true
		}
	}
	return jsonRecord{}, false
}

// -p has room for one number, so a search with several meanings prints the
// best of them and leaves the rest to --json.
func TestScoreReportsTheBestMeaning(t *testing.T) {
	inScoreFixture(t)
	env := newEnv(t, meaningServer(t, map[string]map[string]float64{
		"a disk error":  {"I/O error on sda1": 0.91, "retrying": 0.11},
		"out of memory": {"I/O error on sda1": 0.42, "retrying": 0.80},
	}))

	env.stdin = strings.NewReader("I/O error on sda1\nretrying\n")
	got := exec(t, env, "-n", "-p", "-e", "a disk error", "-e", "out of memory", "-")

	if want := "1:0.91:I/O error on sda1\n2:0.80:retrying\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q (stderr: %q)", got.stdout, want, got.stderr)
	}
}

// A blank line is decided without ever being scored, so -v selects it: the
// record says so, and still reports no score behind it.
func TestJSONSelectsUnsentLinesUnderInvert(t *testing.T) {
	inScoreFixture(t)
	env := newEnv(t, scoringServer(t, byExample))

	got := exec(t, env, "--json", "-v", "hit", "g.txt")

	blank, ok := recordAt(t, got.stdout, 2)
	if !ok {
		t.Fatalf("stdout = %q, want a record for the blank line", got.stdout)
	}
	if !blank.Selected {
		t.Errorf("selected = false, want true: -v selects the line that did not match")
	}
	if blank.Score != nil || len(blank.Scores) != 0 {
		t.Errorf("score = %v, scores = %v; want neither", blank.Score, blank.Scores)
	}
}

// The file field is the path as the user spelled it, the same name the text
// output prints and the error messages use.
func TestJSONNamesEveryInput(t *testing.T) {
	inScoreFixture(t)
	env := newEnv(t, scoringServer(t, byExample))

	got := exec(t, env, "--json", "a", "f1.txt", "g.txt")

	var files []string
	for _, rec := range records(t, got.stdout) {
		files = append(files, rec.File)
	}
	want := []string{"f1.txt", "f1.txt", "f1.txt", "g.txt", "g.txt"}
	if len(files) != len(want) {
		t.Fatalf("files = %v, want %v (stdout %q)", files, want, got.stdout)
	}
	for i, file := range files {
		if file != want[i] {
			t.Errorf("files = %v, want %v", files, want)
			break
		}
	}
}

// The two formats are two spellings of one answer: --json changes the shape of
// a record, never which lines there are or what is said about them. Generated
// input rather than a fixture, because the cases that could come apart are the
// awkward ones -- a blank line at the edge of a context window, a -m quota
// reached with lines still owed -- and those are easier to stumble on than to
// enumerate.
func TestJSONAndTextAgree(t *testing.T) {
	combos := [][]string{
		nil, {"-C1"}, {"-C2"}, {"-A1"}, {"-B2"}, {"-A0"},
		{"-m1"}, {"-m2", "-A1"}, {"-v"}, {"-v", "-C1"}, {"-t", "0.8"},
		{"-m1", "-B1"}, {"-v", "-m1", "-A2"},
	}
	for round := range 12 {
		rng := rand.New(rand.NewSource(int64(round)))
		var b strings.Builder
		for i := range 1 + rng.Intn(20) {
			switch rng.Intn(3) {
			case 0:
				fmt.Fprintf(&b, "a%d\n", i)
			case 1:
				fmt.Fprintf(&b, "b%d\n", i)
			default:
				b.WriteString("\n")
			}
		}
		content := b.String()

		for _, args := range combos {
			t.Run(fmt.Sprintf("%d/%s", round, strings.Join(args, " ")), func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Chdir(dir)

				env := newEnv(t, scoringServer(t, startsWithA))
				text := exec(t, env, append(append([]string{}, args...), "-n", "-p", "a", "f.txt")...)

				env = newEnv(t, scoringServer(t, startsWithA))
				js := exec(t, env, append(append([]string{}, args...), "--json", "a", "f.txt")...)

				if text.code != js.code {
					t.Errorf("exit codes differ: text %d, json %d", text.code, js.code)
				}

				// Which lines there are, and which of them were selected,
				// are the same in both formats whatever else happened. Their
				// scores are too, bar one case that is timing and not format:
				// past an -m quota the reader stops sending, and how many
				// lines it had already sent when the quota was reached
				// depends on how far ahead of the verdicts it had got. Such a
				// line is printed as context either way; it just carries a
				// score in the run where it was sent and none in the other.
				scored := !quotaRace(args)

				var want []string
				for line := range strings.Lines(text.stdout) {
					line = strings.TrimSuffix(line, "\n")
					if line == "--" {
						continue
					}
					num, rest, _ := strings.Cut(line, ":")
					if _, err := strconv.Atoi(num); err != nil {
						// A context line: number, "-", score, "-", text.
						num, rest, _ = strings.Cut(line, "-")
						if !scored {
							want = append(want, num+" false")
							continue
						}
						want = append(want, num+" false "+scoreField(rest, "-"))
						continue
					}
					want = append(want, num+" true "+scoreField(rest, ":"))
				}

				var got []string
				for _, rec := range records(t, js.stdout) {
					if !scored && !rec.Selected {
						got = append(got, fmt.Sprintf("%d false", rec.Line))
						continue
					}
					score := "?"
					if rec.Score != nil {
						score = strconv.FormatFloat(*rec.Score, 'f', 2, 64)
					}
					got = append(got, fmt.Sprintf("%d %v %s", rec.Line, rec.Selected, score))
				}

				if strings.Join(got, "|") != strings.Join(want, "|") {
					t.Errorf("json %v\ntext %v\n(text output %q)", got, want, text.stdout)
				}
			})
		}
	}
}

// quotaRace reports a command line under which a line past an -m quota may or
// may not have been scored before the reader was told to stop.
func quotaRace(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-m") {
			return true
		}
	}
	return false
}

func scoreField(rest, sep string) string {
	score, _, _ := strings.Cut(rest, sep)
	return score
}

// The text of a record is the line as it was read, not the text the model was
// shown: Query trims the CR of a CRLF and cuts a very long line down to what a
// request may carry, and none of that may reach the output.
func TestJSONTextIsTheLineAsRead(t *testing.T) {
	inScoreFixture(t)
	// Longer than the cap input puts on what one line contributes to a
	// request, so a truncated text would be obvious here and nowhere else.
	long := "a1 " + strings.Repeat("x", 9000)
	if err := os.WriteFile("long.txt", []byte(long+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := newEnv(t, scoringServer(t, func(string) float64 { return 0.9 }))

	got := exec(t, env, "--json", "a", "long.txt")

	rec, ok := recordAt(t, got.stdout, 1)
	if !ok {
		t.Fatalf("stdout = %q, want one record", got.stdout)
	}
	if rec.Text != long+"\r" {
		t.Errorf("text is %d bytes and ends %q; want the %d bytes of the line as read, CR and all",
			len(rec.Text), rec.Text[max(len(rec.Text)-4, 0):], len(long)+1)
	}
}
