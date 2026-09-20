package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sijiaoh/jevgrep/internal/apikey"
	"github.com/sijiaoh/jevgrep/internal/jev"
)

// silentServer fails the test if it is ever asked anything. A dry run that
// reaches the network has already spent the money it was asked to estimate.
func silentServer(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("the API was called during a dry run")
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The three reports of §2.3, §2.4 and §2.5, word for word.
func TestDryRunReports(t *testing.T) {
	tests := []struct {
		golden string
		args   []string
		stdin  string
	}{
		{golden: "dry-run", args: []string{"--dry-run", "-r", "a", "."}},
		// A blank line is never sent, and a run that asks two things of a line
		// pays for two questions: both are what this report exists to show.
		{golden: "dry-run-two-meanings", args: []string{"--dry-run", "-e", "a", "--not", "b"}, stdin: "a1\n\nb\na2\n"},
		{golden: "dry-run-json", args: []string{"--dry-run", "--json", "-r", "a", "."}},
	}

	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			inFixture(t)
			env := newEnv(t, silentServer(t))
			env.stdin = strings.NewReader(tt.stdin)

			got := exec(t, env, tt.args...)

			if got.code != ExitMatch {
				t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
			}
			if got.stderr != "" {
				t.Errorf("stderr = %q, want it to be empty", got.stderr)
			}
			compareGolden(t, tt.golden, []byte(got.stdout))
		})
	}
}

// The first question anyone asks is what a search would cost, and plenty of
// them ask it before they have signed up. An estimate behind a key would be a
// price list behind the till.
func TestDryRunNeedsNoAPIKeyAndSendsNothing(t *testing.T) {
	inFixture(t)
	env := newEnv(t, silentServer(t))
	t.Setenv(apikey.EnvVar, "")

	got := exec(t, env, "--dry-run", "a", "f1.txt")

	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}
	if !strings.HasPrefix(got.stdout, "dry run: nothing was sent\n") {
		t.Errorf("stdout = %q, want the dry run report", got.stdout)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want no complaint about a missing key", got.stderr)
	}
}

// What is already on disk is not going to be paid for again, so it is not in
// the estimate either: that is the whole difference between a first search and
// a second one.
func TestDryRunTakesTheCacheOffTheEstimate(t *testing.T) {
	inFixture(t)
	env := newEnv(t, scoringServer(t, startsWithA))

	if got := exec(t, env, "a", "f1.txt"); got.code != ExitMatch {
		t.Fatalf("the first search exited %d (stderr: %q)", got.code, got.stderr)
	}

	// The same search again, this time only priced. The server is swapped for
	// one that must not be called: everything the estimate needs is local.
	env.baseURL = silentServer(t)
	got := exec(t, env, "--dry-run", "a", "f1.txt")

	cached := reportValue(t, got.stdout, "cached")
	questions := reportValue(t, got.stdout, "questions")
	if cached != questions || questions == "0" {
		t.Errorf("cached = %s of %s questions, want every one of them already scored:\n%s", cached, questions, got.stdout)
	}
	if send := reportValue(t, got.stdout, "to send"); send != "0" {
		t.Errorf("to send = %s, want 0:\n%s", send, got.stdout)
	}
	// Nothing left to send, so nothing left to estimate: an unqualified zero.
	if tokens := reportValue(t, got.stdout, "input tokens"); tokens != "0" {
		t.Errorf("input tokens = %s, want an unqualified 0:\n%s", tokens, got.stdout)
	}
}

func TestDryRunWithoutTheCacheQuotesTheFullPrice(t *testing.T) {
	inFixture(t)
	env := newEnv(t, scoringServer(t, startsWithA))

	if got := exec(t, env, "a", "f1.txt"); got.code != ExitMatch {
		t.Fatalf("the first search exited %d (stderr: %q)", got.code, got.stderr)
	}

	env.baseURL = silentServer(t)
	got := exec(t, env, "--dry-run", "--no-cache", "a", "f1.txt")

	if cached := reportValue(t, got.stdout, "cached"); cached != "0" {
		t.Errorf("cached = %s, want 0 under --no-cache:\n%s", cached, got.stdout)
	}
	if !strings.Contains(got.stdout, "--no-cache") {
		t.Errorf("the report does not say why nothing was cached:\n%s", got.stdout)
	}
}

// -q, -l, -L and -m stop a file at a match, and nobody knows where the matches
// are before sending anything. The report says so rather than quoting a figure
// it cannot stand behind.
func TestDryRunSaysWhenItCanOnlyBeAnUpperBound(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		bound bool
	}{
		{name: "the whole file is read", args: []string{"a", "f1.txt"}},
		{name: "-m stops at a match", args: []string{"-m1", "a", "f1.txt"}, bound: true},
		{name: "-l stops at a match", args: []string{"-l", "a", "f1.txt"}, bound: true},
		// -L looks like -l and is not: it has to read every file to the end to
		// know that nothing in it matched, so its estimate is exact.
		{name: "-L reads every file out", args: []string{"-L", "a", "f1.txt"}},
		// -m 0 sends nothing at all, which is the one limit that is exact.
		{name: "-m 0 is exact", args: []string{"-m0", "a", "f1.txt"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inFixture(t)
			env := newEnv(t, silentServer(t))

			got := exec(t, env, append([]string{"--dry-run"}, tt.args...)...)

			if bound := strings.Contains(got.stdout, "(upper bound)"); bound != tt.bound {
				t.Errorf("upper bound = %v, want %v:\n%s", bound, tt.bound, got.stdout)
			}
		})
	}
}

func TestDryRunOfMaxCountZeroEstimatesNothing(t *testing.T) {
	inFixture(t)
	env := newEnv(t, silentServer(t))

	got := exec(t, env, "--dry-run", "-m0", "a", "f1.txt")

	for _, label := range []string{"lines read", "questions", "to send"} {
		if v := reportValue(t, got.stdout, label); v != "0" {
			t.Errorf("%s = %s, want 0: -m 0 reads nothing and sends nothing:\n%s", label, v, got.stdout)
		}
	}
}

// -q says print nothing, and a report is something. The exit code is still 0:
// no line was judged, so "nothing matched" is not a claim this run can make.
func TestDryRunIsSilentUnderQuiet(t *testing.T) {
	inFixture(t)
	env := newEnv(t, silentServer(t))

	got := exec(t, env, "--dry-run", "-q", "zzz", "f2.txt")

	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing at all", got.stdout)
	}
	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d: a dry run never reports a match it did not look for", got.code, ExitMatch)
	}
}

// A path that cannot be read is still worth an estimate of everything else:
// the numbers already counted are the ones the user asked for.
func TestDryRunReportsWhatItCouldNotReadAndStillPrints(t *testing.T) {
	inFixture(t)
	env := newEnv(t, silentServer(t))

	got := exec(t, env, "--dry-run", "a", "f1.txt", "nowhere.txt")

	if got.code != ExitError {
		t.Errorf("exit code = %d, want %d", got.code, ExitError)
	}
	if !strings.Contains(got.stderr, "nowhere.txt") {
		t.Errorf("stderr = %q, want the path it could not read", got.stderr)
	}
	if !strings.Contains(got.stdout, "dry run: nothing was sent") {
		t.Errorf("stdout = %q, want the report all the same", got.stdout)
	}
}

// §6, as a test rather than as a promise: the two reports are counts of
// things, never the things themselves.
func TestTheReportsEchoNoLineAndNoKey(t *testing.T) {
	dir := t.TempDir()
	const secret = "password=hunter2-in-the-log"
	if err := os.WriteFile(filepath.Join(dir, "app.log"), []byte(secret+"\nERROR disk full\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	env := newEnv(t, scoringServer(t, mentionsError))

	for _, args := range [][]string{
		{"--dry-run", "-r", "a disk error", "."},
		{"--dry-run", "--json", "-r", "a disk error", "."},
		{"--stats", "-q", "-r", "a disk error", "."},
	} {
		got := exec(t, env, args...)
		printed := got.stdout + got.stderr
		for _, forbidden := range []string{secret, "hunter2", "ERROR disk full", testKey} {
			if strings.Contains(printed, forbidden) {
				t.Errorf("%v printed %q:\n%s", args, forbidden, printed)
			}
		}
	}
}

// The price is rendered from the one constant that holds it, so a report and
// the module can never quote two different rates.
func TestTheReportRendersThePriceItPricesWith(t *testing.T) {
	inFixture(t)
	env := newEnv(t, silentServer(t))

	got := exec(t, env, "--dry-run", "--json", "-r", "a", ".")

	var record struct {
		Tokens int     `json:"input_tokens"`
		Cost   float64 `json:"cost_usd"`
		Price  float64 `json:"price_usd_per_mtok"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &record); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, got.stdout)
	}
	if record.Price != jev.PriceUSDPerMTokInput {
		t.Errorf("price_usd_per_mtok = %v, want %v", record.Price, jev.PriceUSDPerMTokInput)
	}
	if record.Cost != jev.CostUSD(record.Tokens) {
		t.Errorf("cost_usd = %v for %d tokens, want %v", record.Cost, record.Tokens, jev.CostUSD(record.Tokens))
	}
}

// reportValue reads one row out of a report, so that a test can assert the
// number it cares about without pinning the whole layout.
func reportValue(t *testing.T, report, label string) string {
	t.Helper()

	for line := range strings.SplitSeq(report, "\n") {
		trimmed := strings.TrimPrefix(line, reportIndent)
		if !strings.HasPrefix(trimmed, label+" ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(trimmed, label))
		if len(fields) == 0 {
			t.Fatalf("row %q has no value", line)
		}
		return fields[0]
	}
	t.Fatalf("no %q row in the report:\n%s", label, report)
	return ""
}

// The estimate and the run have to agree about how the input is cut up, or the
// price quoted and the price paid come from two different ideas of a batch.
// They can only differ upwards: a run whose queue fills up sends a part-full
// batch, which is why the estimate is a lower bound and wears a tilde.
func TestTheEstimatedBatchesAreTheOnesTheRunMakes(t *testing.T) {
	dir := t.TempDir()
	var lines strings.Builder
	for i := range 100 {
		fmt.Fprintf(&lines, "a line number %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.log"), []byte(lines.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	env := newEnv(t, statsServer(t, 100, startsWithA))

	estimated := reportValue(t, exec(t, env, "--dry-run", "--no-cache", "a", "big.log").stdout, "batches")
	got := exec(t, env, "--stats", "--no-cache", "a", "big.log")

	if want := strings.TrimPrefix(estimated, "~") + " batches"; !strings.Contains(got.stderr, want) {
		t.Errorf("the run does not report %q:\n%s", want, got.stderr)
	}
}

// A dry run says nothing was sent, and it owes the same of the disk: someone
// pricing jevgrep before they have even signed up should not find a directory
// of ours in their home afterwards.
func TestDryRunLeavesNothingBehind(t *testing.T) {
	inFixture(t)
	env := newEnv(t, silentServer(t))
	home := os.Getenv("XDG_CACHE_HOME")

	if got := exec(t, env, "--dry-run", "-r", "a", "."); got.code != ExitMatch {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%s holds %v after a dry run", home, entries)
	}
}
