package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sijiaoh/jevgrep/internal/buildinfo"
	"github.com/sijiaoh/jevgrep/internal/cli"
)

const usage = "Usage: jevgrep"

func TestRun(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		// Both streams are asserted on every case: jevgrep is meant to be used in
		// pipes, so what lands on stdout vs stderr is part of the contract.
		wantStdout string
		wantStderr string
	}{
		{
			name:       "version goes to stdout",
			args:       []string{"--version"},
			wantCode:   cli.ExitMatch,
			wantStdout: "jevgrep " + buildinfo.Version() + "\n",
		},
		{
			name:       "single dash version is accepted too",
			args:       []string{"-version"},
			wantCode:   cli.ExitMatch,
			wantStdout: "jevgrep " + buildinfo.Version() + "\n",
		},
		{
			name:       "requested help goes to stdout and succeeds",
			args:       []string{"--help"},
			wantCode:   cli.ExitMatch,
			wantStdout: usage,
		},
		{
			name:       "-h behaves like --help",
			args:       []string{"-h"},
			wantCode:   cli.ExitMatch,
			wantStdout: usage,
		},
		{
			name:       "no arguments is an error with usage on stderr",
			args:       nil,
			wantCode:   cli.ExitError,
			wantStderr: usage,
		},
		{
			name:       "unknown flag reports the flag and the usage on stderr",
			args:       []string{"--nope"},
			wantCode:   cli.ExitError,
			wantStderr: "flag provided but not defined: -nope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.Run(tt.args, &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			assertStream(t, "stdout", stdout.String(), tt.wantStdout)
			assertStream(t, "stderr", stderr.String(), tt.wantStderr)
		})
	}
}

// assertStream requires want as a substring, and an empty want as an empty
// stream, so that every case pins the stream it must *not* write to.
func assertStream(t *testing.T, name, got, want string) {
	t.Helper()

	if want == "" {
		if got != "" {
			t.Errorf("%s = %q, want it to be empty", name, got)
		}
		return
	}
	if !strings.Contains(got, want) {
		t.Errorf("%s = %q, want it to contain %q", name, got, want)
	}
}

// The usage line is the error path's only output, so an error must never print
// it more than once.
func TestRunPrintsUsageOnce(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cli.Run([]string{"--nope"}, &stdout, &stderr)

	if n := strings.Count(stderr.String(), usage); n != 1 {
		t.Errorf("usage printed %d times on stderr, want 1", n)
	}
}
