package search

import (
	"context"
	"errors"
	"iter"
	"slices"
	"sync"

	"github.com/sijiaoh/jevgrep/internal/input"
	"github.com/sijiaoh/jevgrep/internal/jev"
)

// defaultConcurrency is how many requests are kept in flight. The work is
// entirely network-bound -- a batch of 30 lines answers in about a second (§2)
// -- so the number is a throughput knob, not a CPU one: eight keeps a few
// hundred lines a second moving without looking to the server like a flood.
// -j/--jobs, which lets the user pick, is a later milestone; Options.Concurrency
// is the seam it will plug into.
const defaultConcurrency = 8

// maxPending caps how many lines may be waiting for a verdict at once, which
// is what keeps jevgrep's memory flat on an input of any size. The requests in
// flight bound only the lines that were sent: a line with nothing to ask about
// (a blank one) never takes a slot, so a long enough run of them would
// otherwise pile up unbounded behind one line still being scored -- measured at
// 450 MB for a 3 MB file of blank lines. A thousand lines is far more than the
// concurrency ever has in flight, so nothing but that runaway is slowed down.
const maxPending = 1024

// Scorer is the part of *jev.Client the scheduler needs. It is an interface so
// that ordering, failure and cancellation can be tested without a server.
type Scorer interface {
	// Score returns exactly one probability per line, in order. *jev.Client
	// checks that the server answered every line before returning.
	Score(ctx context.Context, meaning string, lines []string) ([]float64, error)
}

// Failure is a batch of lines that could not be scored, named by the range of
// input lines it covers so the caller can say which part of which file was
// skipped. The lines are reported as Unscored: they are never selected,
// whether or not -v is in effect, so the output can only ever show them as
// context.
type Failure struct {
	File        string
	First, Last int
	Err         error
}

// Verdict is what the expression had to say about one line.
type Verdict int

const (
	// NoMatch is a decided line the expression rejected. A line that was never
	// sent -- a blank one -- lands here too, or in Match, because -v turns it
	// over: it has a verdict, it just has no scores.
	NoMatch Verdict = iota
	// Match is a decided line the expression selected.
	Match
	// Unscored is a line whose batch failed. It was neither selected nor
	// rejected, so it can only ever be printed as context. This is not the same
	// as having no scores: a blank line has no scores and still has a verdict.
	Unscored
)

// Options configures a Searcher. The zero value is usable: the callbacks are
// optional and the concurrency falls back to the default.
type Options struct {
	// Concurrency caps the requests in flight. Zero means defaultConcurrency.
	Concurrency int
	// Emit receives every line, exactly once, in the order the lines were read,
	// as soon as that line is decided -- not only the matching ones, because
	// -A/-B/-C print lines that did not match, -c counts and -L has to know
	// that a file had nothing. Formatting them is the caller's job.
	//
	// scores gives one probability per Expr.Meanings, by index. A nil scores
	// means this line has no scores at all: either it was never sent (a blank
	// line) or its batch failed. A non-nil slice belongs to the caller from
	// then on; Run never writes to it again, so a printer holding lines back
	// for -B may keep it.
	//
	// Emit sees every line but the caller need not keep every line: the
	// verdicts arrive in input order, so a printer holds at most the -B lines
	// it may still have to print, and maxPending stays the one thing bounding
	// what this run has in memory.
	Emit func(l input.Line, v Verdict, scores []float64)
	// Skip reports a line the caller already knows the verdict of cannot
	// matter for -- everything past a file's -m quota, say. Such a line is
	// never sent to the model and is emitted Unscored. It is the same saving
	// the reader makes by dropping the rest of a file, for the lines it cannot
	// drop: every line sent is a paid request.
	//
	// It is called on the reader's goroutine, in input order, and only for the
	// lines that would otherwise be sent: one with nothing to ask about is
	// already free.
	Skip func(input.Line) bool
	// Fail receives every batch that could not be scored, in line order. A
	// line is in one batch per meaning, so a run that asks two things of the
	// same line can report it twice -- two requests failed, and both were
	// charged for.
	Fail func(Failure)
}

// Searcher scores a stream of lines against an expression.
type Searcher struct {
	scorer Scorer
	expr   *Expr

	concurrency int
	emit        func(input.Line, Verdict, []float64)
	fail        func(Failure)
	skip        func(input.Line) bool
}

// New returns a Searcher that scores lines with scorer and decides them with
// expr.
func New(scorer Scorer, expr *Expr, opts Options) *Searcher {
	s := &Searcher{
		scorer:      scorer,
		expr:        expr,
		concurrency: opts.Concurrency,
		emit:        opts.Emit,
		fail:        opts.Fail,
		skip:        opts.Skip,
	}
	if s.concurrency < 1 {
		s.concurrency = defaultConcurrency
	}
	if s.emit == nil {
		s.emit = func(input.Line, Verdict, []float64) {}
	}
	if s.fail == nil {
		s.fail = func(Failure) {}
	}
	if s.skip == nil {
		s.skip = func(input.Line) bool { return false }
	}
	return s
}

// Run reads lines, scores them in parallel and reports each one through
// Options.Emit in input order, without waiting for the whole input.
//
// Batches that fail go to Options.Fail and the run carries on, so Run returns
// nil for them; what it does return ends the whole run: a *jev.AuthError, for
// which every later request would fail the same way, or ctx's error when the
// caller cancelled. Whatever was emitted before that stands.
func (s *Searcher) Run(ctx context.Context, lines iter.Seq[input.Line]) error {
	meanings := s.expr.Meanings()

	r := &run{
		Searcher: s,
		meanings: meanings,
		// Scores of a line that was never sent. Shared and never written to:
		// blank lines are common enough that a slice each would be waste.
		zeros:   make([]float64, len(meanings)),
		events:  make(chan event, 2*s.concurrency),
		slots:   make(chan struct{}, s.concurrency),
		pending: make(chan struct{}, maxPending),
	}
	r.ctx, r.cancel = context.WithCancel(ctx)
	defer r.cancel()

	go func() {
		r.read(lines)
		// The reader adds to the group before any worker can finish, so waiting
		// here cannot race with a late dispatch.
		r.workers.Wait()
		close(r.events)
	}()

	for {
		select {
		// Ctrl-C has to be answered here rather than by waiting for the
		// goroutines: the reader can be parked in a read on a pipe that will
		// never produce another line, and the user is owed their prompt back.
		// The deferred cancel releases everyone; nothing calls back into the
		// caller once this returns, because only this loop does.
		case <-ctx.Done():
			return ctx.Err()

		case ev, ok := <-r.events:
			// select picks at random between two ready cases, so cancellation
			// has to be checked here too, twice over. A run that was stopped
			// must not report that its in-flight batches could not be scored,
			// which is not news the user needs -- and must not come back
			// without an error just because the input happened to run out
			// while the user was interrupting it.
			if err := ctx.Err(); err != nil {
				return err
			}
			if !ok {
				return nil
			}
			// A rejected key is the one failure worth abandoning the run for,
			// and in-flight batches are not waited for (§5.3).
			if err := r.handle(ev); err != nil {
				return err
			}
		}
	}
}

// run is the state of one Run: the goroutines it started and the records it is
// waiting on. Everything in records is touched only by Run's own loop.
type run struct {
	*Searcher

	ctx      context.Context
	cancel   context.CancelFunc
	meanings []string
	zeros    []float64

	events chan event
	slots  chan struct{}
	// pending holds one token per line read but not yet decided; flush gives
	// them back.
	pending chan struct{}
	workers sync.WaitGroup

	// records holds every line read but not yet decided, oldest first, and head
	// is the sequence number of records[0].
	records []*record
	head    int
}

// record is a line waiting for its verdict.
type record struct {
	line  input.Line
	score []float64
	// scored says the line was sent, so score is this record's own and may be
	// handed to the caller. An unsent line shares r.zeros, which is not.
	scored bool
	// pending counts the meanings this line is still waiting on.
	pending int
	// unscored marks a line whose batch failed, or one the caller asked not to
	// send: it is reported as Unscored rather than decided, because a missing
	// score is not a score of zero. A failure does not by itself finish the
	// line -- it counts down pending like a score does, so that a record
	// outlives every batch that names it.
	unscored bool
	// failures are reported when this record reaches the front of the queue,
	// so that what lands on stderr is in line order like the matches are.
	failures []Failure
}

func (r *record) decided() bool { return r.pending == 0 }

type eventKind int

const (
	kindLine eventKind = iota
	kindScores
	kindFailed
)

// event is what the reader and the workers tell Run's loop. Routing everything
// through one channel is what keeps the queue of undecided lines free of locks.
type event struct {
	kind eventKind

	// line and seq describe a line that was read (kindLine); scored says
	// whether it is one the model will be asked about, and skipped says the
	// caller waved it through unjudged.
	line    input.Line
	seq     int
	scored  bool
	skipped bool

	// seqs are the records a finished batch covers, in request order, and
	// meaning is which meaning it was scored against (kindScores, kindFailed).
	seqs    []int
	meaning int
	scores  []float64
	// covered and err describe a batch that failed (kindFailed); the lines are
	// what turns it into a report the user can act on.
	covered []input.Line
	err     error
}

func (r *run) handle(ev event) error {
	switch ev.kind {
	case kindLine:
		// A line that is not sent keeps the shared zeros to be decided with --
		// that is what makes a blank line never match and always be selected
		// under -v -- but it is emitted with no scores at all, because zero is
		// a stand-in and not something the model ever said.
		rec := &record{line: ev.line, score: r.zeros, unscored: ev.skipped, scored: ev.scored}
		if ev.scored {
			rec.score = make([]float64, len(r.meanings))
			rec.pending = len(r.meanings)
		}
		r.records = append(r.records, rec)

	case kindScores:
		for i, seq := range ev.seqs {
			rec := r.record(seq)
			rec.score[ev.meaning] = ev.scores[i]
			rec.pending--
		}

	case kindFailed:
		var authErr *jev.AuthError
		if errors.As(ev.err, &authErr) {
			return authErr
		}
		for _, seq := range ev.seqs {
			rec := r.record(seq)
			rec.unscored = true
			rec.pending--
		}
		first := r.record(ev.seqs[0])
		first.failures = append(first.failures, failures(ev.covered, ev.err)...)
	}

	r.flush()
	return nil
}

// record returns the record with this sequence number. A batch's records are
// always still queued when its result arrives: they were sent before it, and a
// line is not decided -- so not dropped from the queue -- while any meaning is
// still outstanding.
func (r *run) record(seq int) *record {
	return r.records[seq-r.head]
}

// flush reports every record at the front of the queue that is decided. A line
// is emitted only once every line before it has been, which is what keeps the
// output in input order while the scoring finishes out of order.
func (r *run) flush() {
	for len(r.records) > 0 && r.records[0].decided() {
		rec := r.records[0]
		r.records[0] = nil
		r.records = r.records[1:]
		r.head++
		<-r.pending

		for _, f := range rec.failures {
			r.fail(f)
		}
		switch {
		case rec.unscored:
			r.emit(rec.line, Unscored, nil)
		case r.expr.Match(rec.score):
			r.emit(rec.line, Match, r.scores(rec))
		default:
			r.emit(rec.line, NoMatch, r.scores(rec))
		}
	}
}

// scores are the probabilities to report for a decided record, or nil when it
// was never sent.
func (r *run) scores(rec *record) []float64 {
	if !rec.scored {
		return nil
	}
	return rec.score
}

// queued is a line the reader is holding until it has a full batch for every
// meaning.
type queued struct {
	seq   int
	line  input.Line
	query string
}

// read pulls the input and dispatches batches, announcing every line to Run's
// loop as it goes so that the loop can put results back in order.
func (r *run) read(lines iter.Seq[input.Line]) {
	var (
		buf []queued
		// sent[i] is how many of buf have already been dispatched for
		// meanings[i]. Each meaning is batched separately: the meaning is
		// repeated in every question, so it is part of what decides where a
		// batch has to end.
		sent = make([]int, len(r.meanings))
		seq  = 0
		done = false
	)

	lines(func(line input.Line) bool {
		query, scored := line.Query()
		skipped := false
		if scored && r.skip(line) {
			scored, skipped = false, true
		}
		if !r.reserve(&buf, sent) {
			done = true
			return false
		}
		if !r.send(event{kind: kindLine, line: line, seq: seq, scored: scored, skipped: skipped}) {
			done = true
			return false
		}
		if scored {
			buf = append(buf, queued{seq: seq, line: line, query: query})
			if !r.dispatch(&buf, sent, false) {
				done = true
				return false
			}
		}
		seq++
		return true
	})

	if !done {
		r.dispatch(&buf, sent, true)
	}
}

// reserve takes this line's place in the queue of undecided lines, waiting
// while the queue is full.
//
// Before waiting it sends off everything still buffered, even a part-full
// batch. The queue only ever drains from its front, so waiting without doing
// that could be waiting on a verdict for a line that had never been asked
// about -- a deadlock. The extra request it can cost is paid only on input
// that would otherwise have been read into memory whole.
func (r *run) reserve(buf *[]queued, sent []int) bool {
	select {
	case r.pending <- struct{}{}:
		return true
	default:
	}

	if !r.dispatch(buf, sent, true) {
		return false
	}
	select {
	case r.pending <- struct{}{}:
		return true
	case <-r.ctx.Done():
		return false
	}
}

// dispatch sends off every batch that is complete. Unless the input is over,
// the last batch a split produces is left behind: another line may still belong
// in it, and sending a half-full batch costs an extra request.
func (r *run) dispatch(buf *[]queued, sent []int, final bool) bool {
	for i, meaning := range r.meanings {
		waiting := (*buf)[sent[i]:]
		chunks := jev.Split(meaning, queries(waiting))
		if !final {
			chunks = chunks[:max(len(chunks)-1, 0)]
		}
		for _, chunk := range chunks {
			if !r.score(i, waiting[chunk.Offset:chunk.Offset+len(chunk.Lines)]) {
				return false
			}
		}
		if n := len(chunks); n > 0 {
			sent[i] += chunks[n-1].Offset + len(chunks[n-1].Lines)
		}
	}

	// Drop what every meaning is done with. The queue is bounded because
	// dispatching blocks once every worker is busy.
	if keep := slices.Min(sent); keep > 0 {
		*buf = append((*buf)[:0], (*buf)[keep:]...)
		for i := range sent {
			sent[i] -= keep
		}
	}
	return true
}

// score sends one batch to the model in the background. It blocks while every
// worker is busy, which is also what stops the reader from running away with
// the whole input.
func (r *run) score(meaning int, batch []queued) bool {
	seqs := make([]int, len(batch))
	texts := make([]string, len(batch))
	covered := make([]input.Line, len(batch))
	for i, q := range batch {
		seqs[i], texts[i], covered[i] = q.seq, q.query, q.line
	}

	select {
	case r.slots <- struct{}{}:
	case <-r.ctx.Done():
		return false
	}

	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		defer func() { <-r.slots }()

		scores, err := r.scorer.Score(r.ctx, r.meanings[meaning], texts)
		if err != nil {
			r.send(event{kind: kindFailed, seqs: seqs, covered: covered, err: err})
			return
		}
		r.send(event{kind: kindScores, seqs: seqs, meaning: meaning, scores: scores})
	}()
	return true
}

func (r *run) send(ev event) bool {
	select {
	case r.events <- ev:
		return true
	case <-r.ctx.Done():
		return false
	}
}

func queries(batch []queued) []string {
	texts := make([]string, len(batch))
	for i, q := range batch {
		texts[i] = q.query
	}
	return texts
}

// failures describes a failed batch as one report per file it covers. A batch
// is filled from the input stream and that stream crosses from one file to the
// next, so a single failed request can be bad news about two files, and a
// report names exactly one.
func failures(covered []input.Line, err error) []Failure {
	var out []Failure
	for _, line := range covered {
		if n := len(out); n > 0 && out[n-1].File == line.File {
			out[n-1].Last = line.Num
			continue
		}
		out = append(out, Failure{File: line.File, First: line.Num, Last: line.Num, Err: err})
	}
	return out
}
