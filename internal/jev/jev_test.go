package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "test-key"

// newTestClient wires a Client to a stub server and replaces the backoff sleep
// with a recorder, so retry tests assert the schedule instead of waiting it out.
func newTestClient(t *testing.T, cfg Config, handler http.HandlerFunc) (*Client, *waits) {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg.APIKey = testKey
	cfg.BaseURL = srv.URL
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := &waits{}
	client.sleep = w.record

	return client, w
}

type waits struct {
	mu sync.Mutex
	d  []time.Duration
}

func (w *waits) record(ctx context.Context, d time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.d = append(w.d, d)
	return ctx.Err()
}

func (w *waits) all() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.d
}

// answerWith replies to a decoded request with one probability per question,
// so a handler never has to know how the ids are built.
func answerWith(t *testing.T, w http.ResponseWriter, req request, score func(line string) float64) {
	t.Helper()

	answers := make(map[string]any, len(req.Questions))
	for id := range req.Questions {
		answers[id] = map[string]any{"type": noulType, "noul": score(req.State[id])}
	}
	writeJSON(t, w, map[string]any{
		"model":   req.Model,
		"answers": answers,
	})
}

// half answers every line with 0.5, for tests that care about something other
// than the probabilities.
func half(string) float64 { return 0.5 }

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestScore(t *testing.T) {
	lines := []string{"disk is full", "user logged in", "ディスクの空き容量が不足しています"}
	want := map[string]float64{lines[0]: 0.91, lines[1]: 0.03, lines[2]: 0.88}

	var gotAuth, gotAgent string
	client, _ := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAgent = r.Header.Get("User-Agent")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != scorePath {
			t.Errorf("path = %s, want %s", r.URL.Path, scorePath)
		}
		req, err := readRequest(t, r)
		if err != nil {
			return
		}
		answerWith(t, w, req, func(line string) float64 { return want[line] })
	})

	got, err := client.Score(t.Context(), "a problem with storage", lines)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	for i, line := range lines {
		if got[i] != want[line] {
			t.Errorf("score for line %d = %v, want %v", i, got[i], want[line])
		}
	}
	if gotAuth != "Bearer "+testKey {
		t.Errorf("Authorization = %q, want the bearer key", gotAuth)
	}
	if !strings.HasPrefix(gotAgent, "jevgrep/") {
		t.Errorf("User-Agent = %q, want it to identify jevgrep", gotAgent)
	}
}

// The answers come back in a map, so order can only come from the ids.
func TestScoreKeepsLineOrder(t *testing.T) {
	lines := []string{"zero", "one", "two", "three", "four"}

	client, _ := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		req, err := readRequest(t, r)
		if err != nil {
			return
		}
		answerWith(t, w, req, func(line string) float64 {
			return map[string]float64{"zero": 0, "one": 0.25, "two": 0.5, "three": 0.75, "four": 1}[line]
		})
	})

	got, err := client.Score(t.Context(), "counting", lines)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	want := []float64{0, 0.25, 0.5, 0.75, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scores = %v, want %v", got, want)
		}
	}
}

func TestScoreSendsTheConfiguredModel(t *testing.T) {
	for _, tt := range []struct{ configured, want string }{
		{configured: "", want: DefaultModel},
		{configured: "jev-1.13.0", want: "jev-1.13.0"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			var got string
			client, _ := newTestClient(t, Config{Model: tt.configured}, func(w http.ResponseWriter, r *http.Request) {
				body, _ := readRequest(t, r)
				got = body.Model
				answerWith(t, w, body, half)
			})

			if _, err := client.Score(t.Context(), "anything", []string{"a line"}); err != nil {
				t.Fatalf("Score: %v", err)
			}
			if got != tt.want {
				t.Errorf("model = %q, want %q", got, tt.want)
			}
		})
	}
}

// Every line must reach the server verbatim and carry the meaning as a yes/no
// question; that pairing is the whole contract with the model.
func TestScoreAsksOneNoulQuestionPerLine(t *testing.T) {
	lines := []string{"first", "second"}
	const meaning = "a greeting"

	var got request
	client, _ := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readRequest(t, r)
		got = body
		answerWith(t, w, body, half)
	})

	if _, err := client.Score(t.Context(), meaning, lines); err != nil {
		t.Fatalf("Score: %v", err)
	}

	if len(got.State) != len(lines) || len(got.Questions) != len(lines) {
		t.Fatalf("got %d state entries and %d questions, want %d of each", len(got.State), len(got.Questions), len(lines))
	}
	for i, line := range lines {
		id := lineID(i + 1)
		if got.State[id] != line {
			t.Errorf("state[%s] = %q, want %q", id, got.State[id], line)
		}
		q := got.Questions[id]
		if q.Type != noulType {
			t.Errorf("questions[%s].type = %q, want %q", id, q.Type, noulType)
		}
		if !strings.Contains(q.Instructions, meaning) || !strings.Contains(q.Instructions, id) {
			t.Errorf("questions[%s].instructions = %q, want it to name %s and the meaning", id, q.Instructions, id)
		}
	}
}

func TestScoreWithoutLinesSendsNothing(t *testing.T) {
	var requests int
	client, _ := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "unexpected", http.StatusInternalServerError)
	})

	got, err := client.Score(t.Context(), "anything", nil)
	if err != nil || got != nil {
		t.Fatalf("Score(nil) = %v, %v; want nil, nil", got, err)
	}
	if requests != 0 {
		t.Errorf("sent %d requests for no lines, want 0", requests)
	}
}

func TestScoreRetriesWithGrowingBackoff(t *testing.T) {
	var calls int
	client, waits := newTestClient(t, Config{RetryBackoff: time.Second}, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		body, _ := readRequest(t, r)
		answerWith(t, w, body, half)
	})

	if _, err := client.Score(t.Context(), "anything", []string{"a"}); err != nil {
		t.Fatalf("Score: %v", err)
	}

	if calls != 3 {
		t.Errorf("server saw %d requests, want 3", calls)
	}
	got := waits.all()
	if len(got) != 2 {
		t.Fatalf("waited %d times, want 2", len(got))
	}
	// Equal jitter: each wait is within [d/2, d] of the doubling schedule.
	for i, d := range got {
		full := time.Second << i
		if d < full/2 || d > full {
			t.Errorf("wait %d = %v, want it in [%v, %v]", i, d, full/2, full)
		}
	}
	if got[1] <= got[0] {
		t.Errorf("waits = %v, want the second to be longer", got)
	}
}

func TestScoreObeysRetryAfter(t *testing.T) {
	var calls int
	client, waits := newTestClient(t, Config{RetryBackoff: time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "7")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		body, _ := readRequest(t, r)
		answerWith(t, w, body, half)
	})

	if _, err := client.Score(t.Context(), "anything", []string{"a"}); err != nil {
		t.Fatalf("Score: %v", err)
	}

	if got := waits.all(); len(got) != 1 || got[0] != 7*time.Second {
		t.Errorf("waits = %v, want [7s]", got)
	}
}

func TestScoreGivesUpAfterMaxAttempts(t *testing.T) {
	var calls int
	client, waits := newTestClient(t, Config{MaxAttempts: 3, RetryBackoff: time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	_, err := client.Score(t.Context(), "anything", []string{"a"})

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Score error = %v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError || apiErr.Attempts != 3 {
		t.Errorf("got %+v, want status 500 after 3 attempts", apiErr)
	}
	if calls != 3 {
		t.Errorf("server saw %d requests, want 3", calls)
	}
	if got := len(waits.all()); got != 2 {
		t.Errorf("waited %d times, want 2 (no wait after the last attempt)", got)
	}
	// A batch failure must not look like an auth failure, or the CLI would
	// abort the whole run instead of skipping this batch.
	var authErr *AuthError
	if errors.As(err, &authErr) {
		t.Error("a server error was reported as an authentication failure")
	}
}

func TestScoreDoesNotRetryAuthFailure(t *testing.T) {
	var calls int
	client, waits := newTestClient(t, Config{RetryBackoff: time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-TypeSafe-Request-Id", "req_123")
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(t, w, map[string]any{"detail": map[string]any{
			"error_type": "authentication_error",
			"message":    "Cannot authenticate with the server.",
		}})
	})

	_, err := client.Score(t.Context(), "anything", []string{"a secret line"})

	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("Score error = %v, want an *AuthError", err)
	}
	if calls != 1 {
		t.Errorf("server saw %d requests, want 1: authentication never gets retried", calls)
	}
	if len(waits.all()) != 0 {
		t.Errorf("backed off %v before giving up, want no wait at all", waits.all())
	}
	if authErr.RequestID != "req_123" {
		t.Errorf("RequestID = %q, want it taken from the response header", authErr.RequestID)
	}
}

// §6: nothing the user sees may quote the searched text or the key.
func TestErrorsLeakNeitherKeyNorLines(t *testing.T) {
	const line = "totally-secret-line-content"

	for _, tt := range []struct {
		name   string
		status int
	}{
		{name: "auth", status: http.StatusUnauthorized},
		{name: "validation", status: http.StatusUnprocessableEntity},
		{name: "server", status: http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newTestClient(t, Config{MaxAttempts: 1}, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				// A server that echoes the request back is the case that matters.
				writeJSON(t, w, map[string]any{"detail": map[string]any{
					"error_type": "some_error",
					"message":    "rejected: " + line + " with key " + testKey,
				}})
			})

			_, err := client.Score(t.Context(), "anything", []string{line})
			if err == nil {
				t.Fatal("Score succeeded, want an error")
			}
			if msg := err.Error(); strings.Contains(msg, line) || strings.Contains(msg, testKey) {
				t.Errorf("error %q leaks the line or the key", msg)
			}
		})
	}
}

func TestScoreDoesNotRetryUnprocessableRequests(t *testing.T) {
	var calls int
	client, _ := newTestClient(t, Config{RetryBackoff: time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnprocessableEntity)
		writeJSON(t, w, map[string]any{"detail": map[string]any{"error_type": "validation_error"}})
	})

	_, err := client.Score(t.Context(), "anything", []string{"a"})

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Score error = %v, want an *APIError", err)
	}
	if apiErr.ErrorType != "validation_error" {
		t.Errorf("ErrorType = %q, want it taken from the body", apiErr.ErrorType)
	}
	if calls != 1 {
		t.Errorf("server saw %d requests, want 1: a rejected request stays rejected", calls)
	}
}

func TestScoreRejectsUnusableAnswers(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "a missing answer",
			body: map[string]any{"answers": map[string]any{"L1": map[string]any{"type": noulType, "noul": 0.5}}},
		},
		{
			name: "an answer without a probability",
			body: map[string]any{"answers": map[string]any{
				"L1": map[string]any{"type": noulType, "noul": 0.5},
				"L2": map[string]any{"type": noulType},
			}},
		},
		{
			name: "a probability out of range",
			body: map[string]any{"answers": map[string]any{
				"L1": map[string]any{"type": noulType, "noul": 0.5},
				"L2": map[string]any{"type": noulType, "noul": 1.5},
			}},
		},
		{
			name: "a body that is not the documented shape",
			body: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			client, _ := newTestClient(t, Config{RetryBackoff: time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
				calls++
				writeJSON(t, w, tt.body)
			})

			if _, err := client.Score(t.Context(), "anything", []string{"a", "b"}); err == nil {
				t.Fatal("Score succeeded on an unusable response, want an error")
			}
			// Retrying cannot repair a response the server meant to send.
			if calls != 1 {
				t.Errorf("server saw %d requests, want 1", calls)
			}
		})
	}
}

func TestScoreRetriesConnectionFailures(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing is listening, so every attempt fails to connect

	client, err := New(Config{APIKey: testKey, BaseURL: url, MaxAttempts: 2, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := &waits{}
	client.sleep = w.record

	if _, err := client.Score(t.Context(), "anything", []string{"a"}); err == nil {
		t.Fatal("Score succeeded with no server, want an error")
	}
	if got := len(w.all()); got != 1 {
		t.Errorf("waited %d times, want 1 retry", got)
	}
}

func TestScoreStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	client, _ := newTestClient(t, Config{RetryBackoff: time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
		cancel()
		http.Error(w, "busy", http.StatusServiceUnavailable)
	})

	_, err := client.Score(ctx, "anything", []string{"a"})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Score error = %v, want context.Canceled", err)
	}
}

func TestNewRequiresAnAPIKey(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("New without a key = %v, want ErrNoAPIKey", err)
	}
}

func readRequest(t *testing.T, r *http.Request) (request, error) {
	t.Helper()

	var req request
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		t.Errorf("decode request: %v", err)
	}
	return req, err
}

// A server that wants a longer pause than the client is willing to take is
// refusing the batch; hanging on it would freeze the whole grep.
func TestScoreStopsWhenRetryAfterIsTooLong(t *testing.T) {
	var calls int
	client, waits := newTestClient(t, Config{RetryBackoff: time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", strconv.Itoa(int(maxRetryBackoff/time.Second)+1))
		http.Error(w, "come back tomorrow", http.StatusTooManyRequests)
	})

	_, err := client.Score(t.Context(), "anything", []string{"a"})

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Score error = %v, want an *APIError", err)
	}
	if calls != 1 {
		t.Errorf("server saw %d requests, want 1", calls)
	}
	if len(waits.all()) != 0 {
		t.Errorf("waited %v, want no wait at all", waits.all())
	}
}

// Backoff must stay bounded and positive however many attempts a caller asks
// for: doubling far enough would otherwise overflow into a negative delay.
func TestBackoffStaysWithinTheCap(t *testing.T) {
	client, err := New(Config{APIKey: testKey, RetryBackoff: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, attempt := range []int{1, 2, 10, 64, 1000} {
		got := client.backoff(attempt, nil)
		if got <= 0 || got > maxRetryBackoff {
			t.Errorf("backoff(%d) = %v, want it in (0, %v]", attempt, got, maxRetryBackoff)
		}
	}
}

// A misdirected base URL can answer with something endless; it must be cut off
// rather than read into memory in full.
func TestScoreStopsReadingAnEndlessResponse(t *testing.T) {
	const total = 32 * maxResponseBytes

	var written atomic.Int64
	client, _ := newTestClient(t, Config{MaxAttempts: 1}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A JSON string that never ends: the decoder cannot give up on its own,
		// only the read limit stops it.
		chunk := `{"answers": "` + strings.Repeat("x", 64*1024)
		for written.Load() < total {
			n, err := io.WriteString(w, chunk)
			written.Add(int64(n))
			if err != nil {
				return
			}
			chunk = chunk[len(chunk)-64*1024:]
		}
	})

	if _, err := client.Score(t.Context(), "anything", []string{"a"}); err == nil {
		t.Fatal("Score succeeded on an endless response, want an error")
	}
	if got := written.Load(); got >= total/2 {
		t.Errorf("read %d bytes of the response, want it cut off well before %d", got, total)
	}
}

// The scheduler keeps several chunks in flight on one Client, so its scoring
// has to hold up under -race.
func TestClientIsSafeForConcurrentUse(t *testing.T) {
	client, _ := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		req, err := readRequest(t, r)
		if err != nil {
			return
		}
		answerWith(t, w, req, func(line string) float64 {
			if line == "match" {
				return 0.9
			}
			return 0.1
		})
	})

	const goroutines = 8
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := client.Score(t.Context(), "anything", []string{"match", "miss"})
			if err != nil {
				t.Errorf("Score: %v", err)
				return
			}
			if got[0] != 0.9 || got[1] != 0.1 {
				t.Errorf("scores = %v, want [0.9 0.1]", got)
			}
		}()
	}
	wg.Wait()
}
