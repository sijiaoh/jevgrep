package input_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sijiaoh/jevgrep/internal/input"
)

// queryLimit mirrors the package's unexported cap on the bytes one line
// contributes to a request.
const queryLimit = 4096

// errReader fails every read, standing in for a file that goes away mid-scan.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// scanAll drains sc and returns every line it produced.
func scanAll(t *testing.T, sc *input.Scanner) []input.Line {
	t.Helper()

	var got []input.Line
	for sc.Scan() {
		got = append(got, sc.Line())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
	if err := sc.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	return got
}

// writeFile writes content verbatim, so a case can pin line terminators.
func writeFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

func TestScanSplitsLines(t *testing.T) {
	long := strings.Repeat("x", 200_000)

	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "empty input has no lines",
			content: "",
		},
		{
			name:    "trailing newline does not add an empty line",
			content: "a\nb\n",
			want:    []string{"a", "b"},
		},
		{
			name:    "a missing final newline still yields the last line",
			content: "a\nb",
			want:    []string{"a", "b"},
		},
		{
			name:    "blank lines are kept and numbered",
			content: "a\n\nb\n",
			want:    []string{"a", "", "b"},
		},
		{
			name: "CRLF keeps the carriage return in the text",
			// Output has to reproduce the file byte for byte; Query is what drops
			// the "\r".
			content: "a\r\nb\r\n",
			want:    []string{"a\r", "b\r"},
		},
		{
			name:    "a line far past any buffer size is read whole",
			content: long + "\ntail\n",
			want:    []string{long, "tail"},
		},
		{
			name: "invalid UTF-8 reaches Text unchanged",
			// Only Query may repair bytes; see TestQuery.
			content: "a\xffb\n",
			want:    []string{"a\xffb"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc, err := input.Open(writeFile(t, tt.content), nil)
			if err != nil {
				t.Fatalf("Open() = %v, want nil", err)
			}

			got := scanAll(t, sc)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d lines, want %d", len(got), len(tt.want))
			}
			for i, line := range got {
				if line.Text != tt.want[i] {
					t.Errorf("line %d text = %q, want %q", i+1, line.Text, tt.want[i])
				}
				if line.Num != i+1 {
					t.Errorf("line %d has Num = %d, want %d", i+1, line.Num, i+1)
				}
			}
		})
	}
}

func TestScanReportsTheFileName(t *testing.T) {
	path := writeFile(t, "a\n")

	sc, err := input.Open(path, nil)
	if err != nil {
		t.Fatalf("Open() = %v, want nil", err)
	}
	got := scanAll(t, sc)

	if len(got) != 1 || got[0].File != path {
		t.Errorf("File = %q, want %q", got[0].File, path)
	}
}

func TestOpenReadsStdin(t *testing.T) {
	sc, err := input.Open(input.StdinPath, strings.NewReader("a\nb\n"))
	if err != nil {
		t.Fatalf("Open() = %v, want nil", err)
	}

	got := scanAll(t, sc)
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	// Closing a Scanner over stdin must not close the process's own stdin, which
	// a later PATH operand of "-" may still need; scanAll already asserted that
	// Close succeeded, so only the reported name is left to pin.
	if got[0].File != input.StdinName {
		t.Errorf("File = %q, want %q", got[0].File, input.StdinName)
	}
}

func TestOpenErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.txt")

	tests := []struct {
		name    string
		path    string
		wantErr error
	}{
		{name: "a missing file", path: missing, wantErr: fs.ErrNotExist},
		{name: "a directory", path: dir, wantErr: input.ErrIsDirectory},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc, err := input.Open(tt.path, nil)
			if err == nil {
				_ = sc.Close()
				t.Fatalf("Open() = nil, want %v", tt.wantErr)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Open() = %v, want %v", err, tt.wantErr)
			}
			// The caller prints this to stderr, so it has to name the input it is
			// about and nothing else.
			if !strings.Contains(err.Error(), tt.path) {
				t.Errorf("error %q does not name %q", err, tt.path)
			}
		})
	}
}

func TestQuery(t *testing.T) {
	// One rune over the cap, so the cut has to land on a boundary rather than
	// inside the multi-byte rune that straddles it.
	overlong := strings.Repeat("a", queryLimit-1) + "日本語"

	tests := []struct {
		name     string
		text     string
		want     string
		wantSend bool
	}{
		{name: "a plain line is sent as is", text: "hello", want: "hello", wantSend: true},
		{name: "an empty line is not sent", text: ""},
		{name: "a whitespace-only line is not sent", text: " \t "},
		{name: "a CRLF blank line is not sent", text: "\r"},
		{
			name: "the carriage return of a CRLF line is dropped",
			text: "hello\r", want: "hello", wantSend: true,
		},
		{
			name: "leading whitespace is kept, since indentation is part of code",
			text: "\tif err != nil {", want: "\tif err != nil {", wantSend: true,
		},
		{
			name: "invalid bytes are replaced so the request stays valid UTF-8",
			text: "a\xffb", want: "a�b", wantSend: true,
		},
		{
			name: "a long line is truncated on a rune boundary",
			text: overlong, want: strings.Repeat("a", queryLimit-1), wantSend: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, send := input.Line{Text: tt.text}.Query()

			if send != tt.wantSend {
				t.Fatalf("Query() send = %v, want %v", send, tt.wantSend)
			}
			if got != tt.want {
				t.Errorf("Query() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Truncation changes only what is sent; the printed line stays whole.
func TestQueryLeavesTextAlone(t *testing.T) {
	line := input.Line{Text: strings.Repeat("x", 10_000)}

	query, send := line.Query()
	if !send {
		t.Fatal("Query() send = false, want true")
	}
	if len(query) >= len(line.Text) {
		t.Errorf("query is %d bytes, want it shorter than the %d-byte line", len(query), len(line.Text))
	}
	if len(line.Text) != 10_000 {
		t.Errorf("Text is %d bytes, want it left at 10000", len(line.Text))
	}
}

// The cap is on what actually goes into a request, so nothing Query does to a
// line on its way there may push it back over — least of all replacing invalid
// bytes, which turns each one into the three bytes of U+FFFD.
func TestQueryNeverExceedsTheCap(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "plain ASCII", text: strings.Repeat("a", queryLimit*2)},
		{name: "multi-byte runes", text: strings.Repeat("日", queryLimit)},
		// Interleaved, not a run: repair collapses a run of invalid bytes into a
		// single replacement, so only this shape actually grows the line.
		{name: "invalid bytes interleaved with valid ones", text: strings.Repeat("a\xff", queryLimit)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, send := input.Line{Text: tt.text}.Query()

			if !send {
				t.Fatal("Query() send = false, want true")
			}
			if len(got) > queryLimit {
				t.Errorf("Query() is %d bytes, want at most %d", len(got), queryLimit)
			}
			// A request that is not valid UTF-8 cannot be encoded as JSON at all.
			if !utf8.ValidString(got) {
				t.Error("Query() is not valid UTF-8")
			}
		})
	}
}

// A read that fails partway has to stop the scan and leave its reason in Err:
// that is what lets the caller report this one input and carry on with the rest.
func TestScanStopsOnAReadError(t *testing.T) {
	const secret = "swordfish"

	want := errors.New("disk went away")
	sc, err := input.Open(input.StdinPath, io.MultiReader(strings.NewReader(secret+"\n"), errReader{want}))
	if err != nil {
		t.Fatalf("Open() = %v, want nil", err)
	}

	if !sc.Scan() {
		t.Fatal("Scan() = false on the first line, want true")
	}
	for range 2 {
		if sc.Scan() {
			t.Fatal("Scan() = true after a read error, want false")
		}
	}
	if !errors.Is(sc.Err(), want) {
		t.Errorf("Err() = %v, want %v", sc.Err(), want)
	}
	// The caller prints this; the line it was reading must not ride along.
	if strings.Contains(sc.Err().Error(), secret) {
		t.Errorf("Err() = %q, want it free of line content", sc.Err())
	}
}
