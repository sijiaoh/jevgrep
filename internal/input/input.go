// Package input turns a PATH operand into the individual lines jevgrep scores.
package input

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// StdinPath is the PATH operand standing for stdin, and StdinName is the file
// name its lines report. Both are grep's, because jevgrep's output is meant to
// drop into the same pipelines.
const (
	StdinPath = "-"
	StdinName = "(standard input)"
)

// ErrIsDirectory is returned by Open for a directory. Expanding one is -r, and
// the caller decides that before it opens anything; reaching here means the
// user gave a directory without asking for -r, which is a mistake worth naming
// rather than a silent empty result.
var ErrIsDirectory = errors.New("is a directory")

// maxQueryBytes caps what one line contributes to a request. Prose and code
// lines sit far below it; a line above it is in practice a minified bundle, a
// base64 blob or a dumped payload, whose meaning the first few KB already
// settle. The cap earns its place because a request carries a whole batch of
// lines against a fixed token limit, and every byte in it is billed: without it
// a single pathological line would decide the cost and the batch size of the
// whole run.
const maxQueryBytes = 4096

// Line is one line of input awaiting a match decision.
type Line struct {
	// File is the path exactly as the user spelled it on the command line, or
	// StdinName; output echoes it back.
	File string
	// Num counts from 1, like grep's -n.
	Num int
	// Text is the line as read, minus the trailing "\n". The "\r" of a CRLF
	// terminator stays: output must reproduce the input byte for byte, and
	// cleaning a line up is Query's job alone.
	Text string
}

// Query reports what to send to the model for l, and whether l is worth sending
// at all. The returned text may differ from Text; the difference never reaches
// the output, which always prints Text.
func (l Line) Query() (string, bool) {
	s := strings.TrimSuffix(l.Text, "\r")
	// A line with nothing but whitespace has no meaning to ask about, and every
	// line sent is a paid request, so it is never sent (§6) and never matches.
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	// Bytes that are not UTF-8 cannot go into a JSON request at all. Replacing
	// them keeps the rest of the line searchable instead of dropping the line.
	// Repairing before truncating, not after: a run of bad bytes becomes the
	// three bytes of one U+FFFD, so the other order would let a line of
	// scattered invalid bytes ship up to three times the cap.
	return truncate(strings.ToValidUTF8(s, "�")), true
}

// truncate cuts s to at most maxQueryBytes, backing up to a rune boundary so
// that the cut never splits a rune into invalid UTF-8.
func truncate(s string) string {
	if len(s) <= maxQueryBytes {
		return s
	}
	end := maxQueryBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// Scanner reads one input, a file or stdin, line by line.
type Scanner struct {
	name string
	r    *bufio.Reader
	// close is nil for stdin: the process's own stdin is not ours to close.
	close func() error
	num   int
	line  Line
	err   error
	done  bool
}

// Open prepares name for reading, taking StdinPath to mean the given stdin. Any
// error it returns concerns this one input and carries no line content, so the
// caller can report it and go on with the remaining paths (§4).
func Open(name string, stdin io.Reader) (*Scanner, error) {
	if name == StdinPath {
		return &Scanner{name: StdinName, r: bufio.NewReader(stdin)}, nil
	}

	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	// A directory opens happily on Unix and only fails on the first read, with a
	// message that differs per platform. Rejecting it here makes one answer.
	if info.IsDir() {
		_ = f.Close()
		return nil, fmt.Errorf("%s: %w", name, ErrIsDirectory)
	}
	return &Scanner{name: name, r: bufio.NewReader(f), close: f.Close}, nil
}

// Scan advances to the next line and reports whether there was one. Input that
// does not end in a newline still yields its last line.
func (s *Scanner) Scan() bool {
	if s.done {
		return false
	}

	// ReadString rather than bufio.Scanner: a line over the scanner's buffer
	// limit is a case jevgrep must handle, not refuse, because Text has to stay
	// the whole line even when only its first maxQueryBytes are searched.
	text, err := s.r.ReadString('\n')
	switch {
	case err == nil:
		text = text[:len(text)-1]
	case errors.Is(err, io.EOF):
		s.done = true
		if text == "" {
			return false
		}
	default:
		s.done = true
		s.err = err
		return false
	}

	s.num++
	s.line = Line{File: s.name, Num: s.num, Text: text}
	return true
}

// Line returns the line found by the most recent call to Scan that returned
// true.
func (s *Scanner) Line() Line { return s.line }

// Err reports the read error that stopped Scan early, if any. Reaching the end
// of the input is not one.
func (s *Scanner) Err() error { return s.err }

// Close releases the file, and does nothing when the input is stdin.
func (s *Scanner) Close() error {
	if s.close == nil {
		return nil
	}
	return s.close()
}
