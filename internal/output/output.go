// Package output writes the lines jevgrep matched, in grep's format.
package output

import (
	"io"
	"strconv"

	"github.com/sijiaoh/jevgrep/internal/input"
)

// Filenames says whether output carries the file name of each line.
type Filenames int

const (
	// Auto is grep's default: name the file only when there is more than one
	// input to tell apart. The key is how many there are, not whether a name
	// happens to be available: with a single input the name is the same on every
	// line and only gets in the way of the next tool in the pipe.
	Auto Filenames = iota
	// Always is -H, Never is -h; either overrides Auto.
	Always
	Never
)

// Options configures a Printer. Its fields are the command line as parsed, not
// a decision already made, so that the one rule turning them into a decision
// lives here and is tested here.
type Options struct {
	Filenames Filenames
	// MultipleInputs says the run searches more than one input. Working that out
	// is the caller's job, not this package's: only the command line knows
	// whether an operand is a directory that -r will expand, and how many files
	// it expands to is not known when the first line is printed.
	MultipleInputs bool
	// LineNumber is -n.
	LineNumber bool
}

// Printer writes matched lines to a single writer. It is not safe for
// concurrent use: search already serializes its callback to keep the output in
// input order.
type Printer struct {
	w        io.Writer
	filename bool
	number   bool
	buf      []byte
	err      error
}

// New returns a Printer writing to w. The Printer buffers nothing between
// calls, so w has to be one that passes each write through: output is meant to
// be readable while the run is still scoring later lines.
func New(w io.Writer, opts Options) *Printer {
	return &Printer{
		w:        w,
		filename: opts.Filenames == Always || (opts.Filenames == Auto && opts.MultipleInputs),
		number:   opts.LineNumber,
	}
}

// Print writes l as one output line: FILE, line number and text joined by ":",
// with whichever prefixes are enabled, and always terminated by "\n" even when
// the input's last line had none (grep does the same).
//
// l.Text goes out byte for byte -- no escaping, no color, and no trace of the
// cleanup Line.Query does for the model.
//
// Once a write fails Print does nothing and Err reports the first failure:
// Print is the callback search hands every match to, so it cannot return one,
// and a caller that keeps printing into a closed pipe would only pile up
// identical errors.
func (p *Printer) Print(l input.Line) {
	if p.err != nil {
		return
	}
	// Assembled first and written once, so that a line reaches the pipe whole
	// rather than in pieces, and nothing is held back after it: output is read
	// live, as the scores come in.
	p.buf = p.buf[:0]
	if p.filename {
		p.buf = append(p.buf, l.File...)
		p.buf = append(p.buf, ':')
	}
	if p.number {
		p.buf = strconv.AppendInt(p.buf, int64(l.Num), 10)
		p.buf = append(p.buf, ':')
	}
	p.buf = append(p.buf, l.Text...)
	p.buf = append(p.buf, '\n')

	if _, err := p.w.Write(p.buf); err != nil {
		p.err = err
	}
}

// Err returns the first write error, if any. A caller reports it and exits 2
// rather than claiming a clean run whose output never arrived.
func (p *Printer) Err() error { return p.err }
