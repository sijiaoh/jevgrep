package apikey_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sijiaoh/jevgrep/internal/apikey"
	"github.com/sijiaoh/jevgrep/internal/jev"
)

const testKey = "sk-typed-at-the-prompt"

// fakeTerminal stands in for the real terminal so every --login branch can be
// tested in-process, which is the whole reason apikey.Terminal is an interface.
type fakeTerminal struct {
	notTerminal map[apikey.Stream]bool
	secret      string
	readErr     error
	reads       int
}

func (f *fakeTerminal) IsTerminal(s apikey.Stream) bool { return !f.notTerminal[s] }

func (f *fakeTerminal) ReadSecret() (string, error) {
	f.reads++
	if f.readErr != nil {
		return "", f.readErr
	}
	return f.secret, nil
}

func TestLoginStoresAKeyTheAPIAccepts(t *testing.T) {
	isolate(t)
	srv := scoringServer(t, http.StatusOK)
	term := &fakeTerminal{secret: testKey}
	var stderr bytes.Buffer

	path, err := login(t, term, &stderr, srv.URL)
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}

	if path != keyPath(t) {
		t.Errorf("Login() = %q, want the key file path %q", path, keyPath(t))
	}
	got, err := apikey.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got != testKey {
		t.Errorf("Load() = %q, want the key that was entered", got)
	}
	// The prompt ends with "key: " and no newline, so the user types on the
	// same line; the newline after it is the one the terminal did not echo.
	if want := guidanceLine + "TypeSafe API key: \n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

// guidanceLine is the line --login prints above the prompt. It is spelled out
// here rather than imported so that a change to the wording has to be made
// twice, on purpose: it is the only thing standing between a user and a prompt
// that looks like a hung program.
const guidanceLine = "jevgrep: paste a key from " + apikey.SignupURL + " (it will not be echoed)\n"

// The test above pins the two lines byte for byte. This one pins the two
// things the first of them is *for*, so that a rewording that drops either
// half fails even though both literals were updated together.
func TestLoginSaysWhereToGetAKeyAndThatItIsNotEchoed(t *testing.T) {
	isolate(t)
	srv := scoringServer(t, http.StatusOK)
	var stderr bytes.Buffer

	if _, err := login(t, &fakeTerminal{secret: testKey}, &stderr, srv.URL); err != nil {
		t.Fatalf("Login() = %v", err)
	}

	// The first line only: what follows is the prompt the user is typing into,
	// and an explanation printed after it is an explanation nobody read.
	first, _, _ := strings.Cut(stderr.String(), "\n")
	if !strings.Contains(first, apikey.SignupURL) {
		t.Errorf("first line = %q, want it to name %q", first, apikey.SignupURL)
	}
	if !strings.Contains(first, "not be echoed") {
		t.Errorf("first line = %q, want it to warn that the key is not echoed", first)
	}
}

func TestLoginVerifiesWithTheKeyThatWasEntered(t *testing.T) {
	isolate(t)
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		writeScores(t, w, r)
	}))
	t.Cleanup(srv.Close)

	if _, err := login(t, &fakeTerminal{secret: testKey}, &bytes.Buffer{}, srv.URL); err != nil {
		t.Fatalf("Login() = %v", err)
	}

	if want := "Bearer " + testKey; gotAuth.Load() != want {
		t.Errorf("Authorization = %q, want %q", gotAuth.Load(), want)
	}
}

func TestLoginRefusesWithoutATerminal(t *testing.T) {
	for _, stream := range []apikey.Stream{apikey.Stdin, apikey.Stderr} {
		t.Run(streamName(stream), func(t *testing.T) {
			isolate(t)
			term := &fakeTerminal{notTerminal: map[apikey.Stream]bool{stream: true}, secret: testKey}
			var stderr bytes.Buffer

			_, err := login(t, term, &stderr, "http://127.0.0.1:0")

			if !errors.Is(err, apikey.ErrNotTerminal) {
				t.Errorf("Login() = %v, want ErrNotTerminal", err)
			}
			if term.reads != 0 {
				t.Error("Login() read a secret with no terminal to read it from")
			}
			// Nothing is printed either: the prompt would land in whatever the
			// stream was redirected to.
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", stderr.String())
			}
			assertNothingSaved(t)
		})
	}
}

func TestLoginRefusesAnEmptyKey(t *testing.T) {
	isolate(t)

	_, err := login(t, &fakeTerminal{secret: "  "}, &bytes.Buffer{}, "http://127.0.0.1:0")

	if !errors.Is(err, apikey.ErrNoKeyEntered) {
		t.Errorf("Login() = %v, want ErrNoKeyEntered", err)
	}
	assertNothingSaved(t)
}

// Ctrl-D with nothing typed is the other way out of the prompt.
func TestLoginTreatsEndOfInputAsBackingOut(t *testing.T) {
	isolate(t)

	_, err := login(t, &fakeTerminal{readErr: io.EOF}, &bytes.Buffer{}, "http://127.0.0.1:0")

	if !errors.Is(err, apikey.ErrNoKeyEntered) {
		t.Errorf("Login() = %v, want ErrNoKeyEntered", err)
	}
	assertNothingSaved(t)
}

func TestLoginReportsAReadFailure(t *testing.T) {
	isolate(t)
	readErr := errors.New("terminal went away")

	_, err := login(t, &fakeTerminal{readErr: readErr}, &bytes.Buffer{}, "http://127.0.0.1:0")

	if !errors.Is(err, readErr) {
		t.Errorf("Login() = %v, want the read error", err)
	}
	assertNothingSaved(t)
}

// 401 is a mistyped or revoked key; 403 is a key the account may not use here,
// which is just as much a reason not to store it. Both must stay reachable as
// *jev.AuthError so the CLI can word the two differently.
func TestLoginDoesNotStoreAKeyTheAPIRejects(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			isolate(t)
			srv := scoringServer(t, status)

			_, err := login(t, &fakeTerminal{secret: testKey}, &bytes.Buffer{}, srv.URL)

			if !errors.Is(err, apikey.ErrKeyRejected) {
				t.Fatalf("Login() = %v, want ErrKeyRejected", err)
			}
			var authErr *jev.AuthError
			if !errors.As(err, &authErr) {
				t.Fatalf("Login() error is %v, want it to wrap *jev.AuthError", err)
			}
			if authErr.StatusCode != status {
				t.Errorf("AuthError.StatusCode = %d, want %d", authErr.StatusCode, status)
			}
			assertNothingSaved(t)
		})
	}
}

func TestLoginDoesNotStoreAKeyItCouldNotCheck(t *testing.T) {
	isolate(t)
	srv := scoringServer(t, http.StatusServiceUnavailable)

	_, err := login(t, &fakeTerminal{secret: testKey}, &bytes.Buffer{}, srv.URL)

	if err == nil || errors.Is(err, apikey.ErrKeyRejected) {
		t.Fatalf("Login() = %v, want a plain failure, not a rejected key", err)
	}
	assertNothingSaved(t)
}

// A user waiting at a prompt is told the API is down now, not after a backoff
// schedule they cannot see.
func TestLoginSendsOneRequestAndDoesNotRetry(t *testing.T) {
	isolate(t)
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	if _, err := login(t, &fakeTerminal{secret: testKey}, &bytes.Buffer{}, srv.URL); err == nil {
		t.Fatal("Login() = nil, want an error")
	}

	if got := requests.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1", got)
	}
}

func TestLoginErrorsNeverContainTheKey(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			isolate(t)
			srv := scoringServer(t, status)
			var stderr bytes.Buffer

			_, err := login(t, &fakeTerminal{secret: testKey}, &stderr, srv.URL)

			if err == nil {
				t.Fatal("Login() = nil, want an error")
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("Login() error = %q, want it not to contain the key", err)
			}
			if strings.Contains(stderr.String(), testKey) {
				t.Errorf("stderr = %q, want it not to contain the key", stderr.String())
			}
		})
	}
}

func login(t *testing.T, term apikey.Terminal, stderr *bytes.Buffer, baseURL string) (string, error) {
	t.Helper()
	return apikey.Login(context.Background(), apikey.LoginOptions{
		Terminal: term,
		Stderr:   stderr,
		BaseURL:  baseURL,
	})
}

// scoringServer answers every request with status, scoring the request's lines
// when that status is 200.
func scoringServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		writeScores(t, w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeScores(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var req struct {
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Errorf("decode request: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	answers := make(map[string]any, len(req.Questions))
	for id := range req.Questions {
		answers[id] = map[string]any{"noul": 0.5}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"answers": answers,
		"usage":   map[string]any{"input_tokens": 1, "output_tokens": 0},
	}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func assertNothingSaved(t *testing.T) {
	t.Helper()
	if _, err := apikey.Load(); !errors.Is(err, apikey.ErrNotFound) {
		t.Errorf("Load() = %v after a failed login, want ErrNotFound: nothing may be stored", err)
	}
}

func streamName(s apikey.Stream) string {
	switch s {
	case apikey.Stdin:
		return "stdin"
	case apikey.Stdout:
		return "stdout"
	case apikey.Stderr:
		return "stderr"
	}
	return "unknown"
}
