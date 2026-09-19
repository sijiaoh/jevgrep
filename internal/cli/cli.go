// Package cli implements the jevgrep command line.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/sijiaoh/jevgrep/internal/buildinfo"
)

// Exit codes follow grep: 0 matched, 1 no match, 2 error. Callers of Run pass
// the result straight to os.Exit.
const (
	ExitMatch   = 0
	ExitNoMatch = 1
	ExitError   = 2
)

const usage = `Usage: jevgrep [OPTIONS] MEANING [PATH ...]`

// Run executes jevgrep with the given arguments (excluding the program name)
// and returns the process exit code. All output goes to the given writers so
// that tests never touch the real stdout/stderr.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("jevgrep", flag.ContinueOnError)
	// flag writes only its parse-error message here; usage is printed by the
	// branches below, because flag would otherwise also emit it on stderr for
	// --help, where it belongs on stdout.
	fs.SetOutput(stderr)
	fs.Usage = func() {}

	showVersion := fs.Bool("version", false, "print version and exit")

	switch err := fs.Parse(args); {
	// grep prints its help on stdout and exits 0 when the user asked for it.
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprintln(stdout, usage)
		return ExitMatch
	case err != nil:
		fmt.Fprintln(stderr, usage)
		return ExitError
	}

	if *showVersion {
		fmt.Fprintf(stdout, "jevgrep %s\n", buildinfo.Version())
		return ExitMatch
	}

	fmt.Fprintln(stderr, usage)
	return ExitError
}
