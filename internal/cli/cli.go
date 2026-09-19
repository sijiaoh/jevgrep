// Package cli implements the jevgrep command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"net/http"
	"os"
	"os/signal"
	"sync"

	"github.com/sijiaoh/jevgrep/internal/apikey"
	"github.com/sijiaoh/jevgrep/internal/buildinfo"
	"github.com/sijiaoh/jevgrep/internal/input"
	"github.com/sijiaoh/jevgrep/internal/jev"
	"github.com/sijiaoh/jevgrep/internal/output"
	"github.com/sijiaoh/jevgrep/internal/search"
)

// Exit codes follow grep: 0 matched, 1 no match, 2 error. Callers of Run pass
// the result straight to os.Exit. An error outranks a match: a run that
// printed lines and also failed to read a file exits 2, as grep does.
const (
	ExitMatch   = 0
	ExitNoMatch = 1
	ExitError   = 2
	// ExitInterrupt is 128 + SIGINT, the shell's convention for a process the
	// user stopped.
	ExitInterrupt = 130
)

// environment is what jevgrep needs from the process that it cannot be given
// through Run's writers. Run's signature is fixed (all output goes through the
// writers, the exit code is the return value), so the parts that are not
// writers are gathered here, where a test can replace them.
type environment struct {
	stdin    io.Reader
	terminal apikey.Terminal
	// baseURL overrides the API root, so tests can point at a local server.
	// There is no option for it.
	baseURL string
}

// Run executes jevgrep with the given arguments (excluding the program name)
// and returns the process exit code. All output goes to the given writers so
// that tests never touch the real stdout/stderr.
func Run(args []string, stdout, stderr io.Writer) int {
	env := environment{stdin: os.Stdin, terminal: apikey.OSTerminal{}}
	return run(env, args, stdout, stderr)
}

func run(env environment, args []string, stdout, stderr io.Writer) int {
	cfg, err := parse(args)
	switch {
	// These three outrank each other in this order and ignore the rest of the
	// command line: someone who asks for help on a command line they got wrong
	// wants the help, not a second complaint about the typo.
	case cfg.help:
		fmt.Fprint(stdout, help())
		return ExitMatch
	case cfg.version:
		fmt.Fprintf(stdout, "jevgrep %s\n", buildinfo.Version())
		return ExitMatch
	case cfg.login:
		return login(env, stderr)
	case err != nil:
		return usageFailure(stderr, err)
	}
	return grep(env, cfg, stdout, stderr)
}

// usageFailure prints a wrong command line. The usage line appears exactly
// once, so that the real complaint is the thing that stands out.
func usageFailure(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "jevgrep: %s\n%s\nTry 'jevgrep --help' for more information.\n", err, usage)
	return ExitError
}

func login(env environment, stderr io.Writer) int {
	ctx, stop := interruptible(context.Background())
	defer stop()

	path, err := apikey.Login(ctx, apikey.LoginOptions{
		Terminal: env.terminal,
		Stderr:   stderr,
		BaseURL:  env.baseURL,
	})
	var authErr *jev.AuthError
	switch {
	case err == nil:
		// The real absolute path, on stderr with everything else --login
		// prints, so that it can be copied and stdout stays clean.
		fmt.Fprintf(stderr, "jevgrep: key saved to %s\n", path)
		return ExitMatch
	case errors.Is(err, apikey.ErrNotTerminal):
		return failure(stderr, "--login needs a terminal; set %s instead", apikey.EnvVar)
	case errors.Is(err, apikey.ErrNoKeyEntered):
		return failure(stderr, "no key entered")
	case errors.Is(err, apikey.ErrKeyRejected) && errors.As(err, &authErr):
		if authErr.StatusCode == http.StatusForbidden {
			return failure(stderr, "that key is not allowed to use this API (HTTP %d); nothing was saved", http.StatusForbidden)
		}
		return failure(stderr, "that key was rejected; nothing was saved")
	// The key was good and the disk was not. Saying "could not reach the API"
	// here would send the user off to debug a network that is working.
	case errors.Is(err, apikey.ErrNotSaved):
		return failure(stderr, "the key works, but it could not be stored: %s", osReason(err))
	case errors.Is(err, context.Canceled):
		return ExitInterrupt
	default:
		return failure(stderr, "could not reach the API: %s; nothing was saved", reason(err))
	}
}

func grep(env environment, cfg config, stdout, stderr io.Writer) int {
	expr, err := search.Compile(cfg.terms, cfg.threshold, cfg.invert)
	if err != nil {
		return usageFailure(stderr, compileError(err))
	}

	// How many PATH operands there were is what decides whether output carries
	// file names, and output owns that rule; reading stdin because there were
	// none is zero of them.
	paths, operands := cfg.paths, len(cfg.paths)
	if operands == 0 {
		// grep would read the terminal here. jevgrep bills for every line it
		// reads, so a prompt that looks like a hang is a prompt that runs up a
		// bill nobody meant to start.
		if env.terminal.IsTerminal(apikey.Stdin) {
			return usageFailure(stderr, usageErrorf("no input; give a PATH or pipe data in"))
		}
		paths = []string{input.StdinPath}
	}

	key, err := apikey.Load()
	if err != nil {
		return keyFailure(stderr, err)
	}
	client, err := jev.New(jev.Config{APIKey: key, Model: cfg.model, BaseURL: env.baseURL})
	if err != nil {
		return failure(stderr, "%s", err)
	}

	// Unbuffered on purpose: output.Printer writes whole lines straight
	// through, which is what makes matches appear while the rest is still
	// being scored.
	printer := output.New(stdout, output.Options{
		Filenames:  cfg.filenames,
		Operands:   operands,
		LineNumber: cfg.lineNumber,
	})

	ctx, stop := interruptible(context.Background())
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	log := &errorLog{w: stderr}
	r := &reader{stdin: env.stdin, log: log}
	var (
		matched bool
		fatal   bool
	)
	searcher := search.New(client, expr, search.Options{
		Emit: func(l input.Line) {
			matched = true
			printer.Print(l)
		},
		Fail: func(f search.Failure) {
			// A request the server will refuse again is refused for every
			// batch, so reporting it once and stopping says more than the same
			// line repeated for the rest of the input. It is a different
			// question from jev's "is this worth resending", which has already
			// been answered by the time a failure reaches here.
			var apiErr *jev.APIError
			if errors.As(f.Err, &apiErr) && isClientError(apiErr.StatusCode) {
				if !fatal {
					fatal = true
					log.printf("the API rejected the request: %s", reason(f.Err))
					cancel()
				}
				return
			}
			log.printf("%s: lines %d-%d: could not be scored: %s", f.File, f.First, f.Last, reason(f.Err))
		},
	})

	runErr := searcher.Run(ctx, r.lines(paths))
	var authErr *jev.AuthError
	switch {
	// Already reported, and the cancel it triggered is what Run returns.
	case fatal:
	case errors.As(runErr, &authErr):
		if authErr.StatusCode == http.StatusForbidden {
			log.printf("that key is not allowed to use this API (HTTP %d)", http.StatusForbidden)
		} else {
			log.printf("the API key was rejected (HTTP %d); run `jevgrep --login` to store a new one", authErr.StatusCode)
		}
	case errors.Is(runErr, context.Canceled):
		// Whatever was printed before the interrupt stays printed.
		return ExitInterrupt
	case runErr != nil:
		log.printf("%s", reason(runErr))
	}

	// The output may have failed silently: Print has no way to report it.
	if err := printer.Err(); err != nil {
		log.printf("could not write the output: %s", osReason(err))
	}

	switch {
	case log.failed():
		return ExitError
	case matched:
		return ExitMatch
	}
	return ExitNoMatch
}

// isClientError reports whether the server said the request itself was wrong —
// a mistyped --model, say. 408 and 429 are excluded because they say "later",
// not "never".
func isClientError(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return status >= 400 && status < 500
}

func keyFailure(stderr io.Writer, err error) int {
	var pathErr *fs.PathError
	switch {
	case errors.Is(err, apikey.ErrNotFound):
		fmt.Fprintf(stderr, "jevgrep: no API key found\n  set %s, or run `jevgrep --login` to store one\n  get a key at %s\n",
			apikey.EnvVar, apikey.SignupURL)
		return ExitError
	// A key file that is there but unreadable is not a missing key: sending
	// this user to --login would have them overwrite a file they already have.
	case errors.As(err, &pathErr):
		return failure(stderr, "%s: %s", pathErr.Path, pathErr.Err)
	}
	return failure(stderr, "%s", err)
}

// reader turns the PATH operands into the single stream of lines the searcher
// reads. A path that cannot be opened is reported and skipped: the other
// operands are still worth searching, and the run ends at exit code 2 anyway.
type reader struct {
	stdin io.Reader
	log   *errorLog
}

func (r *reader) lines(paths []string) iter.Seq[input.Line] {
	return func(yield func(input.Line) bool) {
		for _, path := range paths {
			if !r.read(path, yield) {
				return
			}
		}
	}
}

// read streams one input, reporting whether the consumer wants more.
func (r *reader) read(path string, yield func(input.Line) bool) bool {
	scanner, err := input.Open(path, r.stdin)
	if err != nil {
		r.log.printf("%s: %s", displayName(path), osMessage(err))
		return true
	}
	defer func() { _ = scanner.Close() }()

	for scanner.Scan() {
		if !yield(scanner.Line()) {
			return false
		}
	}
	if err := scanner.Err(); err != nil {
		r.log.printf("%s: read error: %s", displayName(path), osMessage(err))
	}
	return true
}

// displayName is what output calls this input, so that an error about it and a
// matching line from it name the same thing.
func displayName(path string) string {
	if path == input.StdinPath {
		return input.StdinName
	}
	return path
}

// errorLog is every "jevgrep: ..." line that is not a usage error, and the
// memory that there was one. It is guarded because the reader goroutine keeps
// running for a moment after Run returns on an interrupt, and it reports read
// errors from there.
type errorLog struct {
	mu   sync.Mutex
	w    io.Writer
	seen bool
}

func (l *errorLog) printf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = true
	fmt.Fprintf(l.w, "jevgrep: "+format+"\n", a...)
}

func (l *errorLog) failed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen
}

func failure(stderr io.Writer, format string, a ...any) int {
	fmt.Fprintf(stderr, "jevgrep: "+format+"\n", a...)
	return ExitError
}

// osMessage is the system's own words for a failed open or read, without the
// path: "jevgrep: notes.txt: no such file or directory", never the doubled
// "jevgrep: notes.txt: open notes.txt: no such file or directory" that the
// error's own text would produce.
func osMessage(err error) string {
	var pathErr *fs.PathError
	switch {
	case errors.Is(err, input.ErrIsDirectory):
		return input.ErrIsDirectory.Error()
	case errors.As(err, &pathErr):
		return pathErr.Err.Error()
	}
	return err.Error()
}

// osReason is for the messages that do not name a path themselves, where the
// path is the most useful part of the system's message rather than a
// repetition of it.
func osReason(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Error()
	}
	return err.Error()
}

// reason describes a failed request in transport terms only. The server's own
// message is left out on purpose: a validation error is exactly where the API
// quotes the submitted lines back, and no searched line may reach the screen.
func reason(err error) string {
	var apiErr *jev.APIError
	if !errors.As(err, &apiErr) {
		return err.Error()
	}
	msg := fmt.Sprintf("HTTP %d", apiErr.StatusCode)
	if apiErr.ErrorType != "" {
		msg += " " + apiErr.ErrorType
	}
	if apiErr.Attempts > 1 {
		msg += fmt.Sprintf(" after %d attempts", apiErr.Attempts)
	}
	if apiErr.RequestID != "" {
		msg += " (request " + apiErr.RequestID + ")"
	}
	return msg
}

// interruptible cancels the context on the first SIGINT and lets the second
// one end the process at once: the user who is pressing Ctrl-C again has
// already decided not to wait for a clean stop.
func interruptible(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	done := make(chan struct{})

	go func() {
		select {
		case <-interrupts:
			// Handing SIGINT back to the default handler is what makes the
			// next one immediate, with no code of ours in the way.
			signal.Stop(interrupts)
			cancel()
		case <-done:
		}
	}()

	return ctx, func() {
		close(done)
		signal.Stop(interrupts)
		cancel()
	}
}
