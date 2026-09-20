// Package jev scores lines against a meaning with TypeSafe's Jev model.
//
// One request carries a whole chunk of lines: the lines go into the request's
// state, and each gets a noul (yes/no) question whose answer is the calibrated
// probability that the line means what the user asked for. Splitting input into
// chunks is Split's job; Score sends exactly one.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/sijiaoh/jevgrep/internal/buildinfo"
)

const (
	// DefaultBaseURL is the TypeSafe API root. Config.BaseURL exists so tests
	// can point at an httptest server; there is no user-facing option for it.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the moving early-access route. §11: users who need
	// calibration to stay put pin a version with --model.
	DefaultModel = "jev-latest"

	scorePath = "/v1/systemone"

	defaultMaxAttempts  = 4
	defaultRetryBackoff = 500 * time.Millisecond
	maxRetryBackoff     = 30 * time.Second
	defaultTimeout      = 2 * time.Minute

	// A scored chunk answers in a few KB. The cap is what stops a misdirected
	// base URL, or a proxy's HTML error page, from being read into memory in
	// full before it can be rejected.
	maxResponseBytes = 1 << 20
)

// Config configures a Client. Every field but APIKey has a working default.
type Config struct {
	APIKey  string
	Model   string
	BaseURL string

	// MaxAttempts counts the first send, so 1 disables retrying.
	MaxAttempts int
	// RetryBackoff is the delay before the second attempt; it doubles after
	// each further failure, with jitter, up to a cap.
	RetryBackoff time.Duration

	// OnAttempt, when set, hears about every HTTP request this client sends,
	// retries included. It is the only way out of here for what a request
	// actually cost, which is what --stats reports: the API's own count is the
	// bill, and everything else jevgrep has is an estimate.
	//
	// It is called from the goroutine that made the request, so an
	// implementation must be safe for concurrent use.
	OnAttempt func(Attempt)
}

// Attempt is one HTTP request a Client sent.
type Attempt struct {
	// InputTokens is what the API said it charged. Reported says whether it
	// said anything at all: a request that failed, or whose answer carried no
	// usage, reports zero tokens and means "unknown", not "free".
	InputTokens int
	Reported    bool
}

// Client scores lines. It holds no state that a request changes, so it is safe
// for concurrent use, which is what the scheduler relies on to keep several
// chunks in flight.
type Client struct {
	apiKey     string
	model      string
	endpoint   string
	httpClient *http.Client

	maxAttempts  int
	retryBackoff time.Duration
	onAttempt    func(Attempt)

	// sleep is a field so tests can observe the backoff schedule without
	// actually waiting it out.
	sleep func(context.Context, time.Duration) error
}

// ErrNoAPIKey is returned by New when no credentials were supplied. Resolving
// where a key comes from is the caller's job.
var ErrNoAPIKey = errors.New("jev: no API key")

// New returns a Client, filling in defaults for whatever Config leaves unset.
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, ErrNoAPIKey
	}

	c := &Client{
		apiKey:       cfg.APIKey,
		model:        cfg.Model,
		httpClient:   &http.Client{Timeout: defaultTimeout},
		maxAttempts:  cfg.MaxAttempts,
		retryBackoff: cfg.RetryBackoff,
		onAttempt:    cfg.OnAttempt,
		sleep:        sleep,
	}
	if c.model == "" {
		c.model = DefaultModel
	}
	if c.maxAttempts < 1 {
		c.maxAttempts = defaultMaxAttempts
	}
	if c.retryBackoff <= 0 {
		c.retryBackoff = defaultRetryBackoff
	}

	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	endpoint, err := url.JoinPath(base, scorePath)
	if err != nil {
		return nil, fmt.Errorf("jev: bad base URL: %w", err)
	}
	c.endpoint = endpoint

	return c, nil
}

// Score returns the probability that each line means meaning, in the order the
// lines were given. It sends one request, retrying it with exponential backoff
// on rate limits, server errors and connection failures; an *AuthError is
// returned immediately and never retried.
//
// lines must be one chunk, as produced by Split.
func (c *Client) Score(ctx context.Context, meaning string, lines []string) ([]float64, error) {
	if len(lines) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(newRequest(c.model, meaning, lines))
	if err != nil {
		return nil, fmt.Errorf("jev: encode request: %w", err)
	}

	for attempt := 1; ; attempt++ {
		scores, err := c.attempt(ctx, body, len(lines))
		if err == nil {
			return scores, nil
		}

		var apiErr *APIError
		if errors.As(err, &apiErr) {
			apiErr.Attempts = attempt
		}
		if attempt >= c.maxAttempts || !shouldRetry(err, apiErr) {
			return nil, err
		}
		if err := c.sleep(ctx, c.backoff(attempt, apiErr)); err != nil {
			return nil, err
		}
	}
}

// shouldRetry reports whether sending the same request again is worth it.
func shouldRetry(err error, apiErr *APIError) bool {
	if apiErr == nil {
		// Transport failures never reached the server. Everything else that
		// gets here — a rejected key, an unusable response — would repeat
		// identically.
		return errors.Is(err, errRetryableTransport)
	}
	if !apiErr.retryable() {
		return false
	}
	// A server asking us to wait longer than we are willing to is refusing the
	// work. A grep reports that and moves on; it does not hang on it.
	return apiErr.retryAfter <= maxRetryBackoff
}

// errRetryableTransport marks the failures that never reached the server (DNS,
// dial, reset connection), which are worth another attempt.
var errRetryableTransport = errors.New("jev: transport failure")

func (c *Client) attempt(ctx context.Context, body []byte, wantLines int) ([]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jev: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "jevgrep/"+buildinfo.Version())

	// Every path from here on has put the request on the wire, so the observer
	// hears about it however it ends: an attempt that failed still took its
	// time, and a user asking where a run went is owed the retries too.
	at := Attempt{}
	defer func() { c.observe(at) }()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// The context ending is the user's doing, not a transient fault.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		// url.Error carries the URL and the cause, never the request body, so
		// this cannot leak line content or the key.
		return nil, fmt.Errorf("%w: %w", errRetryableTransport, err)
	}
	// jevgrep makes many requests in a row; draining what is left lets the
	// connection go back to the pool instead of being thrown away. Draining an
	// unbounded body would be a worse deal than losing the connection.
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}

	var decoded response
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("jev: decode response: %w", err)
	}

	at = decoded.attempt()
	return decoded.scores(wantLines)
}

func (c *Client) observe(a Attempt) {
	if c.onAttempt != nil {
		c.onAttempt(a)
	}
}

// backoff is exponential with equal jitter: half the delay is fixed so waits
// keep growing, half is random so retries from parallel chunks spread out
// instead of hammering the server in lockstep. A Retry-After from the server
// wins, since it knows when the limit actually resets.
func (c *Client) backoff(attempt int, apiErr *APIError) time.Duration {
	if apiErr != nil && apiErr.retryAfter > 0 {
		return apiErr.retryAfter
	}
	// Doubling by repetition rather than by shifting: MaxAttempts is a caller's
	// number, and a shift wide enough to overflow would turn the delay negative.
	d := c.retryBackoff
	for range attempt - 1 {
		if d >= maxRetryBackoff {
			break
		}
		d *= 2
	}
	d = min(d, maxRetryBackoff)
	return d/2 + rand.N(d/2+1)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func responseError(resp *http.Response) error {
	requestID := resp.Header.Get("X-TypeSafe-Request-Id")

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &AuthError{StatusCode: resp.StatusCode, RequestID: requestID}
	}
	return &APIError{
		StatusCode: resp.StatusCode,
		ErrorType:  errorType(io.LimitReader(resp.Body, maxResponseBytes)),
		RequestID:  requestID,
		retryAfter: retryAfter(resp.Header.Get("Retry-After")),
	}
}

// errorType pulls the server's label out of {"detail":{"error_type":...}},
// tolerating any other shape: a proxy in front of the API answers with its own
// body, and a 502 from one must still be reported as a 502.
func errorType(body io.Reader) string {
	var decoded struct {
		Detail struct {
			ErrorType string `json:"error_type"`
		} `json:"detail"`
	}
	if err := json.NewDecoder(body).Decode(&decoded); err != nil {
		return ""
	}
	return decoded.Detail.ErrorType
}

// retryAfter reads the delta-seconds form only. The HTTP-date form is legal but
// unused here, and guessing wrong is worse than falling back to our own backoff.
func retryAfter(header string) time.Duration {
	secs, err := strconv.Atoi(header)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
