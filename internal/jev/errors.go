package jev

import (
	"fmt"
	"net/http"
	"time"
)

// AuthError reports credentials the server would not accept: a rejected key
// (401) or one not allowed to do this (403). It is the one failure the CLI must
// not walk away from, because every later batch would fail the same way, so §4
// of the plan has jevgrep stop immediately instead of retrying or moving on.
// Callers tell it apart from a batch failure with errors.As.
type AuthError struct {
	StatusCode int
	RequestID  string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("jev: authentication failed (HTTP %d%s)", e.StatusCode, requestIDSuffix(e.RequestID))
}

// APIError reports a request the server refused or could not serve. Unlike
// AuthError it is confined to one batch: the CLI reports it and carries on with
// the rest of the input, ending at exit code 2.
type APIError struct {
	StatusCode int
	// ErrorType is the server's short machine-readable label
	// ("rate_limit_error", ...). The human-readable message that comes with it
	// is deliberately dropped: §6 forbids echoing searched line content, and a
	// validation message is exactly where the server would quote it back.
	ErrorType string
	RequestID string
	// Attempts is how many times the request was sent, so that "failed after
	// retrying" is distinguishable from "failed once, retrying would not help".
	Attempts int

	// retryAfter is the server's own Retry-After, unexported because it is
	// spent inside Score and means nothing to a caller holding the error.
	retryAfter time.Duration
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("jev: request failed: HTTP %d", e.StatusCode)
	if e.ErrorType != "" {
		msg += " " + e.ErrorType
	}
	msg += requestIDSuffix(e.RequestID)
	if e.Attempts > 1 {
		msg += fmt.Sprintf(" after %d attempts", e.Attempts)
	}
	return msg
}

// retryable reports whether sending the very same request again could plausibly
// succeed. A 4xx other than these says the request itself is wrong, and no
// amount of backoff fixes that.
func (e *APIError) retryable() bool {
	switch e.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	// Includes 529 "overloaded", which is outside the registered codes but is
	// what the API documents for a busy backend.
	return e.StatusCode >= 500
}

func requestIDSuffix(id string) string {
	if id == "" {
		return ""
	}
	return " (request " + id + ")"
}
