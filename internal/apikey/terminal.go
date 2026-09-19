package apikey

import (
	"fmt"
	"os"
	"os/signal"
	"strings"

	// golang.org/x/term is this module's first dependency, and it is here for
	// two things the standard library does not expose: asking whether a file
	// descriptor is a terminal, and turning echo off while a key is typed.
	// Both are per-platform ioctls (Unix) or Console API calls (Windows).
	// os.Stat's ModeCharDevice is the usual stdlib stand-in and it is wrong for
	// us — it calls `< /dev/null` a terminal, which would make --login prompt
	// into a void and make "no PATH given" hang instead of erroring. x/term is
	// maintained by the Go team, is small, and pulls in nothing but
	// golang.org/x/sys; writing these syscalls ourselves would mean owning
	// them on three platforms.
	"golang.org/x/term"
)

// Stream names one of the process's standard streams.
//
// Terminal is addressed by stream rather than by file descriptor because
// cli.Run only ever has io.Writers, which cannot answer either question, and
// because a test implementation is then a struct literal instead of a pair of
// real pseudo-terminals.
type Stream int

const (
	Stdin Stream = iota
	Stdout
	Stderr
)

// Terminal is what jevgrep needs to know about the terminal it was started
// from. --login uses all of it; the argument parser uses IsTerminal(Stdin) to
// decide whether "no PATH" means "read the pipe" or "there is no input".
//
// It is an interface so both callers can be tested in-process, without a
// pseudo-terminal and without a subprocess.
type Terminal interface {
	// IsTerminal reports whether the stream is attached to a terminal.
	IsTerminal(s Stream) bool
	// ReadSecret reads one line typed at the terminal without echoing it, with
	// the trailing newline removed. It restores the terminal's echo setting
	// before returning, on every path.
	ReadSecret() (string, error)
}

// OSTerminal is the Terminal of the process's real standard streams.
type OSTerminal struct{}

// IsTerminal implements Terminal.
func (OSTerminal) IsTerminal(s Stream) bool {
	f := osFile(s)
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// ReadSecret implements Terminal. It reads from stdin, which is also where the
// non-interactive user would pipe a key in — but --login refuses to run unless
// stdin is a terminal, so that never reaches here.
func (OSTerminal) ReadSecret() (string, error) {
	fd := int(os.Stdin.Fd())

	restore, err := restoreEchoOnInterrupt(fd)
	if err != nil {
		return "", err
	}
	defer restore()

	line, err := term.ReadPassword(fd)
	if err != nil {
		return "", fmt.Errorf("apikey: read from terminal: %w", err)
	}
	// x/term drops the line terminator its own platform uses and ignores the
	// other one. Both are trimmed here so that this method's promise holds
	// whichever platform it runs on: a key with a stray CR on the end fails
	// authentication in a way nobody would guess from looking at it.
	return strings.TrimRight(string(line), "\r\n"), nil
}

// restoreEchoOnInterrupt arranges for the terminal to get its echo back if the
// user presses Ctrl-C at the prompt. term.ReadPassword restores it when it
// returns or panics, but a signal kills the process between those, and the
// shell the user lands back in is then typing blind.
//
// The re-raise at the end matters: taking delivery of the signal here would
// otherwise swallow it, so the interrupt is passed on to whoever else handles
// it — the CLI's own handler, or the default one that ends the process. A CLI
// handler therefore sees the interrupt twice, its own delivery and this one;
// both mean "stop now", which is what it was going to do.
func restoreEchoOnInterrupt(fd int) (func(), error) {
	state, err := term.GetState(fd)
	if err != nil {
		return nil, fmt.Errorf("apikey: read terminal state: %w", err)
	}

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	done := make(chan struct{})

	go func() {
		select {
		case <-interrupts:
			_ = term.Restore(fd, state)
			signal.Stop(interrupts)
			reraiseInterrupt()
		case <-done:
		}
	}()

	return func() {
		close(done)
		signal.Stop(interrupts)
	}, nil
}

func reraiseInterrupt() {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		return
	}
	// Unsupported on Windows, where Ctrl-C is delivered by the console to the
	// whole process group anyway. Echo is already restored either way.
	_ = p.Signal(os.Interrupt)
}

func osFile(s Stream) *os.File {
	switch s {
	case Stdin:
		return os.Stdin
	case Stdout:
		return os.Stdout
	case Stderr:
		return os.Stderr
	}
	return nil
}
