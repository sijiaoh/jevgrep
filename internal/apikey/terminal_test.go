package apikey_test

import (
	"os"
	"testing"

	"github.com/sijiaoh/jevgrep/internal/apikey"
)

// The real Terminal is only as good as its answer for things that are not
// terminals: os.Stat's ModeCharDevice, the stdlib stand-in, calls /dev/null one
// and that is the case jevgrep meets every time it is run from a script.
func TestOSTerminalRejectsStreamsThatAreNotTerminals(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devNull.Close() })

	pipe, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pipe.Close()
		_ = w.Close()
	})

	for name, f := range map[string]*os.File{"devnull": devNull, "pipe": pipe} {
		t.Run(name, func(t *testing.T) {
			// --login asks about stdin and stderr, the parser about stdin; all
			// three streams go through the same mapping, so all three are
			// checked here.
			defer swapStd(t, f)()

			for _, s := range []apikey.Stream{apikey.Stdin, apikey.Stdout, apikey.Stderr} {
				if (apikey.OSTerminal{}).IsTerminal(s) {
					t.Errorf("IsTerminal(%s) = true for %s, want false", streamName(s), name)
				}
			}
			// Reading a secret needs the same terminal the check asks about,
			// so it must fail rather than read the stream in the clear.
			if _, err := (apikey.OSTerminal{}).ReadSecret(); err == nil {
				t.Errorf("ReadSecret() = nil error for %s, want a failure", name)
			}
		})
	}
}

// A Stream that names nothing is not a terminal, rather than a panic or a read
// of file descriptor 0 by accident.
func TestOSTerminalRejectsAnUnknownStream(t *testing.T) {
	if (apikey.OSTerminal{}).IsTerminal(apikey.Stream(42)) {
		t.Error("IsTerminal(42) = true, want false")
	}
}

// swapStd points all three standard streams at f and returns the undo.
func swapStd(t *testing.T, f *os.File) func() {
	t.Helper()
	in, out, errOut := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = f, f, f
	return func() { os.Stdin, os.Stdout, os.Stderr = in, out, errOut }
}
