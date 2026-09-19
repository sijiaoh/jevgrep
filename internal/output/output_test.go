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
		{golden: "plain", opts: output.Options{Operands: 1}},
		{golden: "number", opts: output.Options{Operands: 1, LineNumber: true}},
		{golden: "file", opts: output.Options{Operands: 2}},
		{golden: "file-number", opts: output.Options{Operands: 2, LineNumber: true}},
	}

	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			var buf bytes.Buffer
			p := output.New(&buf, tt.opts)
			for _, l := range lines {
				p.Print(l)
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

func TestFilenameDefaultFollowsTheOperandCount(t *testing.T) {
	tests := []struct {
		name string
		opts output.Options
		want string
	}{
		{name: "no operand, so stdin", opts: output.Options{Operands: 0}, want: "x\n"},
		{name: "one operand", opts: output.Options{Operands: 1}, want: "x\n"},
		{name: "two operands", opts: output.Options{Operands: 2}, want: "notes.txt:x\n"},
		{
			name: "-H names the file even for one operand",
			opts: output.Options{Operands: 1, Filenames: output.Always},
			want: "notes.txt:x\n",
		},
		{
			name: "-H names the file with no operand at all",
			opts: output.Options{Operands: 0, Filenames: output.Always},
			want: "notes.txt:x\n",
		},
		{
			name: "-h wins over the many-operand default",
			opts: output.Options{Operands: 9, Filenames: output.Never},
			want: "x\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := output.New(&buf, tt.opts)
			p.Print(input.Line{File: "notes.txt", Num: 1, Text: "x"})
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// The milestone has no --color, and none of the fixture lines carry an escape
// of their own, so any escape in the output would be one the formatter added.
func TestPrintAddsNoEscapeSequences(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{Operands: 2, LineNumber: true})
	for _, l := range lines {
		p.Print(l)
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
	p := output.New(w, output.Options{Operands: 2, LineNumber: true})
	for _, l := range lines {
		p.Print(l)
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
	p := output.New(w, output.Options{Operands: 1})

	for i := range 5 {
		p.Print(input.Line{File: "notes.txt", Num: i + 1, Text: "x"})
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
	p := output.New(&bytes.Buffer{}, output.Options{Operands: 1})
	p.Print(input.Line{File: "notes.txt", Num: 1, Text: "x"})
	if err := p.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

// The buffer Print reuses must not leak into the next line: a short line after
// a long one is the case that would show it.
func TestPrintReusesItsBufferSafely(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{Operands: 2, LineNumber: true})
	p.Print(input.Line{File: "a-rather-long-name.txt", Num: 123456, Text: "a long line of text"})
	p.Print(input.Line{File: "b.txt", Num: 1, Text: "s"})

	want := "a-rather-long-name.txt:123456:a long line of text\nb.txt:1:s\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEveryOutputLineEndsWithNewline(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, output.Options{Operands: 2, LineNumber: true})
	for _, l := range lines {
		p.Print(l)
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
	p := output.New(&buf, output.Options{Operands: 2, LineNumber: true})
	s := search.New(scorer{score: 0.9}, expr, search.Options{Emit: p.Print})

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
