package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/sijiaoh/jevgrep/internal/input"
	"github.com/sijiaoh/jevgrep/internal/jev"
	"github.com/sijiaoh/jevgrep/internal/search"
)

// dryRun does everything a search does except pay for it: it expands the
// walks, reads every file, decides which lines would be sent, splits them into
// the very batches the searcher would, asks the cache what it already knows,
// and prices the rest.
//
// No API key is loaded and no client is built. "How much would this cost" is
// the first question someone asks, often before they have signed up at all,
// and putting the answer behind a key would be putting the price list behind
// the till.
func dryRun(env environment, cfg config, expr *search.Expr, paths []string, stdout, stderr io.Writer) int {
	log := &errorLog{w: stderr}
	files := &opened{}
	r := &reader{
		stdin:  env.stdin,
		log:    log,
		opened: files,
		// -m 0 is the one command line that reads nothing at all, and it means
		// the same thing here: the estimate for a run that sends nothing is
		// zero, exactly.
		quotas:    newQuotas(cfg),
		recursive: cfg.recursive,
		walkOpts:  walkOptions(cfg),
	}

	cached, state := lookup(cfg)
	meanings := expr.Meanings()
	est := make([]*estimate, len(meanings))
	for i, meaning := range meanings {
		est[i] = &estimate{meaning: meaning}
	}

	var counts dryRunCounts
	for line := range r.lines(paths) {
		counts.lines++
		query, scored := line.Query()
		if !scored {
			counts.blank++
			continue
		}
		for i, meaning := range meanings {
			counts.questions++
			if cached != nil {
				if _, ok := cached(meaning, query); ok {
					counts.cached++
					continue
				}
			}
			est[i].add(query)
		}
	}
	for _, e := range est {
		e.split(true)
		counts.batches += e.batches
		counts.tokens += e.tokens
	}
	counts.files = files.count()
	counts.meanings = len(meanings)
	counts.stdin = counts.files == 1 && onlyStdin(files)
	// A run that can stop reading early sends less than everything it opened,
	// and how much less depends on where the matches turn out to be. The
	// estimate is then a ceiling rather than a figure, and says so.
	counts.upperBound = cfg.stopsEarly()

	switch {
	// -q asks for nothing on stdout, and a report is something. It is not a
	// combination worth refusing: a script may well want only the exit code.
	case cfg.quiet:
	case cfg.json:
		printDryRunJSON(stdout, cfg, counts)
	default:
		fmt.Fprint(stdout, counts.render(state))
	}

	if log.failed() {
		return ExitError
	}
	// Never ExitNoMatch: not one line was judged, so "nothing matched" is a
	// claim this run is in no position to make.
	return ExitMatch
}

// dryRunCounts is one estimate, ready to be printed either way round.
type dryRunCounts struct {
	files, meanings   int
	lines, blank      int
	questions, cached int
	batches, tokens   int
	stdin, upperBound bool
}

func (c dryRunCounts) render(state cacheState) string {
	header := "dry run: nothing was sent"
	if c.upperBound {
		header += " (upper bound)"
	}

	// A run with nothing left to send makes no requests and costs nothing, and
	// that is a certainty rather than a guess: no tilde on it.
	exact := c.questions == c.cached

	files := row{label: "files", value: thousands(c.files)}
	if c.stdin {
		files.note = input.StdinName
	}
	rows := []row{
		files,
		{label: "lines read", value: thousands(c.lines)},
		{label: "blank", value: thousands(c.blank), note: "never sent"},
		{label: "questions", value: thousands(c.questions), note: meaningsNote(c.meanings)},
		{label: "cached", value: thousands(c.cached), note: dryRunCachedNote(state)},
		{label: "to send", value: thousands(c.questions - c.cached)},
		// An estimate, not a count: the searcher sends a part-full batch
		// whenever its queue of undecided lines fills up, so the real run makes
		// at least this many requests and, on a slow enough server, more.
		{label: "batches", value: approx(exact, thousands(c.batches))},
		{label: "input tokens", value: approx(exact, thousands(c.tokens))},
		{label: "cost", value: approx(exact, money(jev.CostUSD(c.tokens))), note: priceNote()},
	}
	return header + "\n" + renderRows(rows)
}

func dryRunCachedNote(state cacheState) string {
	switch {
	case state.off:
		return "--no-cache"
	case !state.on:
		return "no cache this run"
	}
	return "already scored"
}

// printDryRunJSON writes the estimate as one object rather than the stream of
// records --json usually is: an estimate is not about lines, so there is no
// line to make a record of. price_usd_per_mtok rides along so that a script
// pricing a run of its own never has to hard-code the rate.
func printDryRunJSON(stdout io.Writer, cfg config, c dryRunCounts) {
	record := struct {
		DryRun     bool    `json:"dry_run"`
		Model      string  `json:"model"`
		Meanings   int     `json:"meanings"`
		Files      int     `json:"files"`
		Lines      int     `json:"lines"`
		Blank      int     `json:"blank"`
		Questions  int     `json:"questions"`
		Cached     int     `json:"cached"`
		Send       int     `json:"send"`
		Batches    int     `json:"batches"`
		Tokens     int     `json:"input_tokens"`
		Cost       float64 `json:"cost_usd"`
		Price      float64 `json:"price_usd_per_mtok"`
		UpperBound bool    `json:"upper_bound"`
	}{
		DryRun:     true,
		Model:      cfg.model,
		Meanings:   c.meanings,
		Files:      c.files,
		Lines:      c.lines,
		Blank:      c.blank,
		Questions:  c.questions,
		Cached:     c.cached,
		Send:       c.questions - c.cached,
		Batches:    c.batches,
		Tokens:     c.tokens,
		Cost:       jev.CostUSD(c.tokens),
		Price:      jev.PriceUSDPerMTokInput,
		UpperBound: c.upperBound,
	}
	enc := json.NewEncoder(stdout)
	// The same reasoning as output's encoder: a model name is not HTML and
	// must not be spelled as escapes.
	enc.SetEscapeHTML(false)
	_ = enc.Encode(record)
}

// estimate is the size and the price of the requests one meaning would make.
//
// It counts batches by asking jev.Split, the very function the searcher
// dispatches with, rather than by dividing lines by a batch size written down
// a second time here. What it holds is bounded: lines are split in windows and
// only the last, still-fillable batch is carried over -- which is also what
// the dispatcher does with its own buffer.
type estimate struct {
	meaning string
	buf     []string
	batches int
	tokens  int
}

// estimateWindow is how many lines are held before they are split. It only has
// to be comfortably more than a batch -- splitting in windows and carrying the
// last, still-fillable batch over gives the same count as splitting the whole
// input at once, because Split fills batches left to right. A thousand lines
// is also about what the searcher itself keeps undecided, so a dry run of a
// million-line tree costs what the run it predicts does, not more.
const estimateWindow = 1024

func (e *estimate) add(query string) {
	e.buf = append(e.buf, query)
	if len(e.buf) >= estimateWindow {
		e.split(false)
	}
}

// split counts the batches the buffered lines make. Unless the input is over
// the last one is left behind, because another line may still belong in it.
func (e *estimate) split(final bool) {
	chunks := jev.Split(e.meaning, e.buf)
	if !final {
		chunks = chunks[:max(len(chunks)-1, 0)]
	}
	counted := 0
	for _, chunk := range chunks {
		e.batches++
		e.tokens += jev.EstimateTokens(e.meaning, chunk.Lines)
		counted = chunk.Offset + len(chunk.Lines)
	}
	e.buf = append(e.buf[:0], e.buf[counted:]...)
}

// onlyStdin reports that the single input read was the pipe, which is worth
// saying in the report: "files 1" alone would have the user looking for a file.
func onlyStdin(files *opened) bool {
	name, ok := files.at(0)
	return ok && name == input.StdinName
}
