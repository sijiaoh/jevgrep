package cli

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// refusingServer fails the test if it is asked to score anything. It is how a
// test says "this run cost nothing" without trusting a counter to be read at
// the right moment.
func refusingServer(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the API was called; every line of this search was already cached")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestTheSameSearchTwiceCallsTheAPIOnce(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	path := writeFile(t, "app.log", logLines)

	first := exec(t, env, "-p", "a failure", path)
	if first.code != ExitMatch {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", first.code, ExitMatch, first.stderr)
	}

	again := env
	again.baseURL = refusingServer(t)
	second := exec(t, again, "-p", "a failure", path)

	if second.code != first.code {
		t.Errorf("exit code = %d, want %d", second.code, first.code)
	}
	// Byte for byte the same: a cached score is the score, not a note that
	// there was one.
	if second.stdout != first.stdout {
		t.Errorf("stdout = %q, want %q", second.stdout, first.stdout)
	}
	if second.stderr != "" {
		t.Errorf("stderr = %q, want it to be empty", second.stderr)
	}
}

func TestTighteningTheThresholdCostsNothing(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	path := writeFile(t, "app.log", logLines)

	if got := exec(t, env, "-t", "0.5", "a failure", path); got.code != ExitMatch {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}

	// The threshold is applied here, not by the model, so it is not part of
	// what a cached answer answers.
	again := env
	again.baseURL = refusingServer(t)
	got := exec(t, again, "-t", "0.95", "a failure", path)

	if got.code != ExitNoMatch {
		t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, ExitNoMatch, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing to reach 0.95", got.stdout)
	}
}

func TestNoCacheAsksAgain(t *testing.T) {
	var sent atomic.Int64
	env := newEnv(t, countingServer(t, &sent, mentionsError))
	path := writeFile(t, "app.log", logLines)

	exec(t, env, "a failure", path)
	first := sent.Load()
	if first == 0 {
		t.Fatal("the first run sent nothing")
	}

	exec(t, env, "--no-cache", "a failure", path)
	if got := sent.Load() - first; got != first {
		t.Errorf("the second run sent %d lines, want %d: --no-cache must not read the cache", got, first)
	}

	// It must not write one either, or the run after it would hit.
	exec(t, env, "--no-cache", "a failure", path)
	if got := sent.Load() - 2*first; got != first {
		t.Errorf("the third run sent %d lines, want %d: --no-cache must not write the cache", got, first)
	}
}

func TestAnotherModelIsAnotherSearch(t *testing.T) {
	var sent atomic.Int64
	env := newEnv(t, countingServer(t, &sent, mentionsError))
	path := writeFile(t, "app.log", logLines)

	exec(t, env, "a failure", path)
	first := sent.Load()

	exec(t, env, "--model", "jev-1", "a failure", path)
	if got := sent.Load() - first; got != first {
		t.Errorf("the second run sent %d lines, want %d: another model gives another score", got, first)
	}
}

func TestAnUnusableCacheDoesNotStopTheSearch(t *testing.T) {
	env := newEnv(t, scoringServer(t, mentionsError))
	path := writeFile(t, "app.log", logLines)
	// A cache directory that cannot be created at all.
	t.Setenv("XDG_CACHE_HOME", "relative/cache")

	got := exec(t, env, "a failure", path)

	if got.code != ExitMatch {
		t.Errorf("exit code = %d, want %d (stderr: %q)", got.code, ExitMatch, got.stderr)
	}
	if got.stdout != "ERROR disk full\n" {
		t.Errorf("stdout = %q, want the matching line", got.stdout)
	}
	// Nothing went wrong that the user can do anything about, and a warning on
	// every run would be noise.
	if got.stderr != "" {
		t.Errorf("stderr = %q, want it to be empty", got.stderr)
	}
}
