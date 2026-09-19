package output_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sijiaoh/jevgrep/internal/input"
	"github.com/sijiaoh/jevgrep/internal/output"
	"github.com/sijiaoh/jevgrep/internal/search"
)

var update = flag.Bool("update", false, "rewrite the golden files from the current output")

// lines holds every shape the formatter has to reproduce verbatim, so that each
// golden file is also the byte-for-byte record of those edge cases: a CR left
// by a CRLF terminator, bytes that are not UTF-8, a colon inside the file name
// (the field separator, which grep does not escape either), stdin's name, a
// line that had no terminator at all, and line numbers of different widths.
var lines = []input.Line{
	{File: "notes.txt", Num: 1, Text: "a plain line"},
	{File: "notes.txt", Num: 2, Text: "kept CR at the end\r"},
	{File: "notes.txt", Num: 3, Text: "  leading space and a : colon"},
	{File: "notes.txt", Num: 4, Text: ""},
	{File: "odd:name.txt", Num: 12, Text: "the file name has a colon"},
	{File: "bytes.bin", Num: 120, Text: "not UTF-8: \xff\xfe here"},
	{File: input.StdinName, Num: 7, Text: "read from standard input"},
	{File: "notes.txt", Num: 1234, Text: "the last line, with no newline of its own"},
}

func TestPrintMatchesTheGoldenFiles(t *testing.T) {
	tests := []struct {
		golden string
		opts   output.Options
	}{
		{golden: "plain", opts: output.Options{}},
		{golden: "number", opts: output.Options{LineNumber: true}},
		{golden: "file", opts: output.Options{MultipleInputs: true}},
		{golden: "file-number", opts: output.Options{MultipleInputs: true, LineNumber: true}},
	}

	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			var buf bytes.Buffer
			p := output.New(&buf, tt.opts)
			for _, l := range lines {
				p.Print(l, nil, true)
			}
			if err := p.Err(); err != nil {
				t.Fatalf("Err() = %v, want nil", err)
			}
			compareGolden(t, tt.golden, buf.Bytes())
		})
	}
}

func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./internal/output -update` to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output does not match %s:\n got %q\nwant %q", path, got, want)
	}
}

func TestFilenameDefaultFollowsTheInputCount(t *testing.T) {
	tests := []struct {
		name string
		opts output.Options
		want string
	}{
		{name: "one input", opts: output.Options{}, want: "x\n"},
		{name: "several inputs", opts: output.Options{MultipleInputs: true}, want: "notes.txt:x\n"},
		{
			name: "-H names the file even for one input",
			opts: output.Options{Filenames: output.Always},
			want: "notes.txt:x\n",
		},
		{
			name: "-h wins over the many-input default",
			opts: output.Options{MultipleInputs: true, Filenames: output.Never},
			want: "x\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := output.New(&buf, tt.opts)
			p.Print(input.Line{File: "notes.txt", Num: 1, Text: "x"}, nil, true)
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// None of the fixture lines carries an escape of its own, so any escape in the
// output of a run without --color would be one the formatter added.
func TestPrintAddsNoEscapeSequences(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{MultipleInputs: true, LineNumber: true})
	for _, l := range lines {
		p.Print(l, nil, true)
	}
	if bytes.Contains(buf.Bytes(), []byte{0x1b}) {
		t.Errorf("output contains an ANSI escape: %q", buf.String())
	}
}

type recordingWriter struct{ writes []string }

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

// A line has to reach the pipe whole and on its own: a reader downstream sees
// output as it is written, and half a line is worse than a late one.
func TestEachLineIsWrittenInOnePiece(t *testing.T) {
	w := &recordingWriter{}
	p := output.New(w, output.Options{MultipleInputs: true, LineNumber: true})
	for _, l := range lines {
		p.Print(l, nil, true)
	}

	if len(w.writes) != len(lines) {
		t.Fatalf("got %d writes for %d lines: %q", len(w.writes), len(lines), w.writes)
	}
	for i, got := range w.writes {
		if !strings.HasSuffix(got, "\n") || strings.Count(got, "\n") != 1 {
			t.Errorf("write %d is not exactly one line: %q", i, got)
		}
	}
}

type failingWriter struct {
	fail  error
	after int
	calls int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > w.after {
		return 0, w.fail
	}
	return len(p), nil
}

func TestWriteErrorsAreReportedAndStopFurtherWrites(t *testing.T) {
	broken := errors.New("broken pipe")
	w := &failingWriter{fail: broken, after: 1}
	p := output.New(w, output.Options{})

	for i := range 5 {
		p.Print(input.Line{File: "notes.txt", Num: i + 1, Text: "x"}, nil, true)
	}

	if !errors.Is(p.Err(), broken) {
		t.Errorf("Err() = %v, want %v", p.Err(), broken)
	}
	// One write succeeded, one failed, and the rest were never attempted.
	if w.calls != 2 {
		t.Errorf("writer was called %d times, want 2", w.calls)
	}
}

func TestErrIsNilWhenEveryLineWasWritten(t *testing.T) {
	p := output.New(&bytes.Buffer{}, output.Options{})
	p.Print(input.Line{File: "notes.txt", Num: 1, Text: "x"}, nil, true)
	if err := p.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

// The buffer Print reuses must not leak into the next line: a short line after
// a long one is the case that would show it.
func TestPrintReusesItsBufferSafely(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{MultipleInputs: true, LineNumber: true})
	p.Print(input.Line{File: "a-rather-long-name.txt", Num: 123456, Text: "a long line of text"}, nil, true)
	p.Print(input.Line{File: "b.txt", Num: 1, Text: "s"}, nil, true)

	want := "a-rather-long-name.txt:123456:a long line of text\nb.txt:1:s\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEveryOutputLineEndsWithNewline(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{MultipleInputs: true, LineNumber: true})
	for _, l := range lines {
		p.Print(l, nil, true)
	}
	got := bytes.Count(buf.Bytes(), []byte("\n"))
	if got != len(lines) {
		t.Errorf("got %d newlines for %d lines", got, len(lines))
	}
	// Spelled out because it is the one grep behaviour the input side cannot
	// provide: the last line of the fixture had no terminator of its own.
	if !bytes.HasSuffix(buf.Bytes(), []byte(strconv.Itoa(lines[len(lines)-1].Num)+":"+lines[len(lines)-1].Text+"\n")) {
		t.Errorf("last line is not terminated as expected: %q", buf.String())
	}
}

// scorer returns a fixed score for every line, so that Print can be exercised
// through the seam it exists for.
type scorer struct{ score float64 }

func (s scorer) Score(_ context.Context, _ string, lines []string) ([]float64, error) {
	scores := make([]float64, len(lines))
	for i := range scores {
		scores[i] = s.score
	}
	return scores, nil
}

// Print is written to be handed straight to search.Options.Emit. Asserting it
// here keeps that seam compiling and keeps the two packages' idea of a matched
// line the same one.
func TestPrintPlugsIntoSearchAsItIs(t *testing.T) {
	expr, err := search.Compile([]search.Term{{Meaning: "anything"}}, 0.5, false)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	p := output.New(&buf, output.Options{MultipleInputs: true, LineNumber: true})
	s := search.New(scorer{score: 0.9}, expr, search.Options{
		Emit: func(l input.Line, v search.Verdict, _ []float64) { p.Print(l, nil, v == search.Match) },
	})

	if err := s.Run(t.Context(), slices.Values(lines[:3])); err != nil {
		t.Fatal(err)
	}
	if err := p.Err(); err != nil {
		t.Fatal(err)
	}

	want := "notes.txt:1:a plain line\nnotes.txt:2:kept CR at the end\r\nnotes.txt:3:  leading space and a : colon\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// f1 is the fixture the context examples are written against: lines 1, 3 and 7
// are selected, so there is an overlap (1-3), a gap (4-6) and a match at the
// very end to get right.
var f1 = []struct {
	line     input.Line
	selected bool
}{
	{input.Line{File: "f1.txt", Num: 1, Text: "a1"}, true},
	{input.Line{File: "f1.txt", Num: 2, Text: "b"}, false},
	{input.Line{File: "f1.txt", Num: 3, Text: "a2"}, true},
	{input.Line{File: "f1.txt", Num: 4, Text: "c"}, false},
	{input.Line{File: "f1.txt", Num: 5, Text: "d"}, false},
	{input.Line{File: "f1.txt", Num: 6, Text: "e"}, false},
	{input.Line{File: "f1.txt", Num: 7, Text: "a3"}, true},
}

func printF1(p *output.Printer) {
	for _, l := range f1 {
		p.Print(l.line, nil, l.selected)
	}
}

func TestContextPrintsTheLinesAroundASelectedLine(t *testing.T) {
	tests := []struct {
		name string
		opts output.Options
		want string
	}{
		{
			name: "no context option prints no separator",
			opts: output.Options{LineNumber: true},
			want: "1:a1\n3:a2\n7:a3\n",
		},
		{
			name: "-C1",
			opts: output.Options{LineNumber: true, Context: true, Before: 1, After: 1},
			want: "1:a1\n2-b\n3:a2\n4-c\n--\n6-e\n7:a3\n",
		},
		{
			name: "-A1 does not reach back",
			opts: output.Options{LineNumber: true, Context: true, After: 1},
			want: "1:a1\n2-b\n3:a2\n4-c\n--\n7:a3\n",
		},
		{
			name: "-B1 does not reach forward",
			opts: output.Options{LineNumber: true, Context: true, Before: 1},
			want: "1:a1\n2-b\n3:a2\n--\n6-e\n7:a3\n",
		},
		{
			// -A0 asks for no context lines and still puts the separator
			// between two selected lines that are not adjacent, as grep does.
			name: "-C0 is not the same as no context option",
			opts: output.Options{LineNumber: true, Context: true},
			want: "1:a1\n--\n3:a2\n--\n7:a3\n",
		},
		{
			name: "context wider than the gap joins the groups up",
			opts: output.Options{LineNumber: true, Context: true, Before: 2, After: 2},
			want: "1:a1\n2-b\n3:a2\n4-c\n5-d\n6-e\n7:a3\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := output.New(&buf, tt.opts)
			printF1(p)
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// A line printed as the context of one match is not printed again as the
// context of the next, and nothing crosses a file boundary: the -B lines held
// when a file ends belong to that file alone.
func TestContextIsPrintedOnceAndNeverCrossesFiles(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{
		MultipleInputs: true, LineNumber: true, Context: true, Before: 2, After: 2,
	})
	printF1(p)
	p.Print(input.Line{File: "f3.txt", Num: 1, Text: "a9"}, nil, true)
	p.Print(input.Line{File: "f3.txt", Num: 2, Text: "z"}, nil, false)

	want := "f1.txt:1:a1\nf1.txt-2-b\nf1.txt:3:a2\nf1.txt-4-c\nf1.txt-5-d\nf1.txt-6-e\nf1.txt:7:a3\n" +
		"--\nf3.txt:1:a9\nf3.txt-2-z\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The same path given twice is two inputs, and line numbers starting over is
// the only sign of it the printer gets.
func TestLineNumbersStartingOverStartANewGroup(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{LineNumber: true, Context: true, After: 1})
	p.Print(input.Line{File: "f.txt", Num: 1, Text: "a"}, nil, true)
	p.Print(input.Line{File: "f.txt", Num: 1, Text: "a"}, nil, true)

	if want := "1:a\n--\n1:a\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestNullTerminatesTheFileName(t *testing.T) {
	tests := []struct {
		name  string
		print func(*output.Printer)
		want  string
	}{
		{
			name:  "-lZ writes the name with no newline at all",
			print: func(p *output.Printer) { p.Name("f1.txt"); p.Name("f3.txt") },
			want:  "f1.txt\x00f3.txt\x00",
		},
		{
			name:  "-cZ",
			print: func(p *output.Printer) { p.Count("f1.txt", 3) },
			want:  "f1.txt\x003\n",
		},
		{
			// Only the separator after the file name is a NUL; the one after
			// the line number stays what it says about the line.
			name:  "-HnZ with context",
			print: printF1,
			want:  "f1.txt\x001:a1\nf1.txt\x002-b\nf1.txt\x003:a2\nf1.txt\x004-c\n--\nf1.txt\x006-e\nf1.txt\x007:a3\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := output.New(&buf, output.Options{
				Filenames: output.Always, LineNumber: true, Null: true, Context: true, Before: 1, After: 1,
			})
			tt.print(p)
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// The exact bytes matter: this is what a terminal reads, and the one thing
// jevgrep colors differently from grep is the text, which it leaves alone.
func TestColorPaintsTheFieldsAndNotTheText(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{
		Filenames: output.Always, LineNumber: true, Color: true, Context: true, After: 1,
	})
	p.Print(input.Line{File: "f1.txt", Num: 1, Text: "a1"}, nil, true)
	p.Print(input.Line{File: "f1.txt", Num: 2, Text: "b"}, nil, false)
	p.Print(input.Line{File: "f1.txt", Num: 7, Text: "a3"}, nil, true)
	p.Count("f1.txt", 2)
	p.Name("f1.txt")

	want := "\x1b[35mf1.txt\x1b[m\x1b[36m:\x1b[m\x1b[32m1\x1b[m\x1b[36m:\x1b[ma1\n" +
		"\x1b[35mf1.txt\x1b[m\x1b[36m-\x1b[m\x1b[32m2\x1b[m\x1b[36m-\x1b[mb\n" +
		"\x1b[36m--\x1b[m\n" +
		"\x1b[35mf1.txt\x1b[m\x1b[36m:\x1b[m\x1b[32m7\x1b[m\x1b[36m:\x1b[ma3\n" +
		// The count itself is a value, not a field label, and grep leaves it
		// uncolored too.
		"\x1b[35mf1.txt\x1b[m\x1b[36m:\x1b[m2\n" +
		"\x1b[35mf1.txt\x1b[m\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCountAndNameFollowTheFilenameRule(t *testing.T) {
	tests := []struct {
		name string
		opts output.Options
		want string
	}{
		{name: "one input counts without a name", opts: output.Options{}, want: "3\n"},
		{name: "several inputs name the file", opts: output.Options{MultipleInputs: true}, want: "f1.txt:3\n"},
		{name: "-H names it for one input", opts: output.Options{Filenames: output.Always}, want: "f1.txt:3\n"},
		{
			name: "-h drops it for many",
			opts: output.Options{MultipleInputs: true, Filenames: output.Never},
			want: "3\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := output.New(&buf, tt.opts)
			p.Count("f1.txt", 3)
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}

	// -l and -L print the name whatever the rule says: the name is the whole
	// output, not a prefix on something else.
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{Filenames: output.Never})
	p.Name("f1.txt")
	if want := "f1.txt\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

// The -B lines are all the printer holds, so a run with no -B holds nothing
// however long the input is.
func TestBeforeContextHoldsNoMoreThanItWasAskedFor(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{LineNumber: true, Context: true, Before: 2})
	for i := range 1000 {
		p.Print(input.Line{File: "f.txt", Num: i + 1, Text: strconv.Itoa(i + 1)}, nil, false)
	}
	p.Print(input.Line{File: "f.txt", Num: 1001, Text: "hit"}, nil, true)

	if want := "999-999\n1000-1000\n1001:hit\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

// headline stands in for the expression's own reduction: the printer is given
// the rule, it does not know it.
func headline(scores []float64) float64 { return scores[0] }

// -p sits where grep's byte offset does -- after the line number, before the
// text -- and is always four characters wide, so that a field a script reads
// with `awk -F:` has one shape.
func TestScorePrintsAFixedWidthField(t *testing.T) {
	tests := []struct {
		name   string
		scores []float64
		want   string
	}{
		{name: "a plain score", scores: []float64{0.91}, want: "1:0.91:a1\n"},
		{name: "certainty is still four characters", scores: []float64{1}, want: "1:1.00:a1\n"},
		{name: "and so is zero", scores: []float64{0}, want: "1:0.00:a1\n"},
		{name: "rounded to two decimals", scores: []float64{0.123456}, want: "1:0.12:a1\n"},
		// A line that was never sent, or whose batch failed, has no score at
		// all. Printing 0.00 there would be quoting an answer the model was
		// never asked for.
		{name: "no score at all", scores: nil, want: "1:?:a1\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := output.New(&buf, output.Options{LineNumber: true, Score: true, Headline: headline})
			p.Print(input.Line{File: "f1.txt", Num: 1, Text: "a1"}, tt.scores, true)
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// A context line carries its score too, behind the "-" that says what it is.
func TestScoreIsPrintedOnContextLinesAsWell(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{
		LineNumber: true, Score: true, Headline: headline, Context: true, Before: 1, After: 1,
	})
	p.Print(input.Line{File: "f1.txt", Num: 1, Text: "b"}, []float64{0.12}, false)
	p.Print(input.Line{File: "f1.txt", Num: 2, Text: "a1"}, []float64{0.91}, true)
	p.Print(input.Line{File: "f1.txt", Num: 3, Text: "c"}, []float64{0.08}, false)

	if want := "1-0.12-b\n2:0.91:a1\n3-0.08-c\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

// The score is colored like the line number: both say what the line is, in
// front of what it says.
func TestScoreIsColoredLikeTheLineNumber(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{LineNumber: true, Score: true, Headline: headline, Color: true})
	p.Print(input.Line{File: "f1.txt", Num: 1, Text: "a1"}, []float64{0.91}, true)

	want := "\x1b[32m1\x1b[m\x1b[36m:\x1b[m\x1b[32m0.91\x1b[m\x1b[36m:\x1b[ma1\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The record is one line of NDJSON with its fields in a fixed order, which is
// what a golden test and a user's `jq` both rely on.
func TestJSONRecordsAreNDJSON(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{
		// Every text decoration on at once: none of them may show.
		Filenames: output.Always, LineNumber: true, Null: true, Color: true, Score: true,
		JSON: true, Meanings: []string{"a disk error"}, Headline: headline,
		Context: true, Before: 1, After: 0,
	})
	p.Print(input.Line{File: "app.log", Num: 11, Text: "starting up"}, []float64{0.04}, false)
	p.Print(input.Line{File: "app.log", Num: 12, Text: "I/O error on sda1"}, []float64{0.91}, true)
	// A line with no scores, and a jump in the line numbers that would have
	// brought the "--" separator out in the text format.
	p.Print(input.Line{File: "app.log", Num: 20, Text: ""}, nil, true)

	want := `{"type":"line","file":"app.log","line":11,"text":"starting up","score":0.04,"scores":{"a disk error":0.04},"selected":false}` + "\n" +
		`{"type":"line","file":"app.log","line":12,"text":"I/O error on sda1","score":0.91,"scores":{"a disk error":0.91},"selected":true}` + "\n" +
		`{"type":"line","file":"app.log","line":20,"text":"","scores":{},"selected":true}` + "\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The scores go out in full: --json is read by programs, and the two decimals
// a terminal column has room for are not what they came for.
func TestJSONKeepsTheFullPrecision(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{
		JSON: true, Meanings: []string{"m"}, Headline: headline,
	})
	p.Print(input.Line{File: "f.txt", Num: 1, Text: "x"}, []float64{0.123456789}, true)

	if !strings.Contains(buf.String(), "0.123456789") {
		t.Errorf("got %q, want the score as it was scored", buf.String())
	}
}

// A line of code is full of "<" and "&", and a reader who opens the output
// should see them: escaping them is valid JSON that nobody can read.
func TestJSONLeavesMarkupCharactersAlone(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{JSON: true, Headline: headline})
	p.Print(input.Line{File: "f.txt", Num: 1, Text: "if a < b && c > d"}, nil, true)

	if !strings.Contains(buf.String(), `"if a < b && c > d"`) {
		t.Errorf("got %q, want the text unescaped", buf.String())
	}
}

// The whole prefix at once: -Z replaces only the separator after the file
// name, and the score keeps its own.
func TestScoreSitsBetweenTheNumberAndTheText(t *testing.T) {
	tests := []struct {
		name string
		opts output.Options
		want string
	}{
		{
			name: "no line number: the score is the only field",
			opts: output.Options{Score: true, Headline: headline},
			want: "0.91:a1\n",
		},
		{
			name: "with a file name and a number",
			opts: output.Options{Filenames: output.Always, LineNumber: true, Score: true, Headline: headline},
			want: "f1.txt:1:0.91:a1\n",
		},
		{
			name: "-Z takes only the separator after the file name",
			opts: output.Options{
				Filenames: output.Always, LineNumber: true, Score: true, Headline: headline, Null: true,
			},
			want: "f1.txt\x001:0.91:a1\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := output.New(&buf, tt.opts)
			p.Print(input.Line{File: "f1.txt", Num: 1, Text: "a1"}, []float64{0.91}, true)
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
