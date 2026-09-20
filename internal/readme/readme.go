// Command readme keeps the README's option table equal to cli.Options: run it
// to rewrite the table, run it with -check to fail when the two have drifted.
//
// It is a program rather than a test so that the fix is a command and not a
// hand edit, and it lives under internal/ because it is not something a user
// of jevgrep installs.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sijiaoh/jevgrep/internal/cli"
)

// The markers delimiting the generated block. They are HTML comments so that
// they are invisible in a rendered README, and they say where the table comes
// from because the first instinct on seeing a stale table is to edit it here.
const (
	beginMarker = "<!-- BEGIN OPTIONS: generated from cli.Options by `make readme`; do not edit -->"
	endMarker   = "<!-- END OPTIONS -->"
)

func main() {
	check := flag.Bool("check", false, "report drift instead of rewriting the file, and exit non-zero")
	path := flag.String("file", "README.md", "the Markdown file holding the generated block")
	flag.Parse()

	if err := run(*path, *check); err != nil {
		fmt.Fprintf(os.Stderr, "readme: %v\n", err)
		os.Exit(1)
	}
}

func run(path string, check bool) error {
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	want, err := apply(string(current), table())
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if want == string(current) {
		return nil
	}
	if check {
		return fmt.Errorf("%s: the option table is out of date; run `make readme`\n%s", path, drift(string(current), want))
	}
	// 0o644 is only used when the file does not exist, and it cannot: it was
	// read above.
	return os.WriteFile(path, []byte(want), 0o644)
}

// errNoMarkers is what a README without the generated block gets. It names
// both markers, because the block has to be pasted in before anything can be
// generated into it.
var errNoMarkers = errors.New("no generated option table found; the README needs the two marker lines\n" +
	beginMarker + "\n" + endMarker)

// apply replaces whatever sits between the markers with block. It cuts on the
// markers alone rather than on whole lines so that a README whose block is
// still being pasted together -- no newline after the begin marker yet -- is
// generated into rather than rejected.
func apply(doc, block string) (string, error) {
	before, rest, found := strings.Cut(doc, beginMarker)
	if !found {
		return "", errNoMarkers
	}
	_, after, found := strings.Cut(rest, endMarker)
	if !found {
		return "", errNoMarkers
	}
	return before + beginMarker + "\n" + block + endMarker + after, nil
}

// table renders cli.Options as a Markdown table. The flags and the summary are
// rendered by cli itself, so the README says exactly what --help says.
func table() string {
	var b strings.Builder
	b.WriteString("\n| Option | Description |\n| --- | --- |\n")
	for _, o := range cli.Options {
		fmt.Fprintf(&b, "| `%s` | %s |\n", o.Flags(), escape(o.Summary+o.DefaultNote()))
	}
	b.WriteString("\n")
	return b.String()
}

// escape hides the one character that would end a Markdown table cell early.
// A summary has no reason to contain a pipe, but a table silently losing half
// a sentence is not the way to find out that one does.
func escape(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}

// drift reports the first line the two versions disagree on, which is enough
// to see which option was added, removed or reworded.
func drift(got, want string) string {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range max(len(gotLines), len(wantLines)) {
		g, w := at(gotLines, i), at(wantLines, i)
		if g != w {
			return fmt.Sprintf("line %d:\n  have: %s\n  want: %s", i+1, g, w)
		}
	}
	return ""
}

func at(lines []string, i int) string {
	if i >= len(lines) {
		return "(end of file)"
	}
	return lines[i]
}
