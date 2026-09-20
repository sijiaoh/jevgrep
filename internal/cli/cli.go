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
	"time"

	"github.com/sijiaoh/jevgrep/internal/apikey"
	"github.com/sijiaoh/jevgrep/internal/buildinfo"
	"github.com/sijiaoh/jevgrep/internal/cache"
	"github.com/sijiaoh/jevgrep/internal/input"
	"github.com/sijiaoh/jevgrep/internal/jev"
	"github.com/sijiaoh/jevgrep/internal/output"
	"github.com/sijiaoh/jevgrep/internal/search"
	"github.com/sijiaoh/jevgrep/internal/walk"
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
	// now is the clock --stats times the run with. It is here so that a test
	// can assert the whole report, elapsed line and all, without waiting for
	// anything. run fills it in when the caller left it nil.
	now func() time.Time
}

// Run executes jevgrep with the given arguments (excluding the program name)
// and returns the process exit code. All output goes to the given writers so
// that tests never touch the real stdout/stderr.
func Run(args []string, stdout, stderr io.Writer) int {
	env := environment{stdin: os.Stdin, terminal: apikey.OSTerminal{}}
	return run(env, args, stdout, stderr)
}

func run(env environment, args []string, stdout, stderr io.Writer) int {
	if env.now == nil {
		env.now = time.Now
	}
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

	paths := cfg.paths
	switch {
	case len(paths) > 0:
	// -r with no PATH searches ".", as grep does, and this outranks §4's "no
	// PATH reads standard input": a command line that spells out -r means to
	// walk a tree, and quietly searching the pipe instead would be the most
	// expensive kind of surprise.
	case cfg.recursive:
		paths = []string{"."}
	// grep would read the terminal here. jevgrep bills for every line it
	// reads, so a prompt that looks like a hang is a prompt that runs up a
	// bill nobody meant to start.
	case env.terminal.IsTerminal(apikey.Stdin):
		return usageFailure(stderr, usageErrorf("no input; give a PATH or pipe data in"))
	default:
		paths = []string{input.StdinPath}
	}

	if cfg.dryRun {
		return dryRun(env, cfg, expr, paths, stdout, stderr)
	}

	key, err := apikey.Load()
	if err != nil {
		return keyFailure(stderr, err)
	}
	// The meter exists only for --stats. Without it nothing here counts
	// anything, which is what keeps a plain run free of the bookkeeping.
	var m *meter
	if cfg.stats {
		m = &meter{}
	}

	client, err := jev.New(jev.Config{
		APIKey:    key,
		Model:     cfg.model,
		BaseURL:   env.baseURL,
		OnAttempt: attemptObserver(m),
	})
	if err != nil {
		return failure(stderr, "%s", err)
	}

	scorer, recall, cacheUsed, closeCache := scoring(cfg, client)
	// Registered before the context's cancel, so it runs after it: the run is
	// torn down first, and then the cache is shut so that nothing it left in
	// flight can write a file once grep has returned.
	defer closeCache()
	if m != nil {
		scorer = meteringScorer{next: scorer, meter: m}
		// Installed even without a cache to ask: the count of questions is the
		// count of what was paid for, and it is taken where they are asked.
		recall = m.recall(recall)
	}

	// Unbuffered on purpose: output.Printer writes whole lines straight
	// through, which is what makes matches appear while the rest is still
	// being scored.
	printer := output.New(stdout, output.Options{
		Filenames:      cfg.filenames,
		MultipleInputs: multipleInputs(cfg, paths),
		LineNumber:     cfg.lineNumber,
		Null:           cfg.null,
		Color:          colorEnabled(env, cfg.color),
		Before:         cfg.before,
		After:          cfg.after,
		Context:        cfg.context,
		Score:          cfg.score,
		JSON:           cfg.json,
		Meanings:       expr.Meanings(),
		Headline:       expr.Headline,
	})

	ctx, stop := interruptible(context.Background())
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	log := &errorLog{w: stderr}
	files := &opened{}
	quotas := newQuotas(cfg)
	r := &reader{
		stdin:     env.stdin,
		log:       log,
		opened:    files,
		quotas:    quotas,
		recursive: cfg.recursive,
		walkOpts:  walkOptions(cfg),
	}
	out := &sink{
		mode:     cfg.mode(),
		printer:  printer,
		opened:   files,
		quotas:   quotas,
		maxCount: cfg.maxCount,
		stop:     cancel,
	}
	// Only lines printed with their context can owe a file anything past its
	// -m quota.
	if out.mode == modeLines {
		out.after = cfg.after
	}
	var fatal bool
	searcher := search.New(scorer, expr, search.Options{
		Emit:   out.line,
		Cached: recall,
		// The reader is asked about the file it is reading, not about the
		// line: this runs on the reader's own goroutine, inside the yield of
		// the line it is being asked about, which is what makes a plain field
		// enough to say which file that is.
		Skip: func(input.Line) bool { return quotas.skip(r.current) },
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

	lines := r.lines(paths)
	if m != nil {
		lines = m.watch(lines)
	}

	start := env.now()
	runErr := searcher.Run(ctx, lines)
	elapsed := env.now().Sub(start)
	var interrupted bool
	var authErr *jev.AuthError
	switch {
	// Already reported, and the cancel it triggered is what Run returns.
	case fatal:
	// -q stopped the run itself, at the first match. The lines that were never
	// read are the point of the option, not a failure.
	case out.quit:
	case errors.As(runErr, &authErr):
		if authErr.StatusCode == http.StatusForbidden {
			log.printf("that key is not allowed to use this API (HTTP %d)", http.StatusForbidden)
		} else {
			log.printf("the API key was rejected (HTTP %d); run `jevgrep --login` to store a new one", authErr.StatusCode)
		}
	case errors.Is(runErr, context.Canceled):
		// Whatever was printed before the interrupt stays printed. The exit
		// code waits until the end all the same: the run still owes --stats a
		// report of what it spent before it was stopped.
		interrupted = true
	case runErr != nil:
		log.printf("%s", reason(runErr))
	// Every file was read to its end, so -c and -L can say what each of them
	// came to. A run that was cut short says nothing: a count of a file that
	// was only half read is a wrong count stated as a fact.
	default:
		out.done()
	}

	// The output may have failed silently: Print has no way to report it. An
	// interrupted run says nothing about it: the batches Ctrl-C tore down are
	// not news, and neither is a half-written line.
	if err := printer.Err(); err != nil && !interrupted {
		log.printf("could not write the output: %s", osReason(err))
	}

	// However the run ended -- finished, part failed, key rejected, Ctrl-C --
	// the money is spent, and the moment a user most needs to know how much is
	// the moment the run did not finish. Failing to print it does not change
	// the exit code: the search is what was asked for, the report is not.
	if m != nil {
		log.report(m.report(files.count(), elapsed, cacheUsed))
	}
	if interrupted {
		return ExitInterrupt
	}

	switch {
	// -q answers the one question it was asked and nothing else: a match found
	// is exit 0 even where another file could not be read (the message for it
	// is on stderr all the same). Without a match the error still decides.
	case out.mode == modeQuiet && out.matched:
		return ExitMatch
	case log.failed():
		return ExitError
	// -L prints the files that did not match and still exits 1 when none did,
	// because the exit code answers "was a line selected", not "was anything
	// printed". grep does exactly this, and it is not a slip to fix.
	case out.matched:
		return ExitMatch
	}
	return ExitNoMatch
}

// scoring returns what scores the lines, what already knows some of the
// answers, what became of the cache, and how to shut it. Without a cache the
// client scores on its own and nothing knows an answer in advance, which is
// the shape --no-cache asks for.
//
// The returned func closes the cache; it is never nil, so the caller can defer
// it without asking whether there was a cache at all.
func scoring(cfg config, client *jev.Client) (search.Scorer, func(meaning, query string) (float64, bool), cacheState, func()) {
	c, state := openCache(cfg, cache.Open)
	if c == nil {
		return client, nil, state, func() {}
	}
	return c.Wrap(client), c.Lookup, state, c.Close
}

// lookup is what a dry run needs of the cache: the answers it already has, and
// nothing that would write one -- not even the directory, which is why it
// opens the cache the other way round.
func lookup(cfg config) (func(meaning, query string) (float64, bool), cacheState) {
	c, state := openCache(cfg, cache.OpenForReading)
	if c == nil {
		return nil, state
	}
	return c.Lookup, state
}

func openCache(cfg config, open func(string) (*cache.Cache, error)) (*cache.Cache, cacheState) {
	if cfg.noCache {
		return nil, cacheState{off: true}
	}
	// Where it would be is worked out apart from opening it, so that --stats
	// can name the directory it could not use.
	dir, _ := cache.Dir()
	c, err := open(cfg.model)
	// A cache that cannot be opened is not worth a word to the user: the
	// search is the same search, it just costs what it did last time, and a
	// warning repeated on every run would be noise about something nobody
	// asked for. Only --stats says so, where it was asked.
	if err != nil {
		return nil, cacheState{dir: dir}
	}
	return c, cacheState{dir: dir, on: true}
}

// walkOptions is which files a walk will search, as both the search and the
// dry run ask for it: a dry run that priced a different set of files than the
// run it is predicting would be worse than no dry run.
func walkOptions(cfg config) walk.Options {
	return walk.Options{
		Globs:    cfg.globs,
		Hidden:   cfg.hidden,
		NoIgnore: cfg.noIgnore,
	}
}

// attemptObserver is what the client tells the meter about every request it
// sends. Nil without --stats, so that a plain run carries no callback at all.
func attemptObserver(m *meter) func(jev.Attempt) {
	if m == nil {
		return nil
	}
	return m.attempt
}

// colorEnabled turns --color into the yes or no the printer wants. "auto" asks
// the terminal, and asks about stdout, which is where the escapes would go:
// output that is piped or redirected has to stay the bytes the next tool
// parses. "always" is for `| less -R`, where the pipe is a terminal after all.
func colorEnabled(env environment, when string) bool {
	switch when {
	case colorAlways:
		return true
	case colorNever:
		return false
	}
	return env.terminal.IsTerminal(apikey.Stdout)
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

// multipleInputs decides the default for -H, which output owns but cannot work
// out: only here is it known that an operand is a directory -r will expand.
//
// A directory operand counts as many inputs on its own, even when it holds one
// file. The decision has to be made before the first line is printed, and how
// many files a walk will turn up is exactly what the user did not know either
// -- which is grep's reasoning too, and its measured behavior.
func multipleInputs(cfg config, paths []string) bool {
	if len(paths) > 1 {
		return true
	}
	if !cfg.recursive {
		return false
	}
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// reader turns the PATH operands into the single stream of lines the searcher
// reads. A path that cannot be opened is reported and skipped: the other
// operands are still worth searching, and the run ends at exit code 2 anyway.
type reader struct {
	stdin  io.Reader
	log    *errorLog
	opened *opened
	// quotas is what -q, -l and -m stop this reader with: a file the output
	// side is done with is not read any further, and its remaining lines are
	// never sent to be scored.
	quotas *quotas
	// current is the input being read, as its place in the list of opened
	// ones. It is written and read on this goroutine only: search asks about a
	// line from inside the yield of that very line.
	current   int
	recursive bool
	walkOpts  walk.Options
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

// read streams one PATH operand, reporting whether the consumer wants more.
func (r *reader) read(path string, yield func(input.Line) bool) bool {
	if r.recursive && path != input.StdinPath {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return r.walkDir(path, yield)
		}
	}
	// An operand skips the filters that pick which files are worth searching --
	// hidden, ignored, secret, all of them: the user named this file, and that
	// is the one way to search one. The binary test still applies, because it
	// answers a different question: there is no line in a blob to send, and
	// paying to ship an executable byte by byte would also ship the binary form
	// of whatever it was built with.
	//
	// A failure to sniff is left unreported here on purpose; opening the file
	// below fails the same way, and that path is where a directory gets its own
	// message.
	if path != input.StdinPath {
		if binary, err := walk.IsBinary(path); err == nil && binary {
			// A notice, not an error: it does not change the exit code, because
			// the run did nothing wrong.
			r.log.notice("%s: binary file, skipped", path)
			return true
		}
	}
	return r.file(path, yield)
}

// walkDir searches every file under a directory operand. A directory that
// cannot be read is reported and the walk carries on, the same bargain an
// unreadable file gets.
func (r *reader) walkDir(dir string, yield func(input.Line) bool) bool {
	for path, err := range walk.Files(dir, r.walkOpts) {
		if err != nil {
			r.log.printf("%s", walkMessage(err))
			continue
		}
		if !r.file(path, yield) {
			return false
		}
	}
	return true
}

// file streams one file, or standard input.
func (r *reader) file(path string, yield func(input.Line) bool) bool {
	scanner, err := input.Open(path, r.stdin)
	if err != nil {
		r.log.printf("%s: %s", displayName(path), osMessage(err))
		return true
	}
	defer func() { _ = scanner.Close() }()

	// Listed as soon as it opens, before any line of it is read: a file with
	// no lines at all still has a count of zero to report and is still a file
	// that did not match.
	r.current = r.opened.add(displayName(path))

	for !r.quotas.stopped(r.current) {
		if !scanner.Scan() {
			break
		}
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
// memory that there was an error among them. It is guarded because the reader
// goroutine keeps running for a moment after Run returns on an interrupt, and
// it reports read errors from there.
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

// notice prints a line that is not a complaint about the run: something was
// skipped for a reason the user should know, and the exit code stays what it
// would have been.
func (l *errorLog) notice(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "jevgrep: "+format+"\n", a...)
}

// report prints a block that is neither a complaint nor part of the results:
// the --stats table, under the same "jevgrep: " prefix as everything else on
// stderr so that it cannot be mistaken for output. It takes the lock because
// the reader goroutine can still be reporting a read error as this is written.
func (l *errorLog) report(body string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprint(l.w, "jevgrep: stats\n"+body)
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
	// The half sentence grep does not have: someone who aimed jevgrep at a
	// directory wanted -r, and telling them the name of the option is cheaper
	// than making them look it up.
	case errors.Is(err, input.ErrIsDirectory):
		return input.ErrIsDirectory.Error() + " (use -r to search it)"
	case errors.As(err, &pathErr):
		return pathErr.Err.Error()
	}
	return err.Error()
}

// walkMessage names what the walk could not read and then the system's words
// for it. The error spells itself "open d/locked: permission denied"; an
// operand that fails is reported as "d/locked: permission denied", and one run
// should not have two shapes for the same thing.
func walkMessage(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Path + ": " + pathErr.Err.Error()
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
