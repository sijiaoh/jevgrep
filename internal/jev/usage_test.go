package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

// observer collects what a client says about the requests it sent. Score runs
// the observer on the goroutine that made the request, so this is guarded.
type observer struct {
	mu sync.Mutex
	at []Attempt
}

func (o *observer) record(a Attempt) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.at = append(o.at, a)
}

func (o *observer) all() []Attempt {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.at
}

func TestAnAnsweredRequestReportsWhatItWasCharged(t *testing.T) {
	obs := &observer{}
	client, _ := newTestClient(t, Config{OnAttempt: obs.record}, func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		answers := make(map[string]any, len(req.Questions))
		for id := range req.Questions {
			answers[id] = map[string]any{"type": noulType, "noul": 0.5}
		}
		writeJSON(t, w, map[string]any{
			"answers": answers,
			"usage":   map[string]any{"input_tokens": 312, "output_tokens": 38},
		})
	})

	if _, err := client.Score(context.Background(), "an error", []string{"a", "b"}); err != nil {
		t.Fatalf("Score: %v", err)
	}

	got := obs.all()
	if len(got) != 1 {
		t.Fatalf("observed %d attempts, want 1", len(got))
	}
	if !got[0].Reported || got[0].InputTokens != 312 {
		t.Errorf("attempt = %+v, want the 312 input tokens the API charged", got[0])
	}
}

// The field is documented as optional, and a response without it must not be
// read as a request that cost nothing: --stats falls back to its own estimate,
// which it can only do if it is told the difference.
func TestAResponseWithoutUsageReportsNothingRatherThanZero(t *testing.T) {
	obs := &observer{}
	client, _ := newTestClient(t, Config{OnAttempt: obs.record}, func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		answerWith(t, w, req, half)
	})

	if _, err := client.Score(context.Background(), "an error", []string{"a"}); err != nil {
		t.Fatalf("Score: %v", err)
	}

	got := obs.all()
	if len(got) != 1 {
		t.Fatalf("observed %d attempts, want 1", len(got))
	}
	if got[0].Reported {
		t.Errorf("attempt = %+v, want Reported false when the response carries no usage", got[0])
	}
}

// Retries are counted, because they are what a user who wonders where their
// afternoon went is actually looking at.
func TestEveryAttemptIsReportedRetriesIncluded(t *testing.T) {
	obs := &observer{}
	var calls int
	client, _ := newTestClient(t, Config{OnAttempt: obs.record}, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		answerWith(t, w, req, half)
	})

	if _, err := client.Score(context.Background(), "an error", []string{"a"}); err != nil {
		t.Fatalf("Score: %v", err)
	}

	got := obs.all()
	if len(got) != 3 {
		t.Fatalf("observed %d attempts, want 3: the two that failed count too", len(got))
	}
	for i, a := range got[:2] {
		if a.Reported || a.InputTokens != 0 {
			t.Errorf("attempt %d = %+v, want nothing reported for a request that failed", i, a)
		}
	}
}
