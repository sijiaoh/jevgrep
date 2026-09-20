package apikey

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sijiaoh/jevgrep/internal/jev"
)

// The errors --login answers with. Each one is a different thing to tell the
// user, and the wording of that is the CLI's business, not this package's.
var (
	// ErrNotTerminal reports that --login was run without a terminal to prompt
	// on. It is not something to work around: reading a key from a pipe would
	// put it in the shell history or the CI log of whoever set it up.
	ErrNotTerminal = errors.New("apikey: --login needs a terminal")
	// ErrNoKeyEntered reports an empty line at the prompt, which is how a user
	// backs out.
	ErrNoKeyEntered = errors.New("apikey: no key entered")
	// ErrKeyRejected reports that the server would not accept the key. The
	// *jev.AuthError it wraps is still reachable with errors.As, so the CLI can
	// separate "wrong key" (401) from "this key may not do that" (403).
	ErrKeyRejected = errors.New("apikey: the API rejected the key")
	// ErrNotSaved reports a key the API accepted that could not be written
	// down. It is told apart from the failures above because it is the only
	// one where nothing is wrong with the key or the network, and the user has
	// a disk or a permission to fix instead.
	ErrNotSaved = errors.New("apikey: the key could not be stored")
)

// What --login prints before it reads. The line above the prompt earns its
// place twice over: the user got here from a hint they have since scrolled
// past, and the terminal echoes nothing while they paste, which reads as a
// hung program to anyone not expecting it. It is built from SignupURL so the
// address cannot drift from the one the "no API key" hint gives.
//
// It is worded here rather than by the CLI, which owns the wording of
// everything else --login says, because only this function knows whether a
// prompt is about to happen at all: printing it before the call would put it
// on top of the "--login needs a terminal" error, where there is no prompt to
// explain.
const (
	guidance = "jevgrep: paste a key from " + SignupURL + " (it will not be echoed)\n"
	prompt   = "TypeSafe API key: "
)

// LoginOptions is what Login needs from the process around it.
type LoginOptions struct {
	Terminal Terminal
	Stderr   io.Writer

	// BaseURL is passed to jev. It exists so tests can verify against an
	// httptest server; --login takes no options of its own, and the key is
	// checked against the default model deliberately — what is being verified
	// is the key, not a model the user asked for.
	BaseURL string
}

// Login asks for a key, checks it against the API and stores it, returning the
// path it was written to.
//
// Everything it prints goes to opts.Stderr and stdout is left untouched:
// --login exists so a key can be set up without disturbing whatever jevgrep's
// output is piped into.
func Login(ctx context.Context, opts LoginOptions) (string, error) {
	// stderr is checked as well as stdin, because that is where the prompt
	// goes: a key typed blind into a redirected prompt is a key typed blind.
	if !opts.Terminal.IsTerminal(Stdin) || !opts.Terminal.IsTerminal(Stderr) {
		return "", ErrNotTerminal
	}

	fmt.Fprint(opts.Stderr, guidance+prompt)
	key, err := opts.Terminal.ReadSecret()
	// The newline the terminal did not echo, so that whatever is printed next
	// starts on its own line.
	fmt.Fprintln(opts.Stderr)
	if err != nil {
		// Ctrl-D at the prompt reaches us as EOF with nothing typed. That is a
		// user backing out, the same as pressing Enter on an empty line, and
		// it deserves the same answer rather than a read error.
		if errors.Is(err, io.EOF) {
			return "", ErrNoKeyEntered
		}
		return "", err
	}

	// A pasted key picks up spaces surprisingly often, and a key with a space
	// on the end fails authentication in a way that looks nothing like its
	// cause.
	key = strings.TrimSpace(key)
	if key == "" {
		return "", ErrNoKeyEntered
	}

	if err := verify(ctx, key, opts); err != nil {
		return "", err
	}
	path, err := Save(key)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotSaved, err)
	}
	return path, nil
}

// verify spends one small request to find out whether the key works, before
// anything is written. A key that was mistyped is otherwise found out by the
// next real search, long after the user has stopped thinking about it.
func verify(ctx context.Context, key string, opts LoginOptions) error {
	client, err := jev.New(jev.Config{
		APIKey:  key,
		BaseURL: opts.BaseURL,
		// One attempt: the user is sitting at a prompt. Waiting out a backoff
		// schedule to tell them the network is down is worse than telling them
		// now, and they can just run --login again.
		MaxAttempts: 1,
	})
	if err != nil {
		return err
	}

	// The smallest request the API takes: one short line, one short meaning.
	if _, err := client.Score(ctx, "ok", []string{"ok"}); err != nil {
		var authErr *jev.AuthError
		if errors.As(err, &authErr) {
			return fmt.Errorf("%w: %w", ErrKeyRejected, err)
		}
		return err
	}
	return nil
}
