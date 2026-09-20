package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sijiaoh/jevgrep/internal/cache"
)

// answering is a stub API that scores each line and, when tokens is not nil,
// says what it charged. Both shapes are real: the usage field is documented as
// optional, and what --stats reports depends on which one it got.
func answering(t *testing.T, tokens *int, score func(line string) float64) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State map[string]string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		answers := make(map[string]any, len(req.State))
		for id, line := range req.State {
			answers[id] = map[string]any{"type": "noul", "noul": score(line)}
		}
		body := map[string]any{"answers": answers}
		if tokens != nil {
			body["usage"] = map[string]any{"input_tokens": *tokens, "output_tokens": 0}
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}
}

func serving(t *testing.T, h http.HandlerFunc) string {
	t.Helper()

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

// statsServer charges inputTokens for every request, as the live API does.
func statsServer(t *testing.T, inputTokens int, score func(line string) float64) string {
	t.Helper()

	return serving(t, answering(t, &inputTokens, score))
}

// silentUsageServer answers without saying what it charged.
func silentUsageServer(t *testing.T, score func(line string) float64) string {
	t.Helper()

	return serving(t, answering(t, nil, score))
}

// tickingClock advances by d between the two times a run asks it, so that the
// elapsed line of a report is the same on every machine.
func tickingClock(d time.Duration) func() time.Time {
	start := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	var calls int
	return func() time.Time {
		calls++
		return start.Add(time.Duration(calls-1) * d)
	}
}

// The report of §3.3, word for word. --no-cache is what makes the cache line
// something a golden file can hold: the directory is a different path on every
// machine, and a test of its own covers it.
func TestStatsReport(t *testing.T) {
	inFixture(t)
	env := newEnv(t, statsServer(t, 4321, startsWithA))
	env.now = tickingClock(12400 * time.Millisecond)

	got := exec(t, env, "-n", "--no-cache", "--stats", "a", "f1.txt")

	if got.code != ExitMatch {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}
	// The results are untouched by having asked for a report about them.
	compareGolden(t, "lines", []byte(got.stdout))
	compareGolden(t, "stats", []byte(got.stderr))
}

// stdout is the result stream: a table in the middle of it would break every
// pipeline that parses jevgrep's output, which is what stderr is for.
func TestStatsStaysOutOfTheResults(t *testing.T) {
	inFixture(t)
	env := newEnv(t, statsServer(t, 100, startsWithA))

	got := exec(t, env, "--json", "--stats", "a", "f1.txt")

	for line := range strings.SplitSeq(strings.TrimSuffix(got.stdout, "\n"), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Errorf("stdout line %q is not a JSON record: %v", line, err)
		}
	}
	if !strings.Contains(got.stderr, "jevgrep: stats") {
		t.Errorf("stderr = %q, want the stats report", got.stderr)
	}
	// The table is for people. Machine-readable numbers are --dry-run --json.
	if strings.Contains(got.stderr, "{") {
		t.Errorf("stderr = %q, want a table rather than JSON", got.stderr)
	}
}

// -q and --stats is the run that wants one number and no lines: "is it in
// there, and what did that cost me".
func TestStatsSurvivesQuiet(t *testing.T) {
	inFixture(t)
	env := newEnv(t, statsServer(t, 100, startsWithA))

	got := exec(t, env, "-q", "--stats", "a", "f1.txt")

	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing at all", got.stdout)
	}
	if !strings.Contains(got.stderr, "jevgrep: stats") {
		t.Errorf("stderr = %q, want the stats report", got.stderr)
	}
	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d: the report does not change the answer", got.code, ExitMatch)
	}
}

// The API's own count is the bill. Ours is a guess, and says so with a tilde.
func TestStatsPrefersTheAPIsTokenCount(t *testing.T) {
	inFixture(t)

	reported := newEnv(t, statsServer(t, 4321, startsWithA))
	got := exec(t, reported, "--stats", "a", "f1.txt")
	if v := reportValue(t, got.stderr, "input tokens"); v != "4,321" {
		t.Errorf("input tokens = %s, want the 4,321 the API charged, unqualified:\n%s", v, got.stderr)
	}

	guessed := newEnv(t, silentUsageServer(t, startsWithA))
	got = exec(t, guessed, "--stats", "a", "f1.txt")
	if v := reportValue(t, got.stderr, "input tokens"); !strings.HasPrefix(v, "~") {
		t.Errorf("input tokens = %s, want an estimate when the API reported none:\n%s", v, got.stderr)
	}
}

// Retries are where the money and the afternoon go, so they are counted apart
// from the batches that caused them.
func TestStatsCountsRetriesApartFromBatches(t *testing.T) {
	inFixture(t)

	var calls atomic.Int64
	answer := answering(t, nil, startsWithA)
	url := serving(t, func(w http.ResponseWriter, r *http.Request) {
		// The first attempt fails, so the batch is sent twice and the report
		// has to tell the two apart.
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		answer(w, r)
	})

	env := newEnv(t, url)
	got := exec(t, env, "--stats", "a", "f1.txt")

	if v := reportValue(t, got.stderr, "requests"); v != "2" {
		t.Errorf("requests = %s, want 2: the attempt that failed was a request too:\n%s", v, got.stderr)
	}
	if !strings.Contains(got.stderr, "1 batch, 1 retried") {
		t.Errorf("stderr does not account for the retry:\n%s", got.stderr)
	}
	if v := reportValue(t, got.stderr, "failed"); v != "0" {
		t.Errorf("failed = %s, want 0: the batch succeeded in the end:\n%s", v, got.stderr)
	}
}

// The money is spent whether or not the run finished, and the moment a user
// most needs the number is the moment it did not.
func TestStatsIsPrintedWhenTheRunFailed(t *testing.T) {
	inFixture(t)
	env := newEnv(t, failingServer(t, http.StatusInternalServerError, "server_error"))

	got := exec(t, env, "--stats", "a", "f1.txt")

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	if !strings.Contains(got.stderr, "jevgrep: stats") {
		t.Errorf("stderr = %q, want the report all the same", got.stderr)
	}
	if v := reportValue(t, got.stderr, "failed"); v != "1" {
		t.Errorf("failed = %s, want the one batch that could not be scored:\n%s", v, got.stderr)
	}
}

// A rejected key ends the run on the spot, and the questions asked before it
// was rejected were still paid for.
func TestStatsIsPrintedWhenTheKeyWasRejected(t *testing.T) {
	inFixture(t)
	env := newEnv(t, failingServer(t, http.StatusUnauthorized, "unauthorized"))

	got := exec(t, env, "--stats", "a", "f1.txt")

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	if !strings.Contains(got.stderr, "jevgrep: stats") {
		t.Errorf("stderr = %q, want the report all the same", got.stderr)
	}
	// The report is written to be pasted into an issue, and an API that
	// answers with the line it was given must not get it echoed back there.
	for _, forbidden := range []string{"secret content", testKey} {
		if strings.Contains(got.stderr, forbidden) {
			t.Errorf("stderr quotes %q:\n%s", forbidden, got.stderr)
		}
	}
}

// The only place jevgrep itself says where the cache is, which is the whole of
// how someone clears it.
func TestStatsNamesTheCacheDirectory(t *testing.T) {
	inFixture(t)
	env := newEnv(t, statsServer(t, 100, startsWithA))

	got := exec(t, env, "--stats", "a", "f1.txt")

	dir, err := cache.Dir()
	if err != nil {
		t.Fatalf("cache.Dir: %v", err)
	}
	if !strings.Contains(got.stderr, dir) || !strings.Contains(got.stderr, "(delete it to clear)") {
		t.Errorf("stderr does not say where the cache is:\n%s", got.stderr)
	}
}

// A run that answered from the cache sent fewer questions, and the report is
// the one place that difference is visible at all.
func TestStatsCountsTheCacheHits(t *testing.T) {
	inFixture(t)
	env := newEnv(t, statsServer(t, 100, startsWithA))

	if got := exec(t, env, "a", "f1.txt"); got.code != ExitMatch {
		t.Fatalf("the first search exited %d (stderr: %q)", got.code, got.stderr)
	}
	got := exec(t, env, "--stats", "a", "f1.txt")

	questions := reportValue(t, got.stderr, "questions")
	if v := reportValue(t, got.stderr, "cached"); v != questions || questions == "0" {
		t.Errorf("cached = %s of %s questions, want all of them:\n%s", v, questions, got.stderr)
	}
	if v := reportValue(t, got.stderr, "requests"); v != "0" {
		t.Errorf("requests = %s, want 0: every answer was already on disk:\n%s", v, got.stderr)
	}
	if v := reportValue(t, got.stderr, "sent"); v != "0" {
		t.Errorf("sent = %s, want 0:\n%s", v, got.stderr)
	}
	// Nothing was sent, so nothing was spent: an exact zero, with no tilde to
	// suggest jevgrep is guessing about a run that made no requests.
	if v := reportValue(t, got.stderr, "input tokens"); v != "0" {
		t.Errorf("input tokens = %s, want an unqualified 0:\n%s", v, got.stderr)
	}
}

// --stats says nothing about a run that never happened: the dry run's own
// report already is one.
func TestStatsWithDryRunDoesNothing(t *testing.T) {
	inFixture(t)
	env := newEnv(t, silentServer(t))

	got := exec(t, env, "--dry-run", "--stats", "a", "f1.txt")

	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want nothing: the dry run report is on stdout", got.stderr)
	}
}

// A run without --stats counts nothing and prints nothing about itself.
func TestWithoutStatsNothingIsReported(t *testing.T) {
	inFixture(t)
	env := newEnv(t, statsServer(t, 100, startsWithA))

	got := exec(t, env, "a", "f1.txt")

	if strings.Contains(got.stderr, "stats") {
		t.Errorf("stderr = %q, want nothing about a report nobody asked for", got.stderr)
	}
}

func TestReportLayout(t *testing.T) {
	rows := []row{
		{label: "files", value: "12"},
		{label: "input tokens", value: "~152,000"},
		{label: "cost", value: "~$0.0064", note: "at $0.042 per 1M input tokens"},
	}

	want := "" +
		"  files               12\n" +
		"  input tokens  ~152,000\n" +
		"  cost          ~$0.0064  at $0.042 per 1M input tokens\n"
	if got := renderRows(rows); got != want {
		t.Errorf("renderRows =\n%q\nwant\n%q", got, want)
	}
}

func TestNumbersAreReadableAtAGlance(t *testing.T) {
	for n, want := range map[int]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 152000: "152,000", 1234567: "1,234,567"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %s, want %s", n, got, want)
		}
	}
}

// A price that rounds to $0.0000 reads as free, which is the one thing a price
// must never do.
func TestCheapRunsStillShowAPrice(t *testing.T) {
	tests := []struct {
		v    float64
		want string
	}{
		{v: 0, want: "$0"},
		{v: 0.0064, want: "$0.0064"},
		{v: 12.5, want: "$12.5000"},
		{v: 0.0000294, want: "$0.000029"},
		{v: 0.0000000042, want: "<$0.000001"},
	}
	for _, tt := range tests {
		if got := money(tt.v); got != tt.want {
			t.Errorf("money(%v) = %s, want %s", tt.v, got, tt.want)
		}
	}
}

// Ctrl-C is the run that most needs a bill: it stopped, and the questions it
// had already asked were paid for all the same.
func TestStatsIsPrintedAfterAnInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a process cannot send itself an interrupt on Windows")
	}
	inFixture(t)

	requested := make(chan struct{})
	released := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(requested) })
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))
	defer srv.Close()
	defer close(released)

	env := newEnv(t, srv.URL)

	var stderr safeBuffer
	codes := make(chan int, 1)
	go func() { codes <- run(env, []string{"--stats", "a", "f1.txt"}, io.Discard, &stderr) }()

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
	if !strings.Contains(stderr.String(), "jevgrep: stats") {
		t.Errorf("stderr = %q, want the report of what the interrupted run spent", stderr.String())
	}
}

// The numbers are a column, and a cheap price or a large count is exactly
// where a fixed width would break it. The cache directory is the one value
// that is not a figure: it may run past the column, and it must not drag the
// figures out to the width of a path.
func TestTheFiguresLineUpHoweverLongTheyGet(t *testing.T) {
	rows := []row{
		{label: "input tokens", value: "~123,456,789"},
		{label: "cost", value: "~$0.000014", note: "at $0.042 per 1M input tokens"},
		{label: "cache", value: "/home/you/.cache/jevgrep", note: "(delete it to clear)", wide: true},
	}

	want := "" +
		"  input tokens ~123,456,789\n" +
		"  cost           ~$0.000014  at $0.042 per 1M input tokens\n" +
		"  cache        /home/you/.cache/jevgrep  (delete it to clear)\n"
	if got := renderRows(rows); got != want {
		t.Errorf("renderRows =\n%s\nwant\n%s", got, want)
	}
}
