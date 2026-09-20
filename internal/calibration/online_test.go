//go:build calibration

// This file is the opt-in half of the package: it is the only code in the
// repository that talks to the live API from a test, and the "calibration"
// build tag above is what keeps it out of `make check`. Run it with
// `make calibrate`, which is also where the cost of a run is written down.
//
// Everything here goes straight to *jev.Client, so no cache is involved at
// all -- a stronger guarantee than --no-cache, and the reason a re-run after a
// model update measures the model and not last month's answers.

package calibration

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sijiaoh/jevgrep/internal/apikey"
	"github.com/sijiaoh/jevgrep/internal/cli"
	"github.com/sijiaoh/jevgrep/internal/jev"
)

// meter accumulates what a test run has cost, so the last line of its log can
// say it out loud. Every number here is the API's own count, not an estimate.
type meter struct {
	requests int
	tokens   int
	reported int
}

func (m *meter) observe(a jev.Attempt) {
	m.requests++
	if a.Reported {
		m.reported++
		m.tokens += a.InputTokens
	}
}

func (m *meter) report(t *testing.T) {
	t.Helper()
	t.Logf("billed: %d requests, %d input tokens (%d of them reported), $%.5f",
		m.requests, m.tokens, m.reported, jev.CostUSD(m.tokens))
}

// client is the scorer these tests use. A missing key fails rather than skips:
// a calibration run that quietly passes without talking to the model is worse
// than no calibration run, because its silence reads like a green result.
func client(t *testing.T, m *meter) *jev.Client {
	t.Helper()
	key, err := apikey.Load()
	if err != nil {
		t.Fatalf("this test needs a real API key (%s or --login): %v", apikey.EnvVar, err)
	}
	c, err := jev.New(jev.Config{APIKey: key, OnAttempt: m.observe})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return c
}

// shippedThreshold is the default -t the binary ships with, read from the one
// place it is written down. Hard-coding 0.5 here would let this test go on
// blessing a default the binary no longer has.
func shippedThreshold(t *testing.T) float64 {
	t.Helper()
	for _, o := range cli.Options {
		if o.Long != "threshold" {
			continue
		}
		v, err := strconv.ParseFloat(o.Default, 64)
		if err != nil {
			t.Fatalf("the -t default is not a number: %q", o.Default)
		}
		return v
	}
	t.Fatal("cli.Options has no --threshold")
	return 0
}

// shippedChunkLines is the batch size jev.Split ships with, derived by asking
// Split rather than by repeating the constant, which is unexported precisely
// because nothing outside that package gets to pick it.
func shippedChunkLines(t *testing.T) int {
	t.Helper()
	chunks := jev.Split("a meaning", make([]string, 200))
	if len(chunks) < 2 {
		t.Fatalf("Split did not split 200 lines: %d chunk(s)", len(chunks))
	}
	return len(chunks[0].Lines)
}

func score(t *testing.T, c *jev.Client, meaning string, lines []string) []float64 {
	t.Helper()
	scores, err := c.Score(context.Background(), meaning, lines)
	if err != nil {
		t.Fatalf("score %d line(s) against %q: %v", len(lines), meaning, err)
	}
	return scores
}

// --- the corpus run ------------------------------------------------------

// TestTheCorpusPicksTheDefaultThreshold is the calibration itself: it scores
// every case against the live model and prints the sweep the default -t is
// argued from: over the whole corpus, and then over each subset that could
// have argued for a different number.
func TestTheCorpusPicksTheDefaultThreshold(t *testing.T) {
	m := &meter{}
	defer m.report(t)
	c := client(t, m)

	scored := scoreCorpus(t, c)
	shipped := shippedThreshold(t)

	for _, sub := range []struct {
		title string
		keep  func(Case) bool
	}{
		{"whole corpus", func(Case) bool { return true }},
		{"cross-language only", Case.Cross},
		{"boundary cases only", func(c Case) bool { return c.Boundary }},
		{"obvious cases only", func(c Case) bool { return !c.Boundary }},
		{"English lines", func(c Case) bool { return c.Lang == "en" }},
		{"Japanese lines", func(c Case) bool { return c.Lang == "ja" }},
		{"Chinese lines", func(c Case) bool { return c.Lang == "zh" }},
	} {
		set := Only(scored, sub.keep)
		rows := Sweep(set, Thresholds())
		// Swept on its own rather than looked up in the table above: a
		// default that is not a multiple of 0.05 still has to be reported
		// against, and looking it up would report a row that is not there.
		here := Sweep(set, []float64{shipped})[0]
		low, high := Separation(set)
		t.Log("\n" + Report(fmt.Sprintf("%s (%d cases)", sub.title, len(set)), rows))
		t.Logf("%s: best F1 at -t %.2f (%.3f); at the shipped -t %.2f, F1 %.3f, precision %.3f, recall %.3f",
			sub.title, Best(rows).Threshold, Best(rows).F1(),
			shipped, here.F1(), here.Precision(), here.Recall())
		t.Logf("%s: lowest match %.3f, highest non-match %.3f (separated by %.3f)",
			sub.title, low, high, low-high)
	}

	whole := Sweep(scored, Thresholds())
	for _, s := range Mistakes(scored, shipped) {
		t.Logf("wrong at -t %.2f: scored %.3f, want %s: %q [%s] %s",
			shipped, s.Score, class(s.Match), s.Text, s.Lang, s.Note)
	}

	// The assertions are deliberately loose, in two different ways.
	//
	// Loose about the number, because a live model moves and a test that
	// pinned one would fail on every update with nothing to say. What this
	// catches is the shipped default no longer being a defensible choice.
	//
	// And loose about being the best, on purpose: this corpus has about as
	// many matches as non-matches, while a real search is almost all
	// non-matches, so its F1 systematically flatters low thresholds --
	// TestARepositoryScaleSearchIsPriced is the counterweight that keeps a
	// default from being chosen on this table alone. The demand here is that
	// the shipped threshold be close to the best, not equal to it.
	got := Sweep(scored, []float64{shipped})[0]
	best := Best(whole)
	if got.F1() < 0.80 {
		t.Errorf("the shipped -t %.2f scores F1 %.3f on the corpus; re-pick it (best here is -t %.2f at %.3f)",
			shipped, got.F1(), best.Threshold, best.F1())
	}
	if got.F1() < 0.9*best.F1() {
		t.Errorf("the shipped -t %.2f scores F1 %.3f where -t %.2f scores %.3f; that is no longer close",
			shipped, got.F1(), best.Threshold, best.F1())
	}
}

func class(match bool) string {
	if match {
		return "match"
	}
	return "non-match"
}

// scoreCorpus asks each meaning of its own lines, one request per meaning,
// which is what a jevgrep run over a file of those lines would do.
func scoreCorpus(t *testing.T, c *jev.Client) []Scored {
	t.Helper()

	byMeaning := map[string][]Case{}
	var order []string
	for _, k := range Cases() {
		if _, ok := byMeaning[k.Meaning]; !ok {
			order = append(order, k.Meaning)
		}
		byMeaning[k.Meaning] = append(byMeaning[k.Meaning], k)
	}

	var out []Scored
	for _, meaning := range order {
		group := byMeaning[meaning]
		lines := make([]string, len(group))
		for i, k := range group {
			lines[i] = k.Text
		}
		for _, chunk := range jev.Split(meaning, lines) {
			scores := score(t, c, meaning, chunk.Lines)
			for i, p := range scores {
				out = append(out, Scored{Case: group[chunk.Offset+i], Score: p})
			}
		}
	}
	return out
}

// --- measurement 1: is the fixed per-request overhead really fixed? ------

// TestTheFixedRequestOverheadIsConstant re-derives jev's
// perRequestOverheadTokens from the API's own billing. The constant was fitted
// from two measurements of one short meaning; what --dry-run's price depends on
// is whether it holds for a long meaning and a full batch too.
//
// It also times each request, which is the only evidence behind the shipped
// batch size: the size is a latency choice, not a token one.
func TestTheFixedRequestOverheadIsConstant(t *testing.T) {
	m := &meter{}
	defer m.report(t)
	c := client(t, m)

	meanings := []struct{ name, text string }{
		{"short", "a disk error"},
		{"medium", "a line reporting that a write to persistent storage did not succeed"},
		{"long", strings.Repeat("a line that reports a failure of some kind in a long-winded way, ", 8) + "roughly"},
	}
	sizes := []int{1, 5, 15, 30, 60}
	filler := "ERROR write failed" // 18 bytes, the same in every request

	t.Logf("the shipped batch size is %d lines; the rows below run up to it", shippedChunkLines(t))
	t.Logf("%-8s %9s %6s %11s %9s %9s", "meaning", "meaning-B", "lines", "billed-tok", "per-line", "elapsed")
	for _, mn := range meanings {
		billed := map[int]int{}
		for _, n := range sizes {
			lines := make([]string, n)
			for i := range lines {
				lines[i] = filler
			}
			before := m.tokens
			start := time.Now()
			score(t, c, mn.text, lines)
			elapsed := time.Since(start)
			got := m.tokens - before
			billed[n] = got
			t.Logf("%-8s %9d %6d %11d %9.1f %9s", mn.name, len(mn.text), n, got,
				float64(got)/float64(n), elapsed.Round(10*time.Millisecond))
		}

		// Two points, far apart, are enough to separate the fixed part from
		// the per-line part of a line that is the same in every request.
		lo, hi := sizes[0], sizes[len(sizes)-1]
		perLine := float64(billed[hi]-billed[lo]) / float64(hi-lo)
		fixed := float64(billed[lo]) - perLine
		estFixed, estPerLine := estimatedParts(mn.text, filler)
		t.Logf("%-8s meaning=%dB: measured fixed %.0f + %.1f per line; jev estimates fixed %d + %d per line",
			mn.name, len(mn.text), fixed, perLine, estFixed, estPerLine)

		// The claim under test: the fixed part does not follow the meaning. If
		// it does, --dry-run under-prices long meanings and the constant has
		// to become a function of the meaning instead.
		if fixed < 150 || fixed > 400 {
			t.Errorf("%s meaning: fixed overhead measured at %.0f tokens, far from the 258 jev assumes", mn.name, fixed)
		}
	}
}

// --- measurement 2: what a repository-scale first search costs -----------

// TestARepositoryScaleSearchIsPriced measures the bill on real source lines
// from this repository and scales it to the ten thousand lines §10 talks
// about. It measures a sample rather than sending ten thousand lines, because
// the per-line price is what is being measured and sending the rest of them
// would buy nothing but a bigger bill.
func TestARepositoryScaleSearchIsPriced(t *testing.T) {
	m := &meter{}
	defer m.report(t)
	c := client(t, m)

	const sample = 300
	lines := repositoryLines(t, sample)
	meaning := "a line that reports an error to the user"

	var quoted int
	var scores []float64
	for _, chunk := range jev.Split(meaning, lines) {
		quoted += jev.EstimateTokens(meaning, chunk.Lines)
		scores = append(scores, score(t, c, meaning, chunk.Lines)...)
	}

	perLine := float64(m.tokens) / float64(len(lines))
	t.Logf("%d real lines in %d requests: %d billed tokens (%.1f per line), $%.5f",
		len(lines), m.requests, m.tokens, perLine, jev.CostUSD(m.tokens))
	t.Logf("--dry-run would have quoted %d tokens ($%.5f), %+.1f%% off",
		quoted, jev.CostUSD(quoted), 100*(float64(quoted)/float64(m.tokens)-1))

	// The corpus is built out of near misses, so it cannot say what lowering
	// the default threshold does to ordinary input, where almost every line
	// is nowhere near the meaning. These lines can: they are what a `-r` run
	// over this repository would actually be asked about.
	bands := map[string]int{}
	for _, p := range scores {
		switch {
		case p >= 0.75:
			bands["0.75+"]++
		case p >= 0.50:
			bands["0.50-0.75"]++
		case p >= 0.25:
			bands["0.25-0.50"]++
		case p >= 0.10:
			bands["0.10-0.25"]++
		default:
			bands["under 0.10"]++
		}
	}
	t.Logf("scores over %d real lines: %v", len(scores), bands)
	t.Logf("a search would print %d of them at -t 0.25 and %d at -t 0.50",
		bands["0.25-0.50"]+bands["0.50-0.75"]+bands["0.75+"], bands["0.50-0.75"]+bands["0.75+"])

	// The base rate the corpus cannot show. These lines are not labelled, so
	// this cannot say how many of the selected ones are right -- but a grep
	// that answers a single meaning with a sixth of the file is not usable
	// whatever the labels would say, and a default threshold that did that
	// would have been chosen on the balanced corpus alone.
	shipped := shippedThreshold(t)
	var selected int
	for _, p := range scores {
		if p >= shipped {
			selected++
		}
	}
	t.Logf("at the shipped -t %.2f, %d of %d ordinary lines (%.0f%%) would be printed",
		shipped, selected, len(scores), 100*float64(selected)/float64(len(scores)))
	if float64(selected) > 0.15*float64(len(scores)) {
		t.Errorf("the shipped -t %.2f selects %d of %d lines of ordinary source; that is a flood, not a grep",
			shipped, selected, len(scores))
	}

	for _, scale := range []int{10_000, 100_000} {
		tokens := int(perLine * float64(scale))
		t.Logf("extrapolated to %d lines, one meaning, no cache: %d tokens, $%.4f", scale, tokens, jev.CostUSD(tokens))
	}

	// --dry-run quoting less than the bill is the one direction a price must
	// not be wrong in (§11), so the estimate is held to a floor, not to a band.
	if float64(quoted) < 0.9*float64(m.tokens) {
		t.Errorf("--dry-run would quote %d tokens for a bill of %d: it under-prices real input", quoted, m.tokens)
	}
}

// repositoryLines reads up to n non-blank lines of this repository's own Go
// source: the input jevgrep is actually pointed at, not prose invented for a
// benchmark.
func repositoryLines(t *testing.T, n int) []string {
	t.Helper()

	var out []string
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case len(out) >= n:
			return filepath.SkipAll
		// This package's own files are skipped for two reasons: the corpus
		// holds invented credential-shaped lines, which jevgrep itself would
		// never send (§6), and a "line from somewhere else" that turned out
		// to be a corpus line would make the padding in the jitter run stop
		// being unrelated.
		case d.IsDir() && (d.Name() == ".git" || d.Name() == "dist" || d.Name() == "testdata" || d.Name() == "calibration"):
			return filepath.SkipDir
		case d.IsDir() || !strings.HasSuffix(path, ".go"):
			return nil
		}

		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == "" || len(out) >= n {
				continue
			}
			out = append(out, line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read this repository: %v", err)
	}
	if len(out) < n {
		t.Fatalf("only found %d lines to score, wanted %d", len(out), n)
	}
	return out
}

// --- measurement 3: does a score depend on the batch it travelled in? ----

// TestAScoreDoesNotDependOnItsBatch is the one internal/cache leans on: the
// cache treats a probability as a pure function of (line, meaning), while a
// request puts the whole batch into the model's state. This scores the same
// lines six different ways -- alone, twice over, padded, reversed, one per
// request, and in a batch filled to the shipped size with real source -- and
// reports the spread.
//
// The failure that matters is not a spread but a crossing: a line whose score
// moves across the default threshold depending on what it was sent with is a
// line the cache can answer differently from a fresh run.
func TestAScoreDoesNotDependOnItsBatch(t *testing.T) {
	m := &meter{}
	defer m.report(t)
	c := client(t, m)

	meaning := meaningDiskWrite
	var probes []Case
	for _, k := range Cases() {
		if k.Meaning == meaning {
			probes = append(probes, k)
		}
	}
	probeText := make([]string, len(probes))
	for i, p := range probes {
		probeText[i] = p.Text
	}

	// Company for the probes: lines about something else entirely, which is
	// the state a real run would have put around them.
	var padding []string
	for _, k := range Cases() {
		if k.Meaning == meaningShutdown || k.Meaning == meaningRetry {
			padding = append(padding, k.Text)
		}
	}
	// And a batch filled to the size jevgrep actually ships, with real source
	// lines: that is the state every line in a `-r` run travels in, so a
	// batch size is only defensible if the answers survive it.
	full := append(append([]string{}, probeText...), padding...)
	full = append(full, repositoryLines(t, shippedChunkLines(t))...)
	full = full[:shippedChunkLines(t)]

	runs := map[string][]float64{}
	runs["alone in a batch of their own"] = score(t, c, meaning, probeText)
	runs["the same batch again"] = score(t, c, meaning, probeText)
	runs["padded with unrelated lines"] = score(t, c, meaning, append(append([]string{}, probeText...), padding...))[:len(probes)]
	runs["in reverse order"] = reverse(score(t, c, meaning, reverse(probeText)))
	runs["in a full batch of real lines"] = score(t, c, meaning, full)[:len(probes)]

	one := make([]float64, len(probes))
	for i, text := range probeText {
		one[i] = score(t, c, meaning, []string{text})[0]
	}
	runs["one line per request"] = one

	names := make([]string, 0, len(runs))
	for name := range runs {
		names = append(names, name)
	}
	slices.Sort(names)

	shipped := shippedThreshold(t)
	var worst float64
	for i, p := range probes {
		lo, hi := math.Inf(1), math.Inf(-1)
		var parts []string
		for _, name := range names {
			v := runs[name][i]
			lo, hi = math.Min(lo, v), math.Max(hi, v)
			parts = append(parts, fmt.Sprintf("%s %.3f", name, v))
		}
		worst = math.Max(worst, hi-lo)
		t.Logf("spread %.3f  %s  (%q)", hi-lo, strings.Join(parts, ", "), p.Text)

		if (lo >= shipped) != (hi >= shipped) {
			t.Errorf("%q crosses the default -t %.2f depending on its batch (%.3f..%.3f); "+
				"internal/cache treats a score as a pure function of (line, meaning)", p.Text, shipped, lo, hi)
		}
	}
	t.Logf("widest spread across batch compositions: %.3f", worst)
}

// reverse copies in back to front. It is used on the way out and on the way
// back, so that a run in reverse order can be compared line by line with the
// others.
func reverse[T any](in []T) []T {
	out := make([]T, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

// estimated splits jev's own estimate into the same two parts the measurement
// produces, so the log puts them side by side rather than leaving the reader to
// subtract.
func estimatedParts(meaning, line string) (fixed, perLine int) {
	one := jev.EstimateTokens(meaning, []string{line})
	two := jev.EstimateTokens(meaning, []string{line, line})
	perLine = two - one
	return one - perLine, perLine
}

// --- the batch size ------------------------------------------------------

// TestTheBatchSizeTradesLatencyForPrice measures both halves of the only
// trade-off the shipped chunk size is: a bigger batch spreads the fixed
// per-request overhead over more lines, and makes every line in it wait for
// the whole batch. It is the evidence behind jev's maxLinesPerChunk, which is
// a latency choice and cannot be argued from tokens alone.
func TestTheBatchSizeTradesLatencyForPrice(t *testing.T) {
	m := &meter{}
	defer m.report(t)
	c := client(t, m)

	// Real source lines and a meaning of realistic length: the cost per line
	// depends on both, and a batch of identical 18-byte lines would flatter
	// the big sizes.
	pool := repositoryLines(t, 700)
	meaning := "a line that reports an error to the user"

	// Five samples because the network is the noise floor here: single
	// requests to the same endpoint have been seen to vary by 3x, and the
	// difference being measured between neighbouring sizes is smaller than
	// that.
	const repeats = 5
	t.Logf("%6s %9s %11s %9s %14s", "lines", "requests", "billed-tok", "per-line", "median-elapsed")
	for _, n := range []int{10, 30, 60, 120} {
		var elapsed []time.Duration
		before := m.tokens
		for r := range repeats {
			// A different slice of the pool each repeat, so a repeat measures
			// the API again rather than whatever it may cache about a payload.
			start := (r * n) % (len(pool) - n)
			lines := pool[start : start+n]
			began := time.Now()
			score(t, c, meaning, lines)
			elapsed = append(elapsed, time.Since(began))
		}
		slices.Sort(elapsed)
		tokens := m.tokens - before
		perLine := float64(tokens) / float64(repeats*n)
		t.Logf("%6d %9d %11d %9.1f %14s", n, repeats, tokens, perLine,
			elapsed[len(elapsed)/2].Round(10*time.Millisecond))
		t.Logf("       -> a 10,000 line search at this size: $%.4f", jev.CostUSD(int(perLine*10_000)))
	}
}
