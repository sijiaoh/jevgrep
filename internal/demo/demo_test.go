package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sijiaoh/jevgrep/internal/apikey"
)

func TestParseReadsCommandsAndTheirPinnedOutput(t *testing.T) {
	doc := "intro\n\n" +
		"```console\n$ jevgrep -n \"a disk error\" app.log\n4:oh no\n```\n\n" +
		"```sh\ncurl -fsSL example.invalid | sh\n```\n\n" +
		"<!-- demo: skip: it would prompt for a key -->\n\n" +
		"```console\n$ jevgrep --login\nkey saved\n```\n\n" +
		"<!-- demo: warm -->\n```console\n$ jevgrep --stats -n \"x\" app.log\nstats\n```\n\n" +
		"```console\n$ jevgrep --dry-run -rn \"x\" .\n$ jevgrep -rn \"x\" .\n```\n"

	blocks, err := parse(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []block{
		{line: 3, command: `jevgrep -n "a disk error" app.log`, want: "4:oh no\n"},
		{line: 14, command: "jevgrep --login", want: "key saved\n", directive: skip},
		{line: 20, command: `jevgrep --stats -n "x" app.log`, want: "stats\n", directive: warm},
		{line: 25, command: `jevgrep --dry-run -rn "x" .`},
	}
	if len(blocks) != len(want) {
		t.Fatalf("got %d blocks, want %d: %+v", len(blocks), len(want), blocks)
	}
	for i, b := range blocks {
		if b != want[i] {
			t.Errorf("block %d:\n got %+v\nwant %+v", i, b, want[i])
		}
	}
}

func TestParseRejectsABlockItCannotCheck(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown directive":    "<!-- demo: warmish -->\n```console\n$ jevgrep x\nout\n```\n",
		"output before prompt": "```console\nout\n$ jevgrep x\n```\n",
		"no command":           "```console\njust prose\n```\n",
		"output after two":     "```console\n$ jevgrep a\n$ jevgrep b\nout\n```\n",
		"unterminated":         "```console\n$ jevgrep x\n",
		// A comment holding "--" is not a comment to CommonMark, so the note
		// meant to be invisible would be printed on the project's front page.
		"directive that would be shown to the reader": "<!-- demo: skip: --login prompts -->\n```console\n$ jevgrep x\nout\n```\n",
		"directive ending in a hyphen":                "<!-- demo: skip: rerun it later--->\n```console\n$ jevgrep x\nout\n```\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(doc); err == nil {
				t.Fatal("parsed a block that cannot be checked")
			}
		})
	}
}

func TestNormalizeRewritesTheThreeThingsTheMachineDecides(t *testing.T) {
	t.Setenv("HOME", "/home/somebody")
	cacheHome := filepath.Join(os.TempDir(), "jevgrep-demo-123")

	out := "  elapsed           1.4s\n" +
		"  cache        " + filepath.Join(cacheHome, "jevgrep") + "  (delete it to clear)\n" +
		"key saved to /home/somebody/.config/jevgrep/api_key\n"
	want := "  elapsed           0.0s\n" +
		"  cache        /home/you/.cache/jevgrep  (delete it to clear)\n" +
		"key saved to /home/you/.config/jevgrep/api_key\n"

	if got := normalize(out, cacheHome); got != want {
		t.Errorf("normalize:\n got %q\nwant %q", got, want)
	}
}

// The column is what makes the report readable, and a slower run must not be
// reported as a shifted table.
func TestNormalizeKeepsTheElapsedColumnWhereItWas(t *testing.T) {
	// The layout internal/cli/stats.go prints: the label in a column of its
	// own, the value right-aligned in the next one.
	for _, s := range []string{"0.4s", "12.7s", "1.0s", "123.4s"} {
		got := normalize(fmt.Sprintf("  %-13s%9s\n", "elapsed", s), t.TempDir())
		if got != "  elapsed           0.0s\n" {
			t.Errorf("elapsed %q normalized to %q", s, got)
		}
	}
}

func TestNormalizeLeavesEverythingElseAlone(t *testing.T) {
	out := "4:2026-09-18 03:12:19 ERROR write /var/lib/pg/base/16384: input/output error\n"
	if got := normalize(out, t.TempDir()); got != out {
		t.Errorf("normalize changed a line it does not own:\n got %q\nwant %q", got, out)
	}
}

// A directive says something about the block under it and nothing about the
// next one, which would otherwise be skipped or run twice for no stated reason.
func TestADirectiveDoesNotReachTheBlockAfterTheOneItIsAbove(t *testing.T) {
	blocks, err := parse("<!-- demo: skip: the one below -->\n" +
		"```console\n$ jevgrep one\nout\n```\n\n" +
		"```console\n$ jevgrep two\nout\n```\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if blocks[0].directive != skip {
		t.Errorf("the block under the directive got %q, want %q", blocks[0].directive, skip)
	}
	if blocks[1].directive != "" {
		t.Errorf("the next block got %q, want no directive", blocks[1].directive)
	}
}

func TestDiffTellsNothingPrintedFromAnEmptyLine(t *testing.T) {
	if got, want := diff("4:oh no\n", "", block{}.sameLine), "        README 4:oh no\n"; got != want {
		t.Errorf("a command that printed nothing:\n got %q\nwant %q", got, want)
	}
	if got, want := diff("4:oh no\n", "\n", block{}.sameLine), "        README 4:oh no\n        now    \n"; got != want {
		t.Errorf("a command that printed a blank line:\n got %q\nwant %q", got, want)
	}
}

func TestRunSaysToBuildTheBinaryItWasPointedAt(t *testing.T) {
	t.Setenv(apikey.EnvVar, "not-a-real-key")

	dir := t.TempDir()
	readme := filepath.Join(dir, "README.md")
	write(t, readme, "```console\n$ jevgrep x\nout\n```\n")

	err := run(readme, filepath.Join(dir, "dist", "jevgrep"), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "make build") {
		t.Fatalf("got %v, want an error saying to run make build", err)
	}
}

// A README with nothing pinned costs nothing to check, so it must not be the
// thing that sends someone looking for a key.
func TestRunNeedsNoKeyWhenThereIsNothingToRun(t *testing.T) {
	t.Setenv(apikey.EnvVar, "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	dir := t.TempDir()
	bin := fakeJevgrep(t, dir, "echo unused\n")
	readme := filepath.Join(dir, "README.md")
	write(t, readme,
		"<!-- demo: skip: it would prompt for a key -->\n```console\n$ jevgrep --login\nkey saved\n```\n\n"+
			"```console\n$ jevgrep --dry-run -rn \"x\" .\n$ jevgrep -rn \"x\" .\n```\n")

	var out strings.Builder
	if err := run(readme, bin, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	// And the report says which of the two it is, because they are not the
	// same thing to whoever reads it.
	for command, want := range map[string]string{
		"jevgrep --login":   "skipped",
		"jevgrep --dry-run": "unpinned",
	} {
		if got := verdictFor(t, out.String(), command); got != want {
			t.Errorf("%q is reported as %q, want %q", command, got, want)
		}
	}
}

func TestRunSaysWhereToGetAKeyBeforeSpendingAnything(t *testing.T) {
	// Both sources emptied: neither the environment nor a key file on this
	// machine may answer for the one being tested.
	t.Setenv(apikey.EnvVar, "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	dir := t.TempDir()
	readme := filepath.Join(dir, "README.md")
	write(t, readme, "```console\n$ jevgrep x\nout\n```\n")

	err := run(readme, filepath.Join(dir, "jevgrep"), io.Discard)
	if err == nil || !strings.Contains(err.Error(), apikey.EnvVar) {
		t.Fatalf("got %v, want an error naming %s", err, apikey.EnvVar)
	}
}

func TestRunRerunsEveryPinnedBlockAndReportsDrift(t *testing.T) {
	needsShell(t)
	t.Setenv(apikey.EnvVar, "not-a-real-key")

	dir := t.TempDir()
	// A stand-in for jevgrep: it never talks to anything, it just prints what
	// the README fixture below claims it prints, and it records that it ran.
	bin := fakeJevgrep(t, dir, `
case "$1" in
--login)  echo "login ran" >> "$LOG"; echo "key saved" ;;
--stats)  if [ -f "$XDG_CACHE_HOME/seen" ]; then echo "second run"; else : > "$XDG_CACHE_HOME/seen"; echo "first run"; fi ;;
--drift)  echo "what it prints now" ;;
*)        echo "4:oh no" ;;
esac
`)
	readme := filepath.Join(dir, "README.md")
	write(t, readme,
		"```console\n$ jevgrep -n \"a disk error\" app.log\n4:oh no\n```\n\n"+
			"<!-- demo: skip: it would prompt for a key -->\n```console\n$ jevgrep --login\nkey saved\n```\n\n"+
			"<!-- demo: warm -->\n```console\n$ jevgrep --stats\nsecond run\n```\n")

	var out strings.Builder
	if err := run(readme, bin, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if n := strings.Count(out.String(), "ok        "); n != 2 {
		t.Errorf("got %d blocks checked, want 2:\n%s", n, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "log")); err == nil {
		t.Error("a skipped block was run anyway")
	}
	// How much of the README was checked is the one number a release-time
	// command has to say, or checking nothing looks like checking everything.
	if want := "2 of the 3 console blocks"; !strings.Contains(out.String(), want) {
		t.Errorf("the report does not say %q:\n%s", want, out.String())
	}

	// The same fixture, with one block's pinned output no longer true.
	write(t, readme, "```console\n$ jevgrep --drift\nwhat the README says\n```\n")
	out.Reset()
	err := run(readme, bin, &out)
	if err == nil {
		t.Fatal("drifted output was reported as up to date")
	}
	for _, want := range []string{"README what the README says", "now    what it prints now"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the report does not show %q:\n%s", want, out.String())
		}
	}
}

// Every block gets an empty cache of its own: the first run of one block must
// not be what makes the next one look cached.
func TestEachBlockRunsAgainstACacheOfItsOwn(t *testing.T) {
	needsShell(t)
	t.Setenv(apikey.EnvVar, "not-a-real-key")

	dir := t.TempDir()
	bin := fakeJevgrep(t, dir, `
if [ -f "$XDG_CACHE_HOME/seen" ]; then echo "cached"; else : > "$XDG_CACHE_HOME/seen"; echo "sent"; fi
`)
	readme := filepath.Join(dir, "README.md")
	write(t, readme,
		"```console\n$ jevgrep one\nsent\n```\n\n"+
			"```console\n$ jevgrep two\nsent\n```\n")

	var out strings.Builder
	if err := run(readme, bin, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
}

// verdictFor returns the first word of the report line naming a command.
func verdictFor(t *testing.T, report, command string) string {
	t.Helper()
	for line := range strings.SplitSeq(report, "\n") {
		if strings.Contains(line, command) {
			return strings.Fields(line)[0]
		}
	}
	t.Fatalf("no report line for %q:\n%s", command, report)
	return ""
}

func needsShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the README's demos are written for a POSIX shell")
	}
}

// fakeJevgrep writes a shell script named jevgrep, which is how the commands
// read out of the README find it: they say `jevgrep`, and its directory goes
// on PATH.
func fakeJevgrep(t *testing.T, dir, body string) string {
	t.Helper()
	bin := filepath.Join(dir, "bin", "jevgrep")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nLOG=" + filepath.Join(dir, "log") + "\n" + body
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A `scores` block is the one place where "what the README says" is a range
// rather than a string, so each half of that has to be pinned: what it lets
// through, and what it still catches.
func TestAScoresBlockToleratesADriftingProbabilityAndNothingElse(t *testing.T) {
	pinned := "4:0.96:write failed\n6:0.80:uncorrectable sector\n"
	for name, tc := range map[string]struct {
		have  string
		match bool
	}{
		"unchanged":                  {pinned, true},
		"within the tolerance":       {"4:0.97:write failed\n6:0.72:uncorrectable sector\n", true},
		"beyond the tolerance":       {"4:0.96:write failed\n6:0.69:uncorrectable sector\n", false},
		"a line that lost its score": {"4:0.96:write failed\n6:?:uncorrectable sector\n", false},
		"a different line selected":  {"4:0.96:write failed\n7:0.80:uncorrectable sector\n", false},
		"different text":             {"4:0.96:write failed\n6:0.80:uncorrectable secto\n", false},
		"a line that went missing":   {"4:0.96:write failed\n", false},
		"an extra line":              {pinned + "9:0.90:no space left\n", false},
		// lines() drops a trailing newline; matches() must not, or a command
		// that stopped ending its output with one would pass unnoticed.
		"no trailing newline": {strings.TrimSuffix(pinned, "\n"), false},
	} {
		t.Run(name, func(t *testing.T) {
			b := block{want: pinned, directive: scores}
			if got := b.matches(tc.have); got != tc.match {
				t.Errorf("matches(%q) = %v, want %v", tc.have, got, tc.match)
			}
			// Without the directive the same output is compared byte for
			// byte: the tolerance is something a block opts into.
			plain := block{want: pinned}
			if got := plain.matches(tc.have); got != (tc.have == pinned) {
				t.Errorf("without the directive, matches(%q) = %v", tc.have, got)
			}
		})
	}
}

// The tolerance is for the probability column and nothing else. A longer
// number that merely contains something shaped like one is text, and text is
// compared byte for byte.
func TestANumberInsideTheTextIsNotGivenTheScoreTolerance(t *testing.T) {
	for name, tc := range map[string]struct {
		want, have string
		match      bool
	}{
		"a longer number in the text": {"4:0.96:took 0.123 seconds\n", "4:0.96:took 0.183 seconds\n", false},
		"a number with two digits":    {"4:0.96:took 12.34 seconds\n", "4:0.96:took 12.39 seconds\n", false},
		"the score itself still moves": {
			"4:0.96:took 0.123 seconds\n", "4:0.92:took 0.123 seconds\n", true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := block{want: tc.want, directive: scores}
			if got := b.matches(tc.have); got != tc.match {
				t.Errorf("matches(%q) against %q = %v, want %v", tc.have, tc.want, got, tc.match)
			}
		})
	}
}

// A `scores` block that really drifted must read as one changed line, not as
// three: the probabilities on the lines that are fine jitter too, and a diff
// that reports them as differences hides the one that matters.
func TestADiffOfAScoresBlockDoesNotReportTheJitterAroundTheChange(t *testing.T) {
	b := block{directive: scores, want: "4:0.96:write failed\n6:0.80:uncorrectable sector\n"}
	have := "4:0.97:write failed\n6:0.81:uncorrectable sectors\n"
	if b.matches(have) {
		t.Fatal("a changed line was taken for drift")
	}
	got := diff(b.want, have, b.sameLine)
	// The untouched line is shown as context (unprefixed), not as a pair of
	// README/now lines, and it is shown as the README writes it.
	if !strings.HasPrefix(got, "               4:0.96:write failed\n") || strings.Contains(got, "0.97") {
		t.Errorf("the untouched line is reported as a difference:\n%s", got)
	}
	for _, want := range []string{"README 6:0.80:uncorrectable sector", "now    6:0.81:uncorrectable sectors"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff does not show %q:\n%s", want, got)
		}
	}
}
