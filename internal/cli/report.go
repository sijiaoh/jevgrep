package cli

import (
	"sync"

	"github.com/sijiaoh/jevgrep/internal/input"
	"github.com/sijiaoh/jevgrep/internal/output"
	"github.com/sijiaoh/jevgrep/internal/search"
)

// opened is the inputs that were opened, in the order they were read. -c has
// to print a count for a file that produced no lines at all and -L has to list
// it, and a file that produced nothing is a file the stream of lines says
// nothing about. The reader appends to it as it goes and the sink walks it
// behind, which is why it is guarded: the two are different goroutines.
//
// A file that could not be opened is not in here. It was reported on stderr
// and it has no count to give, which is what grep prints too.
type opened struct {
	mu    sync.Mutex
	names []string
}

// add lists one opened input and returns its position, which is what the
// reader and the sink both call it: two operands can be the same path, and a
// name would make them one file to everything downstream.
func (o *opened) add(name string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.names = append(o.names, name)
	return len(o.names) - 1
}

// count is how many inputs were opened, which is the "files" line of --stats
// and of a dry run: the files that were read, not the operands that were
// named, and not the ones that could not be opened.
func (o *opened) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.names)
}

func (o *opened) at(i int) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i >= len(o.names) {
		return "", false
	}
	return o.names[i], true
}

// quotas is how the output side tells the reader that a file has given it
// everything it can use, so that the rest of that file is never sent. This is
// not about printing less: every line scored is a line paid for, and -q, -l and
// -m are the three options whose whole point is to pay for less of a file.
//
// The reader runs ahead of the verdicts by up to search's own limit, so the
// lines already in flight are still charged for. What is saved is the rest of
// the file, which is usually the bulk of it.
type quotas struct {
	mu sync.Mutex
	// none says no file will ever reach a quota, which is the common case and
	// spares the map a lock per line.
	none bool
	// all says every file has reached its quota before its first line: that is
	// -m 0, the run that reads nothing and sends nothing.
	all bool
	// files is keyed by the input's position in opened, not by its name.
	files map[int]*quota
}

// quota is one file's state. tail is the lines still to be read after the
// quota was reached: they can only be printed as -A context, so they are read
// but never sent.
//
// It counts down on the lines that would have been sent, which is the only
// kind this saves anything on. A blank one in the tail is already free and
// leaves the count where it was, so the reader gives the printer at least the
// -A lines it is owed and at most a few free ones more.
type quota struct {
	tail int
	done bool
}

func newQuotas(cfg config) *quotas {
	q := &quotas{files: make(map[int]*quota)}
	switch {
	case cfg.maxCount == 0:
		q.all = true
	// -l is the other option that gives up on a file early; -q stops the whole
	// run instead of one file, and everything else reads every file out.
	case cfg.maxCount == noMaxCount && cfg.mode() != modeFilesWithMatches:
		q.none = true
	}
	return q
}

// reach records that file has given the output everything it needs, bar tail
// more lines of context.
func (q *quotas) reach(file int, tail int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.files[file]; ok {
		return
	}
	q.files[file] = &quota{tail: tail, done: tail == 0}
}

// stopped reports that the reader can close this file and move on.
func (q *quotas) stopped(file int) bool {
	if q.all {
		return true
	}
	if q.none {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	f := q.files[file]
	return f != nil && f.done
}

// skip reports a line that must not be sent to the model. It is the tail of a
// file whose quota is reached: the context lines that are printed after the
// last line -m allowed, which are printed whatever they mean.
func (q *quotas) skip(file int) bool {
	if q.all {
		return true
	}
	if q.none {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	f := q.files[file]
	if f == nil {
		return false
	}
	if f.tail > 0 {
		f.tail--
		f.done = f.tail == 0
	}
	return true
}

// sink turns the stream of verdicts into jevgrep's output. It sees every line
// in input order, which is what lets it tell where one file ends and the next
// begins, and it is called from one goroutine only -- search's own loop.
type sink struct {
	mode    mode
	printer *output.Printer
	opened  *opened
	quotas  *quotas
	// maxCount is -m, or noMaxCount. The limit is enforced here and not in the
	// reader: the reader runs ahead, so lines past the limit can still arrive,
	// and one of them being printed would be an -m that did not hold.
	maxCount int
	// after is how many lines the reader still has to produce once a file's
	// quota is reached, so that -m and -A can both be true at once.
	after int
	// quit is -q having stopped the run, told apart from a Ctrl-C by being ours.
	quit bool
	stop func()

	matched bool

	// cursor is how far into opened this sink has settled, and the open file is
	// the one it is counting now.
	cursor int
	// index is where the open file sits in the list, which is the name the
	// reader knows it by too.
	index    int
	open     bool
	file     string
	num      int
	count    int
	listed   bool
	finished bool
}

// line receives one decided line. Unscored lines -- the ones whose batch
// failed -- are never selected, so they can only ever be printed as context.
func (s *sink) line(l input.Line, v search.Verdict, scores []float64) {
	// Line numbers restart at 1, so a number that does not carry on from the
	// last one is a new input even where the same path was given twice.
	if !s.open || l.File != s.file || l.Num <= s.num {
		s.start(l.File)
	}
	s.num = l.Num

	selected := v == search.Match && !s.spent()
	if selected {
		s.count++
		s.matched = true
	}

	switch s.mode {
	case modeQuiet:
		if selected && !s.quit {
			s.quit = true
			s.stop()
		}
	case modeFilesWithMatches:
		if selected && !s.listed {
			s.listed = true
			s.printer.Name(l.File)
			// Nothing else in this file can change the answer.
			s.quotas.reach(s.index, 0)
		}
	case modeLines:
		s.printer.Print(l, scores, selected)
	}

	// Nothing after a file's -m quota can change any of the output shapes:
	// past it no line is selected, so no count grows and no name is listed.
	// Without -m there is no quota to reach, and -c and -L then have to read
	// the whole file, because neither knows its answer before the end.
	if selected && s.spent() {
		s.quotas.reach(s.index, s.after)
	}
}

// spent reports that this file has had all the selected lines -m allows it.
func (s *sink) spent() bool {
	return s.maxCount != noMaxCount && s.count >= s.maxCount
}

// start settles the file that was being counted and picks up the next one,
// along with every input opened in between that produced no line at all.
func (s *sink) start(file string) {
	s.settleOpen()

	// Look the file up before settling anything. It is always there -- the
	// reader lists a file before it reads a line of it -- and if it ever were
	// not, walking off the end of the list would report every file after it as
	// empty, which is a wrong answer printed confidently.
	if i, ok := s.find(file); ok {
		for ; s.cursor < i; s.cursor++ {
			name, _ := s.opened.at(s.cursor)
			s.settle(name, 0)
		}
		s.cursor, s.index = i+1, i
	}

	s.open, s.file, s.num, s.count, s.listed = true, file, 0, 0, false
}

// find is where file sits in the list of opened inputs, at or after the cursor.
func (s *sink) find(file string) (int, bool) {
	for i := s.cursor; ; i++ {
		name, ok := s.opened.at(i)
		if !ok {
			return 0, false
		}
		if name == file {
			return i, true
		}
	}
}

// done settles the last file and every input that came after it without a line
// of its own. It is not called on a run that was cut short: a count for a file
// that was never read through is a wrong count, and an interrupted run has no
// business printing one.
func (s *sink) done() {
	if s.finished {
		return
	}
	s.finished = true
	s.settleOpen()
	for {
		name, ok := s.opened.at(s.cursor)
		if !ok {
			return
		}
		s.cursor++
		s.settle(name, 0)
	}
}

func (s *sink) settleOpen() {
	if !s.open {
		return
	}
	s.open = false
	s.settle(s.file, s.count)
}

// settle writes what a finished file owes the output: its count under -c, its
// name under -L when nothing in it was selected. -l has already printed its
// name at the match, and -q prints nothing at all.
func (s *sink) settle(file string, n int) {
	switch s.mode {
	case modeCount:
		// -m 0 asks for no matching lines, and grep prints no counts for it --
		// not even zeros. There is nothing to count: the file was never read.
		if s.maxCount != 0 {
			s.printer.Count(file, n)
		}
	case modeFilesWithoutMatch:
		if n == 0 {
			s.printer.Name(file)
		}
	}
}
