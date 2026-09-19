package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sijiaoh/jevgrep/internal/apikey"
	"github.com/sijiaoh/jevgrep/internal/buildinfo"
	"github.com/sijiaoh/jevgrep/internal/input"
)

// The key every test authenticates with. It is asserted never to appear in
// anything jevgrep prints, so it has to be recognizable.
const testKey = "sk-test-not-a-real-key"

// fakeTerminal answers the two questions --login and "is there input" ask,
// without a pseudo-terminal.
type fakeTerminal struct {
	stdinIsTTY, stderrIsTTY bool
	secret                  string
	err                     error
}

func (f fakeTerminal) IsTerminal(s apikey.Stream) bool {
	switch s {
	case apikey.Stdin:
		return f.stdinIsTTY
	case apikey.Stderr:
		return f.stderrIsTTY
	}
	return false
}

func (f fakeTerminal) ReadSecret() (string, error) { return f.secret, f.err }

// scoringServer stands in for the API, scoring each line with score. Tests
// never reach the network, and never read a developer's own key: every test
// goes through newEnv, which isolates both environment variables.
func scoringServer(t *testing.T, score func(line string) float64) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State map[string]string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		answers := make(map[string]any, len(req.State))
		for id, line := range req.State {
			answers[id] = map[string]any{"type": "noul", "noul": score(line)}
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"answers": answers,
			"usage":   map[string]any{"input_tokens": 1, "output_tokens": 0},
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// failingServer answers every request with status and body.
func failingServer(t *testing.T, status int, errorType string) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"detail":{"error_type":%q,"message":"the line was: secret content"}}`, errorType)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// mentionsError scores a line by whether it looks like an error, which is the
// search every test makes.
func mentionsError(line string) float64 {
	if strings.Contains(line, "ERROR") {
		return 0.9
	}
	return 0.1
}

// newEnv isolates the key and the config directory so that no test can pick up
// the key of whoever is running it, or write to their config.
func newEnv(t *testing.T, baseURL string) environment {
	t.Helper()

	t.Setenv(apikey.EnvVar, testKey)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return environment{
		stdin:    strings.NewReader(""),
		terminal: fakeTerminal{},
		baseURL:  baseURL,
	}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const logLines = "all good\nERROR disk full\nstill fine\n"

type result struct {
	code           int
	stdout, stderr string
}

func exec(t *testing.T, env environment, args ...string) result {
	t.Helper()

	var stdout, stderr strings.Builder
	code := run(env, args, &stdout, &stderr)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestSearchingAFilePrintsTheMatchingLines(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	path := writeFile(t, "app.log", logLines)

	got := exec(t, env, "a failure", path)

	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}
	// One PATH operand, so no file name, and the line comes out whole.
	if got.stdout != "ERROR disk full\n" {
		t.Errorf("stdout = %q, want the matching line only", got.stdout)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want it to be empty", got.stderr)
	}
}

func TestSearchingSeveralFilesNamesThemAndNumbersTheLines(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	first := writeFile(t, "a.log", logLines)
	second := writeFile(t, "b.log", "ERROR again\n")

	got := exec(t, env, "-n", "a failure", first, second)

	want := fmt.Sprintf("%s:2:ERROR disk full\n%s:1:ERROR again\n", first, second)
	if got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d", got.code, ExitMatch)
	}
}

func TestPipedInputIsSearchedWithoutAPath(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	env.stdin = strings.NewReader(logLines)

	got := exec(t, env, "-H", "a failure")

	// -H overrides the default even with no operand at all.
	want := input.StdinName + ":ERROR disk full\n"
	if got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
}

func TestNoFilenameWinsOverSeveralOperands(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	first := writeFile(t, "a.log", "ERROR one\n")
	second := writeFile(t, "b.log", "ERROR two\n")

	got := exec(t, env, "-h", "a failure", first, second)

	if got.stdout != "ERROR one\nERROR two\n" {
		t.Errorf("stdout = %q, want the lines without their file names", got.stdout)
	}
}

func TestNothingMatchingExitsOne(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	path := writeFile(t, "app.log", "all good\nstill fine\n")

	got := exec(t, env, "a failure", path)

	if got.code != ExitNoMatch {
		t.Errorf("exit code = %d, want %d", got.code, ExitNoMatch)
	}
	if got.stdout != "" || got.stderr != "" {
		t.Errorf("stdout = %q, stderr = %q, want both empty", got.stdout, got.stderr)
	}
}

func TestInvertedMatchSelectsTheRest(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	path := writeFile(t, "app.log", logLines)

	got := exec(t, env, "-v", "a failure", path)

	if got.stdout != "all good\nstill fine\n" {
		t.Errorf("stdout = %q, want the lines that do not match", got.stdout)
	}
}

func TestAnUnreadableFileIsReportedAndTheRestIsSearched(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	good := writeFile(t, "a.log", logLines)
	missing := filepath.Join(t.TempDir(), "gone.log")

	got := exec(t, env, "a failure", missing, good)

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d: an error outranks the match", got.code, ExitError)
	}
	if !strings.Contains(got.stdout, "ERROR disk full") {
		t.Errorf("stdout = %q, want the other file to have been searched", got.stdout)
	}
	want := "jevgrep: " + missing + ": no such file or directory\n"
	if got.stderr != want {
		t.Errorf("stderr = %q, want %q", got.stderr, want)
	}
}

func TestADirectoryIsReportedByName(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	dir := t.TempDir()

	got := exec(t, env, "a failure", dir)

	// The path appears once: the system's message for it is used without its
	// own copy of the path. The half sentence after it is jevgrep's own: the
	// user who pointed it at a directory wanted -r.
	want := "jevgrep: " + dir + ": is a directory (use -r to search it)\n"
	if got.stderr != want {
		t.Errorf("stderr = %q, want %q", got.stderr, want)
	}
	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
}

func TestABatchThatCannotBeScoredIsReportedAndTheRunEndsInError(t *testing.T) {
	env := newEnv(t, failingServer(t, http.StatusServiceUnavailable, "api_error"))
	path := writeFile(t, "app.log", logLines)

	got := exec(t, env, "a failure", path)

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	want := fmt.Sprintf("jevgrep: %s: lines 1-3: could not be scored: HTTP 503 api_error after 4 attempts\n", path)
	if got.stderr != want {
		t.Errorf("stderr = %q, want %q", got.stderr, want)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want unscored lines not to be printed", got.stdout)
	}
}

func TestARequestTheServerRefusesStopsTheRun(t *testing.T) {
	env := newEnv(t, failingServer(t, http.StatusBadRequest, "invalid_request_error"))
	path := writeFile(t, "app.log", logLines)

	got := exec(t, env, "--model", "jev-latst", "a failure", path)

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	want := "jevgrep: the API rejected the request: HTTP 400 invalid_request_error\n"
	if got.stderr != want {
		t.Errorf("stderr = %q, want %q", got.stderr, want)
	}
}

func TestARejectedKeyStopsTheRun(t *testing.T) {
	env := newEnv(t, failingServer(t, http.StatusUnauthorized, "authentication_error"))
	path := writeFile(t, "app.log", logLines)

	got := exec(t, env, "a failure", path)

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	want := "jevgrep: the API key was rejected (HTTP 401); run `jevgrep --login` to store a new one\n"
	if got.stderr != want {
		t.Errorf("stderr = %q, want %q", got.stderr, want)
	}
}

func TestAKeyWithoutAccessIsReportedAsSuch(t *testing.T) {
	env := newEnv(t, failingServer(t, http.StatusForbidden, "permission_error"))
	path := writeFile(t, "app.log", logLines)

	got := exec(t, env, "a failure", path)

	want := "jevgrep: that key is not allowed to use this API (HTTP 403)\n"
	if got.stderr != want {
		t.Errorf("stderr = %q, want %q", got.stderr, want)
	}
}

// Nothing jevgrep prints may contain a searched line or the key: the server's
// error body here carries both shapes of leak.
func TestFailuresLeakNeitherTheLinesNorTheKey(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			env := newEnv(t, failingServer(t, status, "some_error"))
			path := writeFile(t, "app.log", "ERROR disk full\n")

			got := exec(t, env, "a failure", path)

			for _, leak := range []string{testKey, "disk full", "secret content"} {
				if strings.Contains(got.stderr+got.stdout, leak) {
					t.Errorf("output mentions %q:\n%s%s", leak, got.stdout, got.stderr)
				}
			}
		})
	}
}

func TestWithoutAKeyTheUserIsToldWhereToGetOne(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	t.Setenv(apikey.EnvVar, "")
	path := writeFile(t, "app.log", logLines)

	got := exec(t, env, "a failure", path)

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	want := "jevgrep: no API key found\n" +
		"  set " + apikey.EnvVar + ", or run `jevgrep --login` to store one\n" +
		"  get a key at " + apikey.SignupURL + "\n"
	if got.stderr != want {
		t.Errorf("stderr = %q, want %q", got.stderr, want)
	}
}

// A key file that exists but cannot be read is not a missing key, and must not
// send the user off to overwrite it.
func TestAnUnreadableKeyFileIsReportedAsAFileError(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	t.Setenv(apikey.EnvVar, "")
	keyPath, err := apikey.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(keyPath, 0o700); err != nil {
		t.Fatal(err)
	}

	got := exec(t, env, "a failure", writeFile(t, "app.log", logLines))

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	if !strings.HasPrefix(got.stderr, "jevgrep: "+keyPath+": ") {
		t.Errorf("stderr = %q, want it to report the key file by path", got.stderr)
	}
	if strings.Contains(got.stderr, "--login") {
		t.Errorf("stderr = %q, want no suggestion to write the file that is already there", got.stderr)
	}
}

func TestVersionAndHelpGoToStdout(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "long version", args: []string{"--version"}, want: "jevgrep " + buildinfo.Version() + "\n"},
		{name: "short version", args: []string{"-V"}, want: "jevgrep " + buildinfo.Version() + "\n"},
		{name: "help", args: []string{"--help"}, want: help()},
		// Asking for help on a command line that is wrong answers the question
		// that was asked.
		{name: "help outranks a bad option", args: []string{"--help", "-Q"}, want: help()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnv(t, "")

			got := exec(t, env, tt.args...)

			if got.code != ExitMatch {
				t.Errorf("exit code = %d, want %d", got.code, ExitMatch)
			}
			if got.stdout != tt.want {
				t.Errorf("stdout = %q, want %q", got.stdout, tt.want)
			}
			if got.stderr != "" {
				t.Errorf("stderr = %q, want it to be empty", got.stderr)
			}
		})
	}
}

// A single dash never introduces a long option: under grep's rules -version is
// -v together with an -e whose argument is "rsion", and a script that relied on
// it printing the version would have silently started searching instead.
func TestASingleDashIsNeverALongOption(t *testing.T) {
	env := newEnv(t, scoringServer(t, func(line string) float64 {
		if strings.Contains(line, "rsion") {
			return 0.9
		}
		return 0.1
	}))
	env.stdin = strings.NewReader("about rsion\nabout nothing\n")

	got := exec(t, env, "-version")

	// -v inverts, so the line that means "rsion" is the one left out.
	if got.stdout != "about nothing\n" {
		t.Errorf("stdout = %q, want -version to have been read as -v -e rsion", got.stdout)
	}
	if strings.Contains(got.stdout, buildinfo.Version()) {
		t.Errorf("stdout = %q, want no version", got.stdout)
	}
}

// -h is --no-filename, as it is in grep. Help is only --help.
func TestShortHIsNotHelp(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	env.stdin = strings.NewReader("")

	got := exec(t, env, "-h", "a failure")

	if got.stdout != "" {
		t.Errorf("stdout = %q, want no help page", got.stdout)
	}
	if got.code != ExitNoMatch {
		t.Errorf("exit code = %d, want %d", got.code, ExitNoMatch)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no arguments at all", args: nil, want: "jevgrep: missing MEANING"},
		{name: "unknown long option", args: []string{"--nope", "x"}, want: "jevgrep: unknown option: --nope"},
		{name: "unknown short option", args: []string{"-Q", "x"}, want: "jevgrep: unknown option: -Q"},
		{name: "missing argument", args: []string{"--model"}, want: "jevgrep: option --model needs an argument"},
		{name: "missing argument in a cluster", args: []string{"-nt"}, want: "jevgrep: option --threshold needs an argument"},
		{name: "argument where none is taken", args: []string{"--login=yes"}, want: "jevgrep: option --login takes no argument"},
		{name: "and without a meaning", args: []string{"--and", "x", "-e", "y"}, want: "jevgrep: --and must follow a meaning"},
		{name: "not without a meaning", args: []string{"--not", "x", "-e", "y"}, want: "jevgrep: --not must follow a meaning"},
		{name: "threshold out of range", args: []string{"-t", "1.5", "x"}, want: `jevgrep: --threshold: not a number between 0 and 1: "1.5"`},
		{name: "threshold not a number", args: []string{"--threshold=high", "x"}, want: `jevgrep: --threshold: not a number between 0 and 1: "high"`},
		// The option is named by its long form whichever form was typed.
		{name: "short threshold is named long", args: []string{"-t2", "x"}, want: `jevgrep: --threshold: not a number between 0 and 1: "2"`},
		{name: "empty meaning", args: []string{"-e", "", "file"}, want: "jevgrep: empty MEANING"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnv(t, "")

			got := exec(t, env, tt.args...)

			if got.code != ExitError {
				t.Errorf("exit code = %d, want %d", got.code, ExitError)
			}
			if got.stdout != "" {
				t.Errorf("stdout = %q, want it to be empty", got.stdout)
			}
			want := tt.want + "\n" + usage + "\nTry 'jevgrep --help' for more information.\n"
			if got.stderr != want {
				t.Errorf("stderr = %q, want %q", got.stderr, want)
			}
		})
	}
}

// The usage line is the error path's only output, so an error must never print
// it more than once.
func TestUsagePrintedOnce(t *testing.T) {
	env := newEnv(t, "")

	got := exec(t, env, "--nope")

	if n := strings.Count(got.stderr, usage); n != 1 {
		t.Errorf("usage printed %d times on stderr, want 1", n)
	}
}

// grep would wait for the user to type. Every line jevgrep reads is billed, so
// a prompt that looks like a hang is a bill nobody meant to start.
func TestATerminalWithNoPathIsAnError(t *testing.T) {
	env := newEnv(t, "")
	env.terminal = fakeTerminal{stdinIsTTY: true}

	got := exec(t, env, "a failure")

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	if !strings.Contains(got.stderr, "jevgrep: no input; give a PATH or pipe data in") {
		t.Errorf("stderr = %q, want it to say there is no input", got.stderr)
	}
}

func TestEverythingAfterADoubleDashIsAPath(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	path := writeFile(t, "-v", logLines)

	got := exec(t, env, "-e", "a failure", "--", path)

	if got.stdout != "ERROR disk full\n" {
		t.Errorf("stdout = %q, want the file named like an option to have been read", got.stdout)
	}
}

// A first operand is the MEANING only when no -e was given, wherever the -e
// appears.
func TestALaterMeaningOptionMakesTheFirstOperandAPath(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	path := writeFile(t, "app.log", logLines)

	got := exec(t, env, path, "-e", "a failure")

	if got.stdout != "ERROR disk full\n" {
		t.Errorf("stdout = %q, want the operand to have been read as a path", got.stdout)
	}
}

func TestOutputThatCannotBeWrittenIsReported(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	path := writeFile(t, "app.log", logLines)

	var stderr strings.Builder
	code := run(env, []string{"a failure", path}, brokenWriter{}, &stderr)

	if code != ExitError {
		t.Errorf("exit code = %d, want %d: a match that was never printed is not a success", code, ExitError)
	}
	if !strings.Contains(stderr.String(), "could not write the output") {
		t.Errorf("stderr = %q, want it to report the write failure", stderr.String())
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestLogin(t *testing.T) {
	tests := []struct {
		name     string
		terminal fakeTerminal
		status   int
		wantCode int
		want     string
	}{
		{
			name:     "a verified key is stored",
			terminal: fakeTerminal{stdinIsTTY: true, stderrIsTTY: true, secret: testKey},
			wantCode: ExitMatch,
			want:     "jevgrep: key saved to ",
		},
		{
			name:     "without a terminal nothing is read",
			terminal: fakeTerminal{secret: testKey},
			wantCode: ExitError,
			want:     "jevgrep: --login needs a terminal; set " + apikey.EnvVar + " instead\n",
		},
		{
			name:     "an empty line backs out",
			terminal: fakeTerminal{stdinIsTTY: true, stderrIsTTY: true, secret: "   "},
			wantCode: ExitError,
			want:     "jevgrep: no key entered\n",
		},
		{
			name:     "a rejected key is not stored",
			terminal: fakeTerminal{stdinIsTTY: true, stderrIsTTY: true, secret: testKey},
			status:   http.StatusUnauthorized,
			wantCode: ExitError,
			want:     "jevgrep: that key was rejected; nothing was saved\n",
		},
		{
			name:     "a key without access says so",
			terminal: fakeTerminal{stdinIsTTY: true, stderrIsTTY: true, secret: testKey},
			status:   http.StatusForbidden,
			wantCode: ExitError,
			want:     "jevgrep: that key is not allowed to use this API (HTTP 403); nothing was saved\n",
		},
		{
			name:     "an unreachable API is not a bad key",
			terminal: fakeTerminal{stdinIsTTY: true, stderrIsTTY: true, secret: testKey},
			status:   http.StatusInternalServerError,
			wantCode: ExitError,
			want:     "jevgrep: could not reach the API: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL := scoringServer(t, func(string) float64 { return 1 })
			if tt.status != 0 {
				baseURL = failingServer(t, tt.status, "some_error")
			}
			env := newEnv(t, baseURL)
			env.terminal = tt.terminal

			got := exec(t, env, "--login")

			if got.code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", got.code, tt.wantCode)
			}
			if !strings.Contains(got.stderr, tt.want) {
				t.Errorf("stderr = %q, want it to contain %q", got.stderr, tt.want)
			}
			// --login exists so a key can be set up without disturbing what
			// jevgrep's output is piped into.
			if got.stdout != "" {
				t.Errorf("stdout = %q, want it to be empty", got.stdout)
			}
			if strings.Contains(got.stderr, testKey) {
				t.Errorf("stderr = %q, want it never to echo the key", got.stderr)
			}
		})
	}
}

func TestLoginStoresTheKeyWhereTheNextRunFindsIt(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	env.terminal = fakeTerminal{stdinIsTTY: true, stderrIsTTY: true, secret: "sk-freshly-typed"}

	if got := exec(t, env, "--login"); got.code != ExitMatch {
		t.Fatalf("--login exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}
	env.terminal = fakeTerminal{}

	// The environment variable is out of the way, so the search can only be
	// authenticated by the file --login just wrote.
	t.Setenv(apikey.EnvVar, "")
	env.stdin = strings.NewReader(logLines)
	if got := exec(t, env, "a failure"); got.stdout != "ERROR disk full\n" {
		t.Errorf("stdout = %q, want the stored key to have been used", got.stdout)
	}
}

// The expression is built here and evaluated in search; this is the wiring
// between the two, which is the only part of it the command line can get wrong.
func TestExpressions(t *testing.T) {
	const lines = "warm morning\ncold morning\ncold evening\n"
	scores := map[string]map[string]float64{
		"cold":    {"warm morning": 0.1, "cold morning": 0.9, "cold evening": 0.9},
		"morning": {"warm morning": 0.9, "cold morning": 0.9, "cold evening": 0.1},
		"warm":    {"warm morning": 0.9, "cold morning": 0.1, "cold evening": 0.1},
		"evening": {"warm morning": 0.1, "cold morning": 0.1, "cold evening": 0.9},
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "one meaning", args: []string{"-e", "cold"}, want: "cold morning\ncold evening\n"},
		{name: "meanings are ored", args: []string{"-e", "warm", "-e", "evening"}, want: "warm morning\ncold evening\n"},
		{name: "and narrows a meaning", args: []string{"-e", "cold", "--and", "morning"}, want: "cold morning\n"},
		{name: "not excludes", args: []string{"-e", "cold", "--not", "morning"}, want: "cold evening\n"},
		// The threshold is what "means it" is measured against, so 0.95 means
		// nothing here is sure enough.
		{name: "threshold raised", args: []string{"-t", "0.95", "-e", "cold"}, want: ""},
		{name: "threshold in a cluster", args: []string{"-nt0.95", "-e", "cold"}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnv(t, scoringServer(t, func(string) float64 { return 0 }))
			env.baseURL = meaningServer(t, scores)
			env.stdin = strings.NewReader(lines)

			got := exec(t, env, tt.args...)

			if got.stdout != tt.want {
				t.Errorf("stdout = %q, want %q (stderr: %q)", got.stdout, tt.want, got.stderr)
			}
		})
	}
}

// meaningServer scores by the meaning the question asks about, which is the
// only way a test can tell the requests of a multi-meaning search apart.
func meaningServer(t *testing.T, scores map[string]map[string]float64) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State     map[string]string `json:"state"`
			Questions map[string]struct {
				Instructions string `json:"instructions"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("request body: %v", err)
			return
		}
		answers := make(map[string]any, len(req.State))
		for id, line := range req.State {
			_, meaning, _ := strings.Cut(req.Questions[id].Instructions, ": ")
			byLine, ok := scores[meaning]
			if !ok {
				t.Errorf("no scores for meaning %q", meaning)
			}
			answers[id] = map[string]any{"type": "noul", "noul": byLine[line]}
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"answers": answers,
			"usage":   map[string]any{"input_tokens": 1, "output_tokens": 0},
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Ctrl-C ends the run at 130 rather than at a match count that was never
// finished.
func TestAnInterruptEndsTheRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a process cannot send itself an interrupt on Windows")
	}

	requested := make(chan struct{})
	released := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(requested) })
		// Held until the interrupt tears the request down, so the run is
		// certainly in the middle of scoring when the signal arrives.
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))
	defer srv.Close()
	// Closed before the server is, so that a run that did not stop cannot
	// leave the test waiting on its own handler.
	defer close(released)

	env := newEnv(t, srv.URL)
	path := writeFile(t, "app.log", logLines)

	// The reader goroutine can outlive the run it was interrupted in, and it
	// reports read errors from there, so what it writes to is locked.
	var stderr safeBuffer
	codes := make(chan int, 1)
	go func() { codes <- run(env, []string{"a failure", path}, io.Discard, &stderr) }()

	<-requested
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := self.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-codes:
		if code != ExitInterrupt {
			t.Errorf("exit code = %d, want %d", code, ExitInterrupt)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("jevgrep did not stop after the interrupt")
	}
	// The batches the interrupt tore down are not news: the user knows what
	// they pressed.
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want it to be empty", stderr.String())
	}
}

type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Run is the wiring to the real process. The rest of the tests go through the
// environment underneath it, so this is what says the two still meet.
func TestRunWritesToTheGivenWriters(t *testing.T) {
	var stdout, stderr strings.Builder

	code := Run([]string{"--version"}, &stdout, &stderr)

	if code != ExitMatch {
		t.Errorf("exit code = %d, want %d", code, ExitMatch)
	}
	if stdout.String() != "jevgrep "+buildinfo.Version()+"\n" {
		t.Errorf("stdout = %q, want the version", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want it to be empty", stderr.String())
	}
}

// Which refusals stop the whole run: the ones that would come back for every
// remaining batch. jev has already decided whether the request was worth
// resending by the time one reaches us, which is a different question.
func TestOnlyRefusalsThatWouldRepeatStopTheRun(t *testing.T) {
	fatal := map[int]bool{
		http.StatusBadRequest: true, http.StatusNotFound: true, http.StatusUnprocessableEntity: true,
		http.StatusRequestTimeout: false, http.StatusTooManyRequests: false,
		http.StatusInternalServerError: false, http.StatusServiceUnavailable: false, 529: false,
	}
	for status, want := range fatal {
		if got := isClientError(status); got != want {
			t.Errorf("isClientError(%d) = %v, want %v", status, got, want)
		}
	}
}

// A key that works and a disk that does not is its own failure: the wording
// must not send the user off to check a network that is fine.
func TestLoginSaysSoWhenTheKeyCannotBeStored(t *testing.T) {
	env := newEnv(t, scoringServer(t, func(string) float64 { return 1 }))
	env.terminal = fakeTerminal{stdinIsTTY: true, stderrIsTTY: true, secret: testKey}
	// A file where the config directory should be: the key verifies, and then
	// there is nowhere to put it.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", blocked)

	got := exec(t, env, "--login")

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	if !strings.Contains(got.stderr, "jevgrep: the key works, but it could not be stored: ") {
		t.Errorf("stderr = %q, want it to blame the storing", got.stderr)
	}
	if strings.Contains(got.stderr, "could not reach the API") {
		t.Errorf("stderr = %q, want no talk of the API, which answered", got.stderr)
	}
}

// Input that stops mid-stream is reported, and what was read before it is
// still searched.
func TestInputThatFailsWhileBeingReadIsReported(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	env.stdin = io.MultiReader(strings.NewReader("ERROR disk full\n"), brokenReader{})

	got := exec(t, env, "a failure")

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	if got.stdout != "ERROR disk full\n" {
		t.Errorf("stdout = %q, want what was read before the failure", got.stdout)
	}
	want := "jevgrep: " + input.StdinName + ": read error: "
	if !strings.HasPrefix(got.stderr, want) {
		t.Errorf("stderr = %q, want it to start with %q", got.stderr, want)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("input/output error") }
