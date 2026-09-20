package cli

import (
	"context"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sijiaoh/jevgrep/internal/input"
	"github.com/sijiaoh/jevgrep/internal/jev"
	"github.com/sijiaoh/jevgrep/internal/search"
)

// The one layout --dry-run and --stats both print: two spaces of indent, the
// label in a column of its own, the number right-aligned under the one above
// it, and whatever explains it two spaces further along. One renderer for both
// because they are the same reckoning, one before the money is spent and one
// after, and two layouts would invite two answers to the same question.
const (
	reportIndent = "  "
	labelWidth   = 13
	valueWidth   = 9
)

// row is one line of a report.
type row struct {
	label string
	value string
	note  string
	// wide says the value is not a figure. The cache directory is the only
	// one, and it must not drag the column of numbers out to the width of a
	// path.
	wide bool
}

func renderRows(rows []row) string {
	// The column is as wide as the widest figure in it and never narrower
	// than valueWidth. A run cheap enough to be priced in millionths, or big
	// enough to be counted in millions, still has to read as one column: a
	// number that pushes past the column and shifts left is the one thing
	// this layout exists to prevent.
	width := valueWidth
	for _, r := range rows {
		if !r.wide {
			width = max(width, len(r.value))
		}
	}

	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s%-*s%*s", reportIndent, labelWidth, r.label, width, r.value)
		if r.note != "" {
			b.WriteString("  " + r.note)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// thousands groups a count the way the reports print it. A bill is read at a
// glance and a seven-digit number without separators is read wrong.
func thousands(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// money is a dollar amount as the reports print it. Four decimals is the
// fraction of a cent a user is deciding on; below that the number would read
// as free, so a cheap run gets the digits it actually costs instead of a
// rounded $0.0000 -- the one direction in which a price must not be wrong.
func money(v float64) string {
	switch {
	case v >= 0.0001:
		return fmt.Sprintf("$%.4f", v)
	case v >= 0.000001:
		return fmt.Sprintf("$%.6f", v)
	case v > 0:
		return "<$0.000001"
	}
	return "$0"
}

// approx marks a number jevgrep worked out itself. The tilde is the whole
// difference between a count and an estimate, and every number in both reports
// is one or the other.
func approx(exact bool, s string) string {
	if exact {
		return s
	}
	return "~" + s
}

// priceNote renders the price behind every cost line, so that the number and
// the rate it came from can never disagree.
func priceNote() string {
	return fmt.Sprintf("at $%g per 1M input tokens", jev.PriceUSDPerMTokInput)
}

// meaningsNote says how many questions each line costs, which is the one thing
// that turns a file of n lines into 2n or 3n paid questions.
func meaningsNote(n int) string {
	if n == 1 {
		return "1 meaning per line"
	}
	return fmt.Sprintf("%d meanings per line", n)
}

// meter counts what a run actually did, for --stats.
//
// Every field is a number of things and never a thing: no line, no file name
// and no key can reach a report that §11 asks people to paste into an issue.
// It is written from the reader's goroutine, from the workers and from Run's
// own loop, so it is guarded.
type meter struct {
	mu sync.Mutex

	lines, blank      int
	questions, cached int
	batches, failed   int
	requests          int
	// estimated is what jevgrep worked out the batches would cost, reported is
	// what the API said it charged, and reports counts the requests that said
	// anything at all. The report prefers the API's number and falls back to
	// ours, which is why both are kept: mixing them would quote a price that
	// is neither.
	estimated, reported, reports int
}

func (m *meter) line(scored bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lines++
	if !scored {
		m.blank++
	}
}

func (m *meter) question(cached bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questions++
	if cached {
		m.cached++
	}
}

func (m *meter) batch(tokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batches++
	m.estimated += tokens
}

func (m *meter) batchFailed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed++
}

func (m *meter) attempt(a jev.Attempt) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests++
	if a.Reported {
		m.reports++
		m.reported += a.InputTokens
	}
}

// watch counts the lines on their way to the searcher. Query is asked a second
// time here rather than remembered: it costs a blank-line test on a run that
// asked for --stats, and carrying the answer through input.Line would put a
// number nobody else wants in everybody's way.
func (m *meter) watch(lines iter.Seq[input.Line]) iter.Seq[input.Line] {
	return func(yield func(input.Line) bool) {
		for line := range lines {
			_, scored := line.Query()
			m.line(scored)
			if !yield(line) {
				return
			}
		}
	}
}

// recall counts the questions as they are asked of the cache. It stands in for
// the cache even when there is none, because a question nobody could answer is
// still a question that was paid for.
func (m *meter) recall(next func(meaning, query string) (float64, bool)) func(string, string) (float64, bool) {
	return func(meaning, query string) (float64, bool) {
		if next == nil {
			m.question(false)
			return 0, false
		}
		score, ok := next(meaning, query)
		m.question(ok)
		return score, ok
	}
}

// meteringScorer counts the batches on their way out and what they cost.
type meteringScorer struct {
	next  search.Scorer
	meter *meter
}

func (s meteringScorer) Score(ctx context.Context, meaning string, lines []string) ([]float64, error) {
	s.meter.batch(jev.EstimateTokens(meaning, lines))
	scores, err := s.next.Score(ctx, meaning, lines)
	// A run the user stopped has no failed batches, only unfinished ones.
	if err != nil && ctx.Err() == nil {
		s.meter.batchFailed()
	}
	return scores, err
}

// tokens is the input tokens this run cost, and whether that is the API's own
// count rather than ours. The caller holds m.mu.
//
// The API's is preferred whole or not at all: a total that adds a reported
// number to an estimated one is a total that is neither, and nothing would say
// which requests were which.
func (m *meter) tokens() (int, bool) {
	// Every request said what it charged, so the total is the bill itself. A
	// run that sent nothing at all lands here too, and rightly: nothing was
	// spent, and that is a count rather than a guess.
	if m.reports == m.requests {
		return m.reported, true
	}
	return m.estimated, false
}

// cacheState is what the score cache turned out to be for this run, which
// only --stats reports: a cache that could not be opened changes nothing about
// the search itself and is never worth a warning of its own.
type cacheState struct {
	// dir is the absolute path, empty when even that could not be worked out.
	dir string
	// on says the run is reading and writing it.
	on bool
	// off says --no-cache was given, as opposed to a cache that failed.
	off bool
}

// report is the --stats table, without its header. files and elapsed come from
// the caller: only it knows how many inputs were opened and how long the whole
// run took.
func (m *meter) report(files int, elapsed time.Duration, c cacheState) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	tokens, exact := m.tokens()
	rows := []row{
		{label: "files", value: thousands(files)},
		{label: "lines read", value: thousands(m.lines)},
		{label: "blank", value: thousands(m.blank), note: "never sent"},
		{label: "questions", value: thousands(m.questions)},
		{label: "cached", value: thousands(m.cached), note: cachedNote(m.cached, m.questions, c)},
		{label: "sent", value: thousands(m.questions - m.cached)},
		{label: "requests", value: thousands(m.requests), note: requestsNote(m.batches, m.requests)},
		{label: "failed", value: thousands(m.failed)},
		{label: "input tokens", value: approx(exact, thousands(tokens))},
		{label: "cost", value: approx(exact, money(jev.CostUSD(tokens))), note: priceNote()},
		{label: "elapsed", value: fmt.Sprintf("%.1fs", elapsed.Seconds())},
		{label: "cache", value: cacheValue(c), note: cacheNote(c), wide: true},
	}
	return renderRows(rows)
}

// cachedNote says what the hits were worth, or why there were none.
func cachedNote(cached, questions int, c cacheState) string {
	switch {
	case c.off:
		return "--no-cache"
	case !c.on:
		return "no cache this run"
	case questions == 0:
		return ""
	}
	return fmt.Sprintf("%d%% of the questions", 100*cached/questions)
}

// requestsNote explains a request count that is larger than the number of
// batches: retries are where the time and the money of a bad afternoon go, and
// nothing else in the report would show them.
func requestsNote(batches, requests int) string {
	return fmt.Sprintf("%s, %s retried", plural(batches, "batch", "batches"), thousands(max(requests-batches, 0)))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return thousands(n) + " " + one
	}
	return thousands(n) + " " + many
}

// cacheValue and cacheNote are the one place the program itself names the
// cache directory. Someone who wants it gone has to be told where it is, and
// --stats is the command they are already typing when they want to know.
func cacheValue(c cacheState) string {
	if !c.on {
		return "off"
	}
	return c.dir
}

func cacheNote(c cacheState) string {
	switch {
	case c.on:
		return "(delete it to clear)"
	case c.off:
		return "--no-cache"
	case c.dir == "":
		return "(no cache directory)"
	}
	return fmt.Sprintf("(could not use %s)", c.dir)
}
