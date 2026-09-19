package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sijiaoh/jevgrep/internal/jev"
)

// defaultThreshold is the score a line has to reach to match. It lives here
// rather than in the option table because the parser needs it as a number and
// the table needs it as the text --help prints.
const defaultThreshold = 0.5

// Option is one command line option, as --help and (from M4) the README both
// render it. Nothing about an option is written down anywhere else: a second
// copy of a summary is a second copy that drifts.
type Option struct {
	// Short is the single letter form without its dash, empty if there is none.
	Short string
	// Long is the long form without its dashes.
	Long string
	// Arg is the placeholder for the option's argument, empty if it takes none.
	Arg string
	// Summary is one English line, without the default value.
	Summary string
	// Default is the default value as a literal, empty if there is none. It is
	// rendered as " (default X)".
	Default string
	// ArgOptional marks an option whose argument may only be given with "=",
	// the way GNU tools spell an optional argument. Without it "--color" would
	// swallow the next word, and that word is usually the MEANING.
	//
	// Only the long form takes it into account. No option has both a short
	// form and an optional argument, and one that did would need the short
	// form's parsing taught the same rule -- the argument attached to the
	// letter or nothing at all.
	ArgOptional bool
}

// Options are jevgrep's options in the order --help lists them. The order is
// meaning, then how to match, then which files to search, then how to print,
// then the rest: related options next to each other are worth more to a reader
// than alphabetical order.
var Options = []Option{
	{Short: "e", Long: "meaning", Arg: "MEANING", Summary: "Add a meaning; repeat to match any of them"},
	{Long: "and", Arg: "MEANING", Summary: "Also require this meaning on the same line"},
	{Long: "not", Arg: "MEANING", Summary: "Reject the lines that have this meaning"},
	{Short: "v", Long: "invert-match", Summary: "Select the lines that do not match"},
	{Short: "t", Long: "threshold", Arg: "NUM", Summary: "Match at a score of NUM or above", Default: strconv.FormatFloat(defaultThreshold, 'g', -1, 64)},
	{Short: "r", Long: "recursive", Summary: "Search the files under each directory"},
	{Short: "g", Long: "glob", Arg: "GLOB", Summary: "Search only the paths matching GLOB, or skip !GLOB"},
	{Long: "hidden", Summary: "Also search hidden files and directories"},
	{Long: "no-ignore", Summary: "Do not obey .gitignore and .ignore files"},
	{Short: "n", Long: "line-number", Summary: "Prefix each output line with its line number"},
	{Short: "H", Long: "with-filename", Summary: "Print the file name with each output line"},
	{Short: "h", Long: "no-filename", Summary: "Never print the file name"},
	{Short: "l", Long: "files-with-matches", Summary: "Print only the name of each file that matched"},
	{Short: "L", Long: "files-without-match", Summary: "Print only the name of each file that did not match"},
	{Short: "c", Long: "count", Summary: "Print only the number of matching lines per file"},
	{Short: "q", Long: "quiet", Summary: "Print nothing; stop at the first match"},
	{Short: "m", Long: "max-count", Arg: "NUM", Summary: "Stop after NUM matching lines per file"},
	{Short: "A", Long: "after-context", Arg: "NUM", Summary: "Print NUM lines after each matching line"},
	{Short: "B", Long: "before-context", Arg: "NUM", Summary: "Print NUM lines before each matching line"},
	{Short: "C", Long: "context", Arg: "NUM", Summary: "Print NUM lines before and after each matching line"},
	{Short: "p", Long: "score", Summary: "Print each line's score before its text"},
	{Long: "json", Summary: "Print one JSON object per line of output"},
	{Short: "Z", Long: "null", Summary: "Terminate each file name with a NUL byte"},
	{Long: "color", Arg: "WHEN", ArgOptional: true, Summary: "Color output: always, never, auto", Default: colorAuto},
	{Long: "model", Arg: "NAME", Summary: "Model that scores the lines", Default: jev.DefaultModel},
	{Long: "login", Summary: "Store an API key for later runs, then exit"},
	{Short: "V", Long: "version", Summary: "Print the version and exit"},
	{Long: "help", Summary: "Print this help and exit"},
}

// The two forms of the command line. usage is the one-liner the error path
// prints; both appear in --help.
const (
	usageWithMeaning = "jevgrep [OPTIONS] MEANING [PATH ...]"
	usageWithFlags   = "jevgrep [OPTIONS] -e MEANING [-e MEANING ...] [PATH ...]"

	usage = "Usage: " + usageWithMeaning
)

const homePage = "https://github.com/sijiaoh/jevgrep"

// summaryColumn is the column every option summary starts in, counted from
// zero. An option whose flags reach it gets its summary on the next line, so
// that the summaries stay a single readable column however long a future
// option's name is.
//
// It is bounded from both sides: the longest flags rendered today
// ("-L, --files-without-match") need 27 columns and must not wrap for nothing,
// and TestHelpLayout holds the whole line to 80. Raising it further buys
// nothing and costs every summary the room it takes -- which is why a couple
// of them are worded shorter than they might otherwise be.
const summaryColumn = 29

// help is the whole --help page. It names the API host so that nobody can
// discover only after their first bill that the lines left the machine.
func help() string {
	var b strings.Builder
	fmt.Fprintf(&b, `jevgrep greps lines by meaning: every line is scored by a remote model
instead of matched against a pattern.

Usage:
  %s
  %s

Every line searched is sent to %s to be scored.

Options:
%s
With no PATH, jevgrep reads standard input, or searches . under -r.
With PATH as -, it reads standard input.
Exit status is %d if a line matched, %d if none did, %d on error.

Home page: %s
`, usageWithMeaning, usageWithFlags, apiHost(), renderOptions(), ExitMatch, ExitNoMatch, ExitError, homePage)
	return b.String()
}

func apiHost() string {
	_, host, _ := strings.Cut(jev.DefaultBaseURL, "://")
	return host
}

// renderOptions lays the option table out as --help shows it.
func renderOptions() string {
	var b strings.Builder
	for _, o := range Options {
		flags := "  "
		if o.Short != "" {
			flags += "-" + o.Short + ", "
		} else {
			// Indented to the column the long names of the options that do
			// have a short form start in.
			flags += "    "
		}
		flags += "--" + o.Long
		switch {
		// An optional argument is rendered the way it has to be written.
		case o.ArgOptional:
			flags += "[=" + o.Arg + "]"
		case o.Arg != "":
			flags += " " + o.Arg
		}

		b.WriteString(flags)
		if len(flags) >= summaryColumn {
			b.WriteString("\n")
			b.WriteString(strings.Repeat(" ", summaryColumn))
		} else {
			b.WriteString(strings.Repeat(" ", summaryColumn-len(flags)))
		}
		b.WriteString(o.Summary)
		if o.Default != "" {
			fmt.Fprintf(&b, " (default %s)", renderDefault(o.Default))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderDefault quotes a default value unless it is a number, which is what
// the standard flag package does and therefore what users are used to reading.
func renderDefault(value string) string {
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return value
	}
	return strconv.Quote(value)
}

// longOption and shortOption find an option by the form the user wrote. The
// table is short enough that a scan beats a map built at init, and a scan
// cannot go stale.
func longOption(name string) *Option {
	for i := range Options {
		if Options[i].Long == name {
			return &Options[i]
		}
	}
	return nil
}

func shortOption(name string) *Option {
	for i := range Options {
		if Options[i].Short != "" && Options[i].Short == name {
			return &Options[i]
		}
	}
	return nil
}
