// Package output writes what jevgrep found, in grep's format.
package output

import (
	"bytes"
	"encoding/json"
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

// The palette, which is grep's own: those are the colors a user already reads
// as "file name" and "line number". It is fixed rather than read from
// GREP_COLORS, because an environment variable that changes what jevgrep
// prints is one more thing to explain and one more way a run differs between
// two machines.
//
// The line's text is never colored, and this is jevgrep's one deliberate
// difference from grep's palette: grep highlights the substring its pattern
// matched, and jevgrep has no substring -- a whole line in red would only be a
// line that is harder to read.
//
// grep also emits "\x1b[K" after every escape, a patch for terminals that
// repaint the background of a cleared line. Nothing jevgrep supports needs it,
// and leaving it out is what makes the golden files readable.
const (
	colorFile      = "\x1b[35m"
	colorNumber    = "\x1b[32m"
	colorSeparator = "\x1b[36m"
	colorOff       = "\x1b[m"
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
	// Null is -Z: the separator right after a file name becomes a NUL byte, and
	// a file name printed on its own is terminated by one instead of a newline.
	Null bool
	// Color says to color the output. Whether that followed from --color=auto
	// and a terminal, or from --color=always, was decided by the caller.
	Color bool
	// Before and After are -B and -A: how many lines around a selected line are
	// printed as context.
	Before, After int
	// Context says an -A, -B or -C was given at all. It is not "Before+After >
	// 0": -A0 asks for no context lines and still brings the "--" group
	// separator into existence, which is exactly what grep does and the only
	// thing this field decides.
	Context bool
	// Score is -p: the line's score goes between its number and its text.
	Score bool
	// JSON is --json, which is a format rather than a decoration: it replaces
	// the text line entirely, and with it every field the options above shape.
	// Which lines are printed is still the rest of the command line's business,
	// so -A/-B/-C keep working and the context lines come out as records of
	// their own.
	JSON bool
	// Meanings names the score at each index, for --json's "scores" object. It
	// is Expr.Meanings in its own order.
	Meanings []string
	// Headline reduces a line's scores to the one number -p prints and --json
	// calls "score". It comes from the caller because which of the meanings
	// that number is taken from is the expression's business, not this
	// package's. A run that sets Score or JSON has to set it; without it no
	// line has a score to print.
	Headline func([]float64) float64
}

// Printer writes jevgrep's output to a single writer. It is not safe for
// concurrent use: search already serializes its callback to keep the output in
// input order.
type Printer struct {
	opts     Options
	w        io.Writer
	filename bool
	buf      []byte
	err      error

	// enc and encoded write one --json record. An encoder rather than
	// json.Marshal, because only an encoder can be told to leave "<", ">" and
	// "&" alone: the lines jevgrep searches are prose and code, and a "<" that
	// came out as "\u003c" would be valid JSON nobody can read, for a benefit
	// only an HTML page would have had.
	enc     *json.Encoder
	encoded bytes.Buffer

	// before holds the lines that could still be printed as -B context, and
	// after counts the lines still owed to the last selected line as -A
	// context. Together they are all the printer keeps: the run's memory is
	// bounded by the -B the user asked for, not by the input.
	before ring
	after  int

	// curFile and curNum follow the input stream, printed or not, so that a
	// file boundary can reset the context; lastFile and lastNum follow what was
	// printed, which is what the "--" separator is about.
	curFile           string
	curNum            int
	lastFile          string
	lastNum           int
	printed, seenLine bool
}

// New returns a Printer writing to w. The Printer buffers no output between
// calls, so w has to be one that passes each write through: output is meant to
// be readable while the run is still scoring later lines. What it does hold is
// the -B lines it may still have to print, which is a bounded few.
func New(w io.Writer, opts Options) *Printer {
	p := &Printer{
		opts:     opts,
		w:        w,
		filename: opts.Filenames == Always || (opts.Filenames == Auto && opts.MultipleInputs),
		before:   newRing(opts.Before),
	}
	if opts.JSON {
		p.enc = json.NewEncoder(&p.encoded)
		p.enc.SetEscapeHTML(false)
	}
	return p
}

// Print takes every line of the input in order, selected or not, and writes
// the ones the output is meant to have: the selected lines, and the -A/-B
// context around them. A line that is not printed now may still be printed as
// context by a later selected line, which is why the unselected ones have to
// come through here too.
//
// An output line is FILE, line number and text joined by ":" for a selected
// line and by "-" for a context line, with whichever prefixes are enabled, and
// always terminated by "\n" even when the input's last line had none (grep
// does the same).
//
// scores is the line's probability per Options.Meanings, or nil for a line
// that has none: one that was never sent, or one whose batch failed. Under -p
// such a line prints "?" rather than a number.
//
// l.Text goes out byte for byte -- no escaping, no color, and no trace of the
// cleanup Line.Query does for the model. (Under --json it cannot: see record.)
//
// Once a write fails Print does nothing and Err reports the first failure:
// Print is the callback search hands every line to, so it cannot return one,
// and a caller that keeps printing into a closed pipe would only pile up
// identical errors.
func (p *Printer) Print(l input.Line, scores []float64, selected bool) {
	// A new input starts its own context: nothing before the first line of a
	// file may be printed as context for it, and nothing after the last line of
	// one is owed to a match in the file before. Line numbers restart at 1, so
	// a number that does not carry on from the last is a new input even when
	// the same path was given twice.
	if !p.seenLine || l.File != p.curFile || l.Num <= p.curNum {
		p.before.reset()
		p.after = 0
	}
	p.curFile, p.curNum, p.seenLine = l.File, l.Num, true

	switch {
	case selected:
		for _, h := range p.before.drain() {
			p.emit(h, false)
		}
		p.emit(held{line: l, scores: scores}, true)
		p.after = p.opts.After
	case p.after > 0:
		p.after--
		p.emit(held{line: l, scores: scores}, false)
	default:
		// The scores are kept with the line: search hands a non-nil slice to
		// this caller for good, so holding one back for -B costs nothing but
		// the space it already takes.
		p.before.push(held{line: l, scores: scores})
	}
}

// Name prints a file name on its own, for -l and -L. Under -Z it is terminated
// by a NUL and no newline, which is the shape `xargs -0` reads and grep's own.
func (p *Printer) Name(file string) {
	if p.err != nil {
		return
	}
	p.buf = p.buf[:0]
	p.buf = p.appendFile(p.buf, file)
	if p.opts.Null {
		p.buf = append(p.buf, 0)
	} else {
		p.buf = append(p.buf, '\n')
	}
	p.write()
}

// Count prints one file's number of selected lines, for -c. The count itself
// is not colored, as it is not in grep: it is the value, not a label.
func (p *Printer) Count(file string, n int) {
	if p.err != nil {
		return
	}
	p.buf = p.buf[:0]
	if p.filename {
		p.buf = p.appendFile(p.buf, file)
		p.buf = p.appendFileSeparator(p.buf, true)
	}
	p.buf = strconv.AppendInt(p.buf, int64(n), 10)
	p.buf = append(p.buf, '\n')
	p.write()
}

// emit writes one line of output in whichever format was asked for, preceded
// by the group separator when it does not carry on from the last one printed.
func (p *Printer) emit(h held, selected bool) {
	if p.err != nil {
		return
	}
	// The separator says "lines were left out here", which is a thing to say
	// in a column of text. A stream of JSON records carries the line numbers
	// in the records themselves, and a line of "--" in it would not even parse.
	if !p.opts.JSON && p.opts.Context && p.printed && (h.line.File != p.lastFile || h.line.Num != p.lastNum+1) {
		p.buf = p.buf[:0]
		p.buf = p.appendColored(p.buf, colorSeparator, "--")
		p.buf = append(p.buf, '\n')
		p.write()
	}
	p.lastFile, p.lastNum, p.printed = h.line.File, h.line.Num, true

	if p.opts.JSON {
		p.record(h, selected)
		return
	}
	p.line(h, selected)
}

// line writes one line of jevgrep's text output.
func (p *Printer) line(h held, selected bool) {
	// Assembled first and written once, so that a line reaches the pipe whole
	// rather than in pieces, and nothing is held back after it: output is read
	// live, as the scores come in.
	p.buf = p.buf[:0]
	if p.filename {
		p.buf = p.appendFile(p.buf, h.line.File)
		p.buf = p.appendFileSeparator(p.buf, selected)
	}
	if p.opts.LineNumber {
		p.buf = p.appendColored(p.buf, colorNumber, strconv.Itoa(h.line.Num))
		p.buf = p.appendSeparator(p.buf, selected)
	}
	if p.opts.Score {
		// Colored like the line number: both are what the line is, printed in
		// front of what the line says.
		p.buf = p.appendColored(p.buf, colorNumber, p.scoreText(h.scores))
		p.buf = p.appendSeparator(p.buf, selected)
	}
	p.buf = append(p.buf, h.line.Text...)
	p.buf = append(p.buf, '\n')
	p.write()
}

// noScore is what -p prints for a line that has none: a blank line, which was
// never sent, or a line whose batch failed. One character, and not one that
// could be read as a number or confused with the "-" separator.
//
// A blank line is decided as a zero, but that zero is a stand-in this program
// put there, not something the model was asked. Printing it as 0.00 would be
// inventing an answer from the API, in the one column a user would take at
// face value.
const noScore = "?"

// scoreText is the score as -p writes it: always four characters, 0.00 to
// 1.00. The fixed width is not for alignment -- file names and line numbers
// are not aligned either -- but so that the field is one shape: golden output
// stays stable, and `awk -F: '$2 > 0.9'` is not tripped up by a column that is
// sometimes "1" and sometimes "1.0".
func (p *Printer) scoreText(scores []float64) string {
	s, ok := p.score(scores)
	if !ok {
		return noScore
	}
	return strconv.FormatFloat(s, 'f', 2, 64)
}

// score is the one number -p and --json report for a line, and whether the
// line has one at all.
func (p *Printer) score(scores []float64) (float64, bool) {
	if scores == nil || p.opts.Headline == nil {
		return 0, false
	}
	return p.opts.Headline(scores), true
}

// record is one line of --json output. The field order is this declaration's,
// which is what makes the output golden-testable.
type record struct {
	// Type is always "line". It is there so that the stats record --stats will
	// add has somewhere to say that it is not a line, without the readers
	// written today having to guess.
	Type string `json:"type"`
	File string `json:"file"`
	Line int    `json:"line"`
	// Text is the one place jevgrep's output is not the input byte for byte:
	// JSON is UTF-8, so a byte that is not valid UTF-8 becomes U+FFFD here.
	// That is JSON's limit, not a cleanup -- what Line.Query does for the model
	// still never reaches the output.
	Text string `json:"text"`
	// Score is the number -p prints, absent on a line that has none.
	Score *float64 `json:"score,omitempty"`
	// Scores is every meaning's probability, empty on a line that has none.
	// Full precision on purpose: JSON is read by programs, and rounding it to
	// what a terminal column can show would throw away what they came for.
	Scores map[string]float64 `json:"scores"`
	// Selected is false only on a context line.
	Selected bool `json:"selected"`
}

func (p *Printer) record(h held, selected bool) {
	rec := record{
		Type:     "line",
		File:     h.line.File,
		Line:     h.line.Num,
		Text:     h.line.Text,
		Scores:   map[string]float64{},
		Selected: selected,
	}
	// Indexed by Meanings, which is the contract Print states: a slice of a
	// different length is a caller that scored a different expression, and
	// this is not the place to paper over that with a record missing a
	// meaning nobody would notice was gone.
	if h.scores != nil {
		for i, meaning := range p.opts.Meanings {
			rec.Scores[meaning] = h.scores[i]
		}
	}
	if s, ok := p.score(h.scores); ok {
		rec.Score = &s
	}

	p.encoded.Reset()
	// The only error an encoder can return here is the writer's, and this one
	// is a buffer: record holds nothing json cannot encode.
	if err := p.enc.Encode(rec); err != nil {
		p.err = err
		return
	}
	// Encode has already put the newline on.
	p.buf = append(p.buf[:0], p.encoded.Bytes()...)
	p.write()
}

func (p *Printer) appendFile(b []byte, file string) []byte {
	return p.appendColored(b, colorFile, file)
}

// appendFileSeparator writes the separator that follows a file name. Under -Z
// it is a NUL, and only this one is: the separator after the line number stays
// what it was, which is what grep writes too. A NUL is not a glyph, so it is
// not colored either.
func (p *Printer) appendFileSeparator(b []byte, selected bool) []byte {
	if p.opts.Null {
		return append(b, 0)
	}
	return p.appendSeparator(b, selected)
}

// appendSeparator writes the ":" of a selected line or the "-" of a context
// one.
func (p *Printer) appendSeparator(b []byte, selected bool) []byte {
	if selected {
		return p.appendColored(b, colorSeparator, ":")
	}
	return p.appendColored(b, colorSeparator, "-")
}

func (p *Printer) appendColored(b []byte, color, s string) []byte {
	if !p.opts.Color {
		return append(b, s...)
	}
	b = append(b, color...)
	b = append(b, s...)
	return append(b, colorOff...)
}

// write puts one assembled piece of output out, and does nothing once a write
// has failed: the group separator and the line after it are two writes, and
// the first error is the one worth reporting.
func (p *Printer) write() {
	if p.err != nil {
		return
	}
	if _, err := p.w.Write(p.buf); err != nil {
		p.err = err
	}
}

// Err returns the first write error, if any. A caller reports it and exits 2
// rather than claiming a clean run whose output never arrived.
func (p *Printer) Err() error { return p.err }

// held is a line together with what the model said about it, which is what
// -p and --json print and therefore what the -B buffer has to keep.
type held struct {
	line   input.Line
	scores []float64
}

// ring holds the last -B lines read, so that a selected line can be given the
// context that came before it. It is a fixed array because the lines it holds
// are the only ones the output side keeps, and that number has to stay the one
// the user asked for.
type ring struct {
	buf   []held
	start int
	n     int
	// out is reused by drain, whose result the caller only reads before the
	// next push.
	out []held
}

func newRing(size int) ring {
	if size <= 0 {
		return ring{}
	}
	return ring{buf: make([]held, size), out: make([]held, 0, size)}
}

func (r *ring) push(h held) {
	if len(r.buf) == 0 {
		return
	}
	r.buf[(r.start+r.n)%len(r.buf)] = h
	if r.n < len(r.buf) {
		r.n++
		return
	}
	r.start = (r.start + 1) % len(r.buf)
}

// drain returns the held lines oldest first and empties the ring: a line is
// printed as context once, and a later selected line must not print it again.
func (r *ring) drain() []held {
	r.out = r.out[:0]
	for i := range r.n {
		r.out = append(r.out, r.buf[(r.start+i)%len(r.buf)])
	}
	r.start, r.n = 0, 0
	return r.out
}

func (r *ring) reset() { r.start, r.n = 0, 0 }
