package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sijiaoh/jevgrep/internal/cli"
)

const skeleton = "# jevgrep\n\n" + beginMarker + "\n" + endMarker + "\n\nTail.\n"

func TestTableRendersEveryOption(t *testing.T) {
	got := table()
	for _, o := range cli.Options {
		if !strings.Contains(got, "`"+o.Flags()+"`") {
			t.Errorf("the table does not list --%s", o.Long)
		}
		if !strings.Contains(got, o.Summary) {
			t.Errorf("the table does not carry the summary of --%s", o.Long)
		}
	}
	// A default is part of what the option does, so it is part of the table.
	if !strings.Contains(got, "(default 0.5)") {
		t.Errorf("the table drops the defaults:\n%s", got)
	}
}

// Every row has to be one line with the same cell count, or the table stops
// being a table for every option after the broken one.
func TestTableRowsAreWellFormed(t *testing.T) {
	for _, row := range strings.Split(strings.TrimSpace(table()), "\n") {
		if !strings.HasPrefix(row, "| ") || !strings.HasSuffix(row, " |") {
			t.Errorf("row is not a table row: %q", row)
		}
		if n := strings.Count(row, "|") - strings.Count(row, `\|`); n != 3 {
			t.Errorf("row has %d cell separators, want 3: %q", n, row)
		}
	}
}

func TestApplyReplacesOnlyTheBlock(t *testing.T) {
	first, err := apply(skeleton, table())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.HasPrefix(first, "# jevgrep\n") || !strings.HasSuffix(first, "\nTail.\n") {
		t.Errorf("apply touched the prose around the block:\n%s", first)
	}
	// Generating over a generated block is what `make readme` does on every
	// run but the first, and it has to be a no-op.
	second, err := apply(first, table())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if second != first {
		t.Errorf("apply is not idempotent:\n%s", drift(second, first))
	}
}

// The markers are pasted in by hand, so the first run may well meet them on
// one line, or without a newline after the begin marker.
func TestApplyToleratesHowTheMarkersWerePastedIn(t *testing.T) {
	for name, doc := range map[string]string{
		"on one line":         "# jevgrep\n" + beginMarker + endMarker + "\n",
		"no trailing newline": "# jevgrep\n" + beginMarker + "\n" + endMarker,
	} {
		got, err := apply(doc, table())
		if err != nil {
			t.Errorf("%s: apply: %v", name, err)
			continue
		}
		if !strings.Contains(got, "| `--help` |") {
			t.Errorf("%s: apply generated nothing into the block:\n%s", name, got)
		}
	}
}

func TestApplyNeedsBothMarkers(t *testing.T) {
	for name, doc := range map[string]string{
		"neither": "# jevgrep\n",
		"begin":   "# jevgrep\n" + beginMarker + "\n",
		"end":     "# jevgrep\n" + endMarker + "\n",
	} {
		if _, err := apply(doc, table()); err == nil {
			t.Errorf("%s: apply accepted a document without the markers", name)
		} else if !strings.Contains(err.Error(), beginMarker) {
			t.Errorf("%s: the error does not say what to paste in: %v", name, err)
		}
	}
}

func TestRunWritesThenChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(path, []byte(skeleton), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run(path, true); err == nil {
		t.Error("-check accepted a README with an empty block")
	}
	if err := run(path, false); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := run(path, true); err != nil {
		t.Errorf("-check rejects what run just wrote: %v", err)
	}

	// A hand-edited row is the drift this exists to catch.
	edited, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(edited), cli.Options[0].Summary, "Something else entirely", 1)
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	err = run(path, true)
	if err == nil {
		t.Fatal("-check accepted an edited table")
	}
	if !strings.Contains(err.Error(), "make readme") || !strings.Contains(err.Error(), "Something else entirely") {
		t.Errorf("the error says neither what drifted nor how to fix it: %v", err)
	}
}

func TestRunReportsAMissingFile(t *testing.T) {
	if err := run(filepath.Join(t.TempDir(), "README.md"), true); err == nil {
		t.Error("run accepted a README that is not there")
	}
}
