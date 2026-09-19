package cli

import (
	"strings"
	"testing"
)

// --help is read in a terminal that may be 80 columns wide, and its summaries
// are only a column while they all start in the same place. Both are properties
// of the renderer, so they hold for options added later too.
func TestHelpLayout(t *testing.T) {
	for _, line := range strings.Split(renderOptions(), "\n") {
		if line == "" {
			continue
		}
		if len(line) > 80 {
			t.Errorf("option line is %d columns wide, want at most 80: %q", len(line), line)
		}
		if len(line) <= summaryColumn || line[summaryColumn] == ' ' || line[summaryColumn-1] != ' ' {
			t.Errorf("summary of %q does not start in column %d", line, summaryColumn)
		}
	}
}

func TestHelpRendersEveryOption(t *testing.T) {
	page := help()
	for _, o := range Options {
		if !strings.Contains(page, "--"+o.Long) {
			t.Errorf("--help does not mention --%s", o.Long)
		}
		if !strings.Contains(page, o.Summary) {
			t.Errorf("--help does not mention the summary of --%s", o.Long)
		}
	}
}

// The options jevgrep does not have yet must not be advertised: a summary is a
// promise, and M1 cannot keep these.
func TestHelpPromisesNothingUnimplemented(t *testing.T) {
	for _, flag := range []string{"-r", "--recursive", "--color", "--json", "--stats", "--jobs", "--dry-run", "-c", "-q"} {
		if strings.Contains(help(), flag+" ") {
			t.Errorf("--help mentions %s, which jevgrep does not have", flag)
		}
	}
}

func TestDefaultsAreRenderedTheWayFlagDoes(t *testing.T) {
	if got := renderDefault("0.5"); got != "0.5" {
		t.Errorf("renderDefault(0.5) = %s, want it unquoted", got)
	}
	if got := renderDefault("jev-latest"); got != `"jev-latest"` {
		t.Errorf("renderDefault(jev-latest) = %s, want it quoted", got)
	}
}
