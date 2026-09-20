// Command demo reruns the commands the README pins output for and fails when
// what comes back is not what the README says. Run it with `make demo`.
//
// It is not part of `make check`: like `make calibrate`, it needs an API key,
// it talks to the live API and it costs money, so it is opt-in and is run
// before a release rather than on every push.
//
// A README that shows output is a promise, and the only way to keep it is to
// make the promise executable. Every claim here is checked against the file
// itself -- the commands are read out of the README, not restated in this
// program, so there is nothing for the two to disagree about.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sijiaoh/jevgrep/internal/apikey"
)

// The fence a pinned demo is written in, and the prompt every command in one
// starts with. ```console is Markdown's convention for a session rather than a
// script, and it is what tells this program which blocks it owns: a ```sh
// block is something to copy, not something with output to compare.
const (
	consoleFence = "```console"
	fence        = "```"
	prompt       = "$ "
)

// A directive is an HTML comment on the line above a block, invisible in a
// rendered README, saying the one thing about the block that cannot be read
// off it.
//
// Everything else is inferred: a block with no output pins nothing and is not
// run, and a block with output must hold exactly one command. Defaulting to
// "run it" is deliberate -- a new pinned block is checked the day it is
// written, without anyone having to remember to say so here.
const (
	directivePrefix = "<!-- demo:"
	commentEnd      = "-->"
	// skip is for a block that cannot be rerun at all.
	skip = "skip"
	// warm is for a block that shows what a second run does. Its command is
	// run once into the same fresh cache and thrown away, so that what is
	// compared is the second run.
	warm = "warm"
	// scores is for a block that prints probabilities (`-p`). They are a
	// measurement from a model, not a fact about the input: the same line
	// scored twice comes back a few hundredths apart, so a block holding one
	// would fail this program at random. Everything else in the block is
	// still compared byte for byte -- which lines were selected, in what
	// order, with what text -- and each score is required to be within
	// scoreTolerance of the pinned one, so a model that actually moved is
	// still caught.
	scores = "scores"
)

// scoreTolerance is how far a probability may sit from the one pinned in the
// README before the block counts as drifted. Repeated runs of the README's own
// demo moved the widest line by 0.06 (0.78 to 0.84), so 0.1 is the nearest
// round number above what was measured rather than a figure picked to make the
// check pass.
//
// It is deliberately not the thing that stops a real change from slipping
// through: what a -p block prints is the lines the search selected, so a line
// that crossed the threshold leaves the output altogether and is caught by the
// line-for-line comparison, whatever this number is.
const scoreTolerance = 0.1

// scoreField is the score as -p writes it: always four characters, 0.00 to
// 1.00 (internal/output.scoreText). The fixed width is what lets a differing
// score be swapped out without moving anything else on the line.
var scoreField = regexp.MustCompile(`\d\.\d\d`)

// The three normalizations, and nothing else: every other difference is a
// failure. Each is a fact about the machine the demo ran on rather than about
// jevgrep, and each would otherwise make the README unreproducible for its
// reader -- which is the whole point of pinning the output.
const (
	homePlaceholder  = "/home/you"
	cachePlaceholder = homePlaceholder + "/.cache/jevgrep"
	// elapsed is `%.1fs` in a right-aligned column (internal/cli/stats.go),
	// so the value is replaced in place and the column is left as it was.
	elapsedValue = "0.0s"
)

var elapsedLine = regexp.MustCompile(`(?m)^(\s*elapsed\s+)(\S+)$`)

func main() {
	file := flag.String("file", "README.md", "the Markdown file holding the pinned demos")
	bin := flag.String("bin", filepath.Join("dist", "jevgrep"), "the jevgrep binary the demos are run against")
	flag.Parse()

	if err := run(*file, *bin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		os.Exit(1)
	}
}

func run(file, bin string, out io.Writer) error {
	doc, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	blocks, err := parse(string(doc))
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}

	// The key is looked for before anything is run, and only when there is
	// something to run. Not because this program needs one -- it never sees a
	// key, the child processes load it themselves -- but because the
	// alternative is a diff against `no API key found` for every block, which
	// reads as a demo that has drifted rather than as one that never ran.
	if slices.ContainsFunc(blocks, block.runnable) {
		if _, err := apikey.Load(); err != nil {
			if errors.Is(err, apikey.ErrNotFound) {
				return fmt.Errorf("no API key found; set %s or run `jevgrep --login`", apikey.EnvVar)
			}
			return err
		}
	}
	binPath, err := filepath.Abs(bin)
	if err != nil {
		return err
	}
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("%w; run `make build` first", err)
	}

	// The commands are written as a reader would run them, from the root of a
	// checkout, so that is where they are run from.
	dir := filepath.Dir(file)

	var checked, failed int
	for _, b := range blocks {
		if !b.runnable() {
			// Told apart, because they are different states: one is a block
			// this program was told to leave alone, the other is a block that
			// promises nothing and so has nothing to check.
			why := "unpinned"
			if b.directive == skip {
				why = "skipped"
			}
			report(out, why, file, b)
			continue
		}
		have, err := b.run(dir, binPath)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", file, b.line, err)
		}
		checked++
		if b.matches(have) {
			report(out, "ok", file, b)
			continue
		}
		failed++
		report(out, "FAIL", file, b)
		fmt.Fprint(out, diff(b.want, have, b.sameLine))
	}
	// Said out loud, because a demo that checked nothing passes just as
	// quietly as one that checked everything, and the difference is the whole
	// value of the command.
	fmt.Fprintf(out, "%d of the %d console blocks in %s were rerun\n", checked, len(blocks), file)
	if failed > 0 {
		return fmt.Errorf("%d of them no longer print what %s says", failed, file)
	}
	return nil
}

// block is one ```console block of the README.
type block struct {
	// line is where the block's fence is, 1-based, so that a failure names a
	// place an editor can be pointed at.
	line      int
	command   string
	want      string
	directive string
}

// report is one line of the run's report. The verdict comes first so that the
// column of them reads straight down: a file:line is as wide as the line
// number happens to be, and a verdict indented by it cannot be skimmed.
func report(out io.Writer, verdict, file string, b block) {
	fmt.Fprintf(out, "%-8s  %s:%d  %s\n", verdict, file, b.line, b.command)
}

// runnable says whether the block pins anything this program may check. A
// block with no output promises nothing, and a skipped one says on the face of
// the README why it cannot be rerun.
func (b block) runnable() bool {
	return b.directive != skip && b.want != ""
}

// matches says whether what the block printed is still what the README says.
//
// Byte for byte, unless the block is marked `scores`: then two lines also
// match when they are identical apart from the probabilities on them, and
// every pinned probability is within scoreTolerance of the one printed now.
func (b block) matches(have string) bool {
	if have == b.want {
		return true
	}
	if b.directive != scores {
		return false
	}
	// Split rather than lines(): lines() drops the trailing newline, and a
	// command that stopped printing one is a change, not drift.
	w, h := strings.Split(b.want, "\n"), strings.Split(have, "\n")
	if len(w) != len(h) {
		return false
	}
	for i := range w {
		if !b.sameLine(w[i], h[i]) {
			return false
		}
	}
	return true
}

// sameLine is what "this line is still what the README says" means for this
// block. It decides the verdict and it decides what the diff calls a
// difference, so the report cannot disagree with the verdict above it.
func (b block) sameLine(want, have string) bool {
	if want == have {
		return true
	}
	return b.directive == scores && lineMatchesApartFromItsScores(want, have)
}

// lineMatchesApartFromItsScores compares one line, allowing the score fields
// to move a little and nothing else to move at all.
//
// The fields are cut out of both lines and what is left is compared byte for
// byte, so a score that turned into something else -- "?", a missing column, a
// different line number, a changed word -- fails here rather than being waved
// through as drift.
func lineMatchesApartFromItsScores(want, have string) bool {
	wf, hf := scoreFields(want), scoreFields(have)
	if len(wf) != len(hf) {
		return false
	}
	for i := range wf {
		// The field is a fixed four characters wide, so two scores in the same
		// column start at the same offset; one that does not is a line whose
		// shape changed, not a score that moved.
		if wf[i][0] != hf[i][0] {
			return false
		}
		w := mustParseScore(want[wf[i][0]:wf[i][1]])
		h := mustParseScore(have[hf[i][0]:hf[i][1]])
		if math.Abs(w-h) > scoreTolerance {
			return false
		}
	}
	return withoutRanges(want, wf) == withoutRanges(have, hf)
}

// scoreFields is where the score fields are, and only those: a match sitting
// inside a longer number (0.123, 12.34) is not one, and letting it pass as one
// would quietly give a number in the text the same tolerance as a probability.
func scoreFields(line string) [][]int {
	var fields [][]int
	for _, m := range scoreField.FindAllStringIndex(line, -1) {
		if m[0] > 0 && isNumeric(line[m[0]-1]) {
			continue
		}
		if m[1] < len(line) && isNumeric(line[m[1]]) {
			continue
		}
		fields = append(fields, m)
	}
	return fields
}

func isNumeric(c byte) bool { return c == '.' || (c >= '0' && c <= '9') }

// mustParseScore reads a field scoreField already matched, so it is four
// characters of digits and a dot and cannot fail to parse.
func mustParseScore(field string) float64 {
	v, err := strconv.ParseFloat(field, 64)
	if err != nil {
		panic("demo: " + field + " matched " + scoreField.String() + " but does not parse: " + err.Error())
	}
	return v
}

// withoutRanges is the line with the given ranges cut out, which is everything
// about it that is not a score.
func withoutRanges(line string, ranges [][]int) string {
	var b strings.Builder
	at := 0
	for _, r := range ranges {
		b.WriteString(line[at:r[0]])
		at = r[1]
	}
	b.WriteString(line[at:])
	return b.String()
}

// run executes the block's command against a cache of its own and returns what
// it printed, normalized.
//
// The cache is pinned to an empty directory and thrown away afterwards for two
// reasons: it is global (`~/.cache/jevgrep`), so a demo run against whatever
// this machine happens to have cached would be comparing the README to itself,
// and `--dry-run`'s `cached 0` and `--stats`'s `cached 10` are both statements
// about a cache in a known state.
func (b block) run(dir, bin string) (string, error) {
	cacheHome, err := os.MkdirTemp("", "jevgrep-demo-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(cacheHome) }()

	if b.directive == warm {
		if _, err := b.exec(dir, bin, cacheHome); err != nil {
			return "", err
		}
	}
	out, err := b.exec(dir, bin, cacheHome)
	if err != nil {
		return "", err
	}
	return normalize(out, cacheHome), nil
}

// exec runs the command through a shell, because the README writes it for one:
// a pipe in a demo is part of what is being demonstrated.
//
// The binary under test is put first on PATH rather than substituted into the
// command: `jevgrep` is what the README says and what the reader will type,
// and a command line rewritten on the way to being checked is a command line
// nobody checked.
//
// stdout and stderr share one buffer because the README block does too --
// `--stats` prints its table to stderr under the lines it selected on stdout,
// and that interleaving is part of what a reader sees.
func (b block) exec(dir, bin, cacheHome string) (string, error) {
	cmd := exec.Command("sh", "-c", b.command)
	cmd.Dir = dir
	// Appended rather than substituted into the inherited environment: exec
	// keeps the last value of a duplicated key, so these two win over whatever
	// this shell already had -- including an XDG_CACHE_HOME of the user's own,
	// which is the one variable a demo must not inherit.
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"XDG_CACHE_HOME="+cacheHome,
	)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	// A non-zero status is not reported: grep exits 1 on no match and 2 on
	// error, and both of those show up in the output being compared, where
	// they are read as the drift they are rather than as a broken demo.
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return "", err
		}
	}
	return out.String(), nil
}

// normalize applies the three rewrites, and no others.
func normalize(out, cacheHome string) string {
	out = strings.ReplaceAll(out, filepath.Join(cacheHome, "jevgrep"), cachePlaceholder)
	if home := os.Getenv("HOME"); home != "" {
		out = strings.ReplaceAll(out, home, homePlaceholder)
	}
	return elapsedLine.ReplaceAllStringFunc(out, func(line string) string {
		m := elapsedLine.FindStringSubmatch(line)
		// The value is right-aligned in a column, so the padding absorbs the
		// difference in width and the table stays a table.
		label := strings.TrimRight(m[1], " ")
		pad := len(m[1]) + len(m[2]) - len(label) - len(elapsedValue)
		return label + strings.Repeat(" ", max(pad, 1)) + elapsedValue
	})
}

// parse pulls every ```console block out of a Markdown document.
func parse(doc string) ([]block, error) {
	var blocks []block
	lines := strings.Split(doc, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != consoleFence {
			continue
		}
		end := i + 1
		for end < len(lines) && strings.TrimSpace(lines[end]) != fence {
			end++
		}
		if end == len(lines) {
			return nil, fmt.Errorf("line %d: unterminated %s block", i+1, consoleFence)
		}
		directive, comment := directiveAbove(lines[:i])
		if directive != "" && !readsAsAComment(comment) {
			return nil, fmt.Errorf("the directive %q would be shown to the reader rather than hidden; write it without %q", comment, "--")
		}
		b, err := parseBlock(lines[i+1:end], directive)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		b.line = i + 1
		blocks = append(blocks, b)
		i = end
	}
	return blocks, nil
}

// directiveAbove reads the directive comment sitting immediately above a
// block, if there is one, and returns the directive and the line it was
// written on.
func directiveAbove(before []string) (directive, comment string) {
	// Blank lines in between are allowed: whether a comment is separated from
	// the block it describes is a matter of how the Markdown reads, not of
	// what it means.
	i := len(before) - 1
	for i >= 0 && strings.TrimSpace(before[i]) == "" {
		i--
	}
	if i < 0 {
		return "", ""
	}
	line := strings.TrimSpace(before[i])
	if !strings.HasPrefix(line, directivePrefix) {
		return "", ""
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(line, directivePrefix), commentEnd)
	// Whatever follows the directive is the reason it is there, written for
	// the next reader of the README and ignored here.
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", line
	}
	return strings.TrimSuffix(fields[0], ":"), line
}

// readsAsAComment says whether a renderer will hide the directive line. A
// comment holding "--" is not a comment to CommonMark, and a README whose
// invisible note is printed to the reader as text is a worse bug than the
// drift this program exists to catch -- so a directive that spells an option
// out is refused here, where it costs one word to reword, rather than found on
// the project's front page. "--" is illegal inside an HTML comment either way.
func readsAsAComment(line string) bool {
	body := strings.TrimSuffix(strings.TrimPrefix(line, "<!--"), commentEnd)
	return !strings.Contains(body, "--") && !strings.HasSuffix(body, "-")
}

func parseBlock(body []string, directive string) (block, error) {
	switch directive {
	case "", skip, warm, scores:
	default:
		return block{}, fmt.Errorf("unknown directive %q, want one of %q, %q, %q", directive, skip, warm, scores)
	}

	var cmds []string
	var want []string
	for _, line := range body {
		switch {
		case strings.HasPrefix(line, prompt):
			if len(want) > 0 {
				return block{}, errors.New("a command follows pinned output; split the block in two")
			}
			cmds = append(cmds, strings.TrimPrefix(line, prompt))
		case len(cmds) == 0:
			return block{}, fmt.Errorf("output before any %q command", strings.TrimSpace(prompt))
		default:
			want = append(want, line)
		}
	}
	switch {
	case len(cmds) == 0:
		return block{}, fmt.Errorf("no %q command", strings.TrimSpace(prompt))
	case len(want) > 0 && len(cmds) > 1:
		return block{}, errors.New("pinned output cannot be told apart from several commands; split the block in two")
	}
	b := block{command: cmds[0], directive: directive}
	if len(want) > 0 {
		b.want = strings.Join(want, "\n") + "\n"
	}
	// A block showing several commands and no output pins nothing, and is
	// named in the report by its first command.
	return b, nil
}

// diff renders what changed, line by line and whole, because a pinned demo is
// short and the interesting part is as often a line that went missing as a
// line that came out different.
//
// Runs of differing lines are printed as two groups rather than interleaved
// pair by pair: a report table that moved by one row differs on every line
// from there down, and read alternately neither version can be read at all.
//
// What counts as a difference is the block's own sameLine, not string
// equality: on a `scores` block a probability that merely jittered is not what
// went wrong, and printing it as though it were buries the line that did.
func diff(want, have string, sameLine func(want, have string) bool) string {
	w, h := lines(want), lines(have)

	shared := func(i int) bool { return i < len(w) && i < len(h) && sameLine(w[i], h[i]) }

	var b strings.Builder
	for i := 0; i < max(len(w), len(h)); {
		if shared(i) {
			fmt.Fprintf(&b, "               %s\n", w[i])
			i++
			continue
		}
		end := i
		for end < max(len(w), len(h)) && !shared(end) {
			end++
		}
		for _, line := range w[min(i, len(w)):min(end, len(w))] {
			fmt.Fprintf(&b, "        README %s\n", line)
		}
		for _, line := range h[min(i, len(h)):min(end, len(h))] {
			fmt.Fprintf(&b, "        now    %s\n", line)
		}
		i = end
	}
	return b.String()
}

// lines splits output into the lines it holds. Output that is empty holds
// none, rather than the one blank line a plain split would report -- a command
// that printed nothing and a command that printed an empty line are different
// answers, and the diff is where that shows.
func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
