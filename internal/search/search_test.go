package search

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sijiaoh/jevgrep/internal/input"
	"github.com/sijiaoh/jevgrep/internal/jev"
)

// request is one call to the fake scorer.
type request struct {
	meaning string
	lines   []string
}

// fakeScorer answers without a server. respond runs concurrently, once per
// request, which is how a test makes batches finish out of order.
type fakeScorer struct {
	respond func(request) ([]float64, error)

	mu       sync.Mutex
	requests []request
	inFlight int
	peak     int
}

func (f *fakeScorer) Score(_ context.Context, meaning string, lines []string) ([]float64, error) {
	req := request{meaning: meaning, lines: slices.Clone(lines)}

	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.inFlight++
	f.peak = max(f.peak, f.inFlight)
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	if f.respond != nil {
		return f.respond(req)
	}
	return contains(req)
}

// contains scores 1 for a line that has the meaning as a substring, so a test
// can say which lines are supposed to match by writing them that way.
func contains(req request) ([]float64, error) {
	scores := make([]float64, len(req.lines))
	for i, line := range req.lines {
		if strings.Contains(line, req.meaning) {
			scores[i] = 1
		}
	}
	return scores, nil
}

func (f *fakeScorer) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var lines []string
	for _, req := range f.requests {
		lines = append(lines, req.lines...)
	}
	return lines
}

// lines turns text into the stream Run reads, as one file's worth of lines.
func lines(file string, texts ...string) iter.Seq[input.Line] {
	return func(yield func(input.Line) bool) {
		for i, text := range texts {
			if !yield(input.Line{File: file, Num: i + 1, Text: text}) {
				return
			}
		}
	}
}

func numbered(prefix string, n int) []string {
	texts := make([]string, n)
	for i := range texts {
		texts[i] = fmt.Sprintf("%s %d", prefix, i+1)
	}
	return texts
}

// collector gathers what a run emitted and failed on, in the order it happened.
type collector struct {
	matched  []string
	failures []Failure
}

func (c *collector) options(concurrency int) Options {
	return Options{
		Concurrency: concurrency,
		Emit:        func(l input.Line) { c.matched = append(c.matched, l.Text) },
		Fail:        func(f Failure) { c.failures = append(c.failures, f) },
	}
}

func TestRunEmitsMatchesInInputOrderWhateverOrderTheyAreScoredIn(t *testing.T) {
	texts := numbered("line", 95)
	// Every batch but the first waits for the one after it, so the requests
	// finish back to front and only the ordering in Run can save the output.
	var mu sync.Mutex
	done := map[string]chan struct{}{}
	gate := func(req request) chan struct{} {
		mu.Lock()
		defer mu.Unlock()
		if done[req.lines[0]] == nil {
			done[req.lines[0]] = make(chan struct{})
		}
		return done[req.lines[0]]
	}
	scorer := &fakeScorer{}
	scorer.respond = func(req request) ([]float64, error) {
		if next := req.lines[len(req.lines)-1]; next != texts[len(texts)-1] {
			<-gate(request{lines: []string{nextOf(texts, next)}})
		}
		scores, err := contains(req)
		close(gate(req))
		return scores, err
	}

	var got collector
	s := New(scorer, mustCompile(t, []Term{{Meaning: "line"}}, 0.5, false), got.options(len(texts)))

	if err := s.Run(t.Context(), lines("a.txt", texts...)); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !slices.Equal(got.matched, texts) {
		t.Errorf("emitted %d lines out of order: %q", len(got.matched), got.matched)
	}
}

func nextOf(texts []string, text string) string {
	return texts[slices.Index(texts, text)+1]
}

func TestRunEmitsTheFirstMatchesBeforeTheRestAreScored(t *testing.T) {
	// §10 asks for first results in seconds on a whole repository, which is
	// only possible if a match is printed without waiting for the run to end.
	texts := numbered("line", 60)
	release := make(chan struct{})
	scorer := &fakeScorer{respond: func(req request) ([]float64, error) {
		if req.lines[0] != texts[0] {
			<-release
		}
		return contains(req)
	}}

	first := make(chan string)
	opts := Options{
		Concurrency: 4,
		Emit:        func(l input.Line) { first <- l.Text },
	}
	s := New(scorer, mustCompile(t, []Term{{Meaning: "line"}}, 0.5, false), opts)

	go func() {
		defer close(first)
		if err := s.Run(t.Context(), lines("a.txt", texts...)); err != nil {
			t.Errorf("Run() = %v", err)
		}
	}()

	if got := <-first; got != texts[0] {
		t.Fatalf("first emitted line = %q, want %q", got, texts[0])
	}
	close(release)
	// Let the rest of the run finish rather than leaving Run blocked on Emit.
	for range first {
	}
}

func TestLinesWithNothingToAskAboutAreNeverSent(t *testing.T) {
	tests := []struct {
		name   string
		invert bool
		want   []string
	}{
		{"they never match", false, []string{"hit"}},
		{"and -v therefore always selects them", true, []string{"", "   ", "\t"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scorer := &fakeScorer{}
			var got collector
			s := New(scorer, mustCompile(t, []Term{{Meaning: "hit"}}, 0.5, tt.invert), got.options(0))

			if err := s.Run(t.Context(), lines("a.txt", "", "hit", "   ", "\t")); err != nil {
				t.Fatalf("Run() = %v", err)
			}
			if want := []string{"hit"}; !slices.Equal(scorer.sent(), want) {
				t.Errorf("sent %q, want %q", scorer.sent(), want)
			}
			if !slices.Equal(got.matched, tt.want) {
				t.Errorf("emitted %q, want %q", got.matched, tt.want)
			}
		})
	}
}

func TestEveryMeaningIsScoredForEveryLine(t *testing.T) {
	// The batches of one meaning are cut in their own places, so a line can sit
	// in the middle of one meaning's batch and at the edge of another's.
	texts := numbered("line", 70)
	scorer := &fakeScorer{}
	expr := mustCompile(t, []Term{
		{Meaning: "line", And: []string{"a much longer meaning that changes where the batches end"}},
	}, 0.5, false)

	var got collector
	if err := New(scorer, expr, got.options(0)).Run(t.Context(), lines("a.txt", texts...)); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	for _, meaning := range expr.Meanings() {
		var batches [][]string
		for _, req := range scorer.requests {
			if req.meaning == meaning {
				batches = append(batches, req.lines)
			}
		}
		// The batches of one meaning are contiguous and cover the input; they
		// are recorded as they are sent, which is not the order they finish in.
		slices.SortFunc(batches, func(a, b []string) int {
			return slices.Index(texts, a[0]) - slices.Index(texts, b[0])
		})
		scored := slices.Concat(batches...)
		if !slices.Equal(scored, texts) {
			t.Errorf("meaning %q scored %d lines, want all %d in order", meaning, len(scored), len(texts))
		}
	}
	// "line" matches every line, the long meaning matches none, and --and
	// wants both.
	if len(got.matched) != 0 {
		t.Errorf("emitted %q, want nothing", got.matched)
	}
}

func TestABatchThatCannotBeScoredIsReportedAndTheRestOfTheRunGoesOn(t *testing.T) {
	texts := numbered("line", 40)
	boom := errors.New("HTTP 503 after 3 attempts")
	scorer := &fakeScorer{respond: func(req request) ([]float64, error) {
		if req.lines[0] == texts[0] {
			return nil, boom
		}
		return contains(req)
	}}

	var got collector
	s := New(scorer, mustCompile(t, []Term{{Meaning: "line"}}, 0.5, false), got.options(0))

	if err := s.Run(t.Context(), lines("a.txt", texts...)); err != nil {
		t.Fatalf("Run() = %v, want the run to carry on", err)
	}

	want := []Failure{{File: "a.txt", First: 1, Last: 30, Err: boom}}
	if !slices.Equal(got.failures, want) {
		t.Errorf("failures = %+v, want %+v", got.failures, want)
	}
	// The skipped lines are neither printed nor counted, and the rest are.
	if !slices.Equal(got.matched, texts[30:]) {
		t.Errorf("emitted %q, want %q", got.matched, texts[30:])
	}
}

func TestAFailedBatchIsReportedOncePerFileItCovers(t *testing.T) {
	boom := errors.New("HTTP 503 after 3 attempts")
	scorer := &fakeScorer{respond: func(request) ([]float64, error) { return nil, boom }}

	var got collector
	s := New(scorer, mustCompile(t, []Term{{Meaning: "line"}}, 0.5, false), got.options(0))

	two := func(yield func(input.Line) bool) {
		for _, file := range []string{"a.txt", "b.txt"} {
			for i, text := range numbered("line", 5) {
				if !yield(input.Line{File: file, Num: i + 1, Text: text}) {
					return
				}
			}
		}
	}
	if err := s.Run(t.Context(), two); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	want := []Failure{
		{File: "a.txt", First: 1, Last: 5, Err: boom},
		{File: "b.txt", First: 1, Last: 5, Err: boom},
	}
	if !slices.Equal(got.failures, want) {
		t.Errorf("failures = %+v, want %+v", got.failures, want)
	}
}

func TestARejectedKeyEndsTheWholeRun(t *testing.T) {
	texts := numbered("line", 95)
	authErr := &jev.AuthError{StatusCode: 401}
	scorer := &fakeScorer{respond: func(req request) ([]float64, error) {
		if req.lines[0] == texts[0] {
			return contains(req)
		}
		return nil, authErr
	}}

	var got collector
	s := New(scorer, mustCompile(t, []Term{{Meaning: "line"}}, 0.5, false), got.options(0))

	err := s.Run(t.Context(), lines("a.txt", texts...))
	var gotAuth *jev.AuthError
	if !errors.As(err, &gotAuth) {
		t.Fatalf("Run() = %v, want an *jev.AuthError", err)
	}
	// Whatever was decided before the run stopped stands, in order, and no key
	// failure is reported as a skipped batch.
	if !slices.Equal(got.matched, texts[:len(got.matched)]) {
		t.Errorf("emitted %q, want a prefix of the input", got.matched)
	}
	if len(got.failures) != 0 {
		t.Errorf("failures = %+v, want none", got.failures)
	}
}

func TestCancellingKeepsWhatWasEmittedAndReportsNoBatchFailures(t *testing.T) {
	texts := numbered("line", 95)
	ctx, cancel := context.WithCancel(t.Context())
	scorer := &fakeScorer{respond: func(req request) ([]float64, error) {
		if req.lines[0] != texts[0] {
			// Stand in for a request in flight when the user hits Ctrl-C.
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return contains(req)
	}}

	var got collector
	opts := got.options(4)
	emit := opts.Emit
	opts.Emit = func(l input.Line) {
		emit(l)
		if l.Text == texts[0] {
			cancel()
			// Let the requests that were in flight come back cancelled while
			// the run is still busy printing, the way a slow terminal would.
			// What the run then has in hand is their failures, and the test is
			// about what it does with those rather than about timing.
			time.Sleep(10 * time.Millisecond)
		}
	}
	s := New(scorer, mustCompile(t, []Term{{Meaning: "line"}}, 0.5, false), opts)

	if err := s.Run(ctx, lines("a.txt", texts...)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v, want context.Canceled", err)
	}
	if !slices.Equal(got.matched, texts[:30]) {
		t.Errorf("emitted %q, want the first batch", got.matched)
	}
	// A cancelled request is the user stopping, not a batch worth a warning.
	if len(got.failures) != 0 {
		t.Errorf("failures = %+v, want none", got.failures)
	}
}

func TestRunKeepsAtMostConcurrencyRequestsInFlight(t *testing.T) {
	const concurrency = 3
	texts := numbered("line", 300)

	// Every request is held until as many as the cap are waiting together, so
	// the peak is what the scheduler allows rather than what it happened to
	// overlap. If it ever allowed fewer, this would not finish.
	var (
		mu      sync.Mutex
		waiting int
		once    sync.Once
		release = make(chan struct{})
	)
	scorer := &fakeScorer{}
	scorer.respond = func(req request) ([]float64, error) {
		mu.Lock()
		waiting++
		if waiting >= concurrency {
			once.Do(func() { close(release) })
		}
		mu.Unlock()

		<-release
		return contains(req)
	}

	var got collector
	s := New(scorer, mustCompile(t, []Term{{Meaning: "line"}}, 0.5, false), got.options(concurrency))
	if err := s.Run(t.Context(), lines("a.txt", texts...)); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if scorer.peak != concurrency {
		t.Errorf("peak requests in flight = %d, want exactly %d", scorer.peak, concurrency)
	}
	if !slices.Equal(got.matched, texts) {
		t.Errorf("emitted %d lines, want all %d in order", len(got.matched), len(texts))
	}
}

func TestRunOnEmptyInputDoesNothing(t *testing.T) {
	scorer := &fakeScorer{}
	var got collector
	s := New(scorer, mustCompile(t, []Term{{Meaning: "hit"}}, 0.5, false), got.options(0))

	if err := s.Run(t.Context(), lines("a.txt")); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if len(scorer.requests) != 0 || len(got.matched) != 0 {
		t.Errorf("sent %d requests and emitted %q, want neither", len(scorer.requests), got.matched)
	}
}

func TestRunWithoutCallbacksIsUsable(t *testing.T) {
	s := New(&fakeScorer{}, mustCompile(t, []Term{{Meaning: "hit"}}, 0.5, false), Options{})
	if err := s.Run(t.Context(), lines("a.txt", "hit")); err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

// The client the command line will hand to New has to fit through the seam the
// tests fake out.
var _ Scorer = (*jev.Client)(nil)

func TestOneMeaningFailingDoesNotStrandTheOthersStillBeingScored(t *testing.T) {
	// Two meanings means two batches over the same lines. The failing one must
	// not retire those lines while the other is still in flight over them.
	texts := numbered("line", 40)
	boom := errors.New("HTTP 503 after 3 attempts")
	slow := "a much longer meaning that changes where the batches end"
	release := make(chan struct{})
	scorer := &fakeScorer{respond: func(req request) ([]float64, error) {
		if req.meaning == slow {
			<-release
			return contains(req)
		}
		return nil, boom
	}}

	var got collector
	expr := mustCompile(t, []Term{{Meaning: "line"}, {Meaning: slow}}, 0.5, false)
	s := New(scorer, expr, got.options(4))

	done := make(chan error, 1)
	go func() { done <- s.Run(t.Context(), lines("a.txt", texts...)) }()
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
	// Every line was part of the failed batches, so none of them is decided,
	// whichever meaning would have matched.
	if len(got.matched) != 0 {
		t.Errorf("emitted %q, want nothing: those lines were never scored", got.matched)
	}
	if len(got.failures) == 0 {
		t.Error("failures = none, want the failed batches reported")
	}
	for _, f := range got.failures {
		if f.File != "a.txt" || f.First < 1 || f.Last > len(texts) {
			t.Errorf("failure %+v is outside the input", f)
		}
	}
}

// A line the model is never asked about takes no slot among the requests in
// flight, so the queue of undecided lines is the only thing standing between a
// long run of such lines and the whole input in memory: before maxPending
// existed, a 3 MB file of blank lines behind one line still being scored cost
// 450 MB of RSS.
func TestLinesThatAreNeverSentDoNotQueueUpWithoutLimit(t *testing.T) {
	// The one line worth scoring never comes back, so nothing can ever leave
	// the queue and the reader has only the cap to stop it.
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })
	scorer := &fakeScorer{respond: func(req request) ([]float64, error) {
		<-held
		return contains(req)
	}}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var read atomic.Int64
	var got collector
	s := New(scorer, mustCompile(t, []Term{{Meaning: "hit"}}, 0.5, false), got.options(0))

	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx, func(yield func(input.Line) bool) {
			if !yield(input.Line{File: "a.txt", Num: 1, Text: "hit"}) {
				return
			}
			for num := 2; ; num++ {
				read.Add(1)
				if !yield(input.Line{File: "a.txt", Num: num, Text: ""}) {
					return
				}
			}
		})
	}()

	// The input is endless on purpose: the only way this finishes is the
	// reader coming to a stop, so the test watches for a while and fails on a
	// reader that keeps going.
	limit := int64(2 * maxPending)
	for range 20 {
		if n := read.Load(); n > limit {
			t.Fatalf("the reader took in %d lines with the queue full, want it to stop near %d", n, maxPending)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := read.Load(); n < maxPending-1 {
		t.Errorf("the reader stopped after %d lines, want it to fill the queue of %d first", n, maxPending)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run() = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not return after the context was cancelled")
	}
}

func TestCancellingReturnsEvenWhileTheInputIsStuck(t *testing.T) {
	// Reading a pipe that has gone quiet parks the reader for as long as the
	// other end likes. Ctrl-C has to get the prompt back anyway.
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	ctx, cancel := context.WithCancel(t.Context())

	var got collector
	s := New(&fakeScorer{}, mustCompile(t, []Term{{Meaning: "hit"}}, 0.5, false), got.options(0))

	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx, func(yield func(input.Line) bool) {
			if !yield(input.Line{File: "a.txt", Num: 1, Text: "hit"}) {
				return
			}
			<-stuck
		})
	}()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run() = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not return after the context was cancelled")
	}
}
