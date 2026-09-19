package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sijiaoh/jevgrep/internal/jev"
	"github.com/sijiaoh/jevgrep/internal/output"
	"github.com/sijiaoh/jevgrep/internal/search"
)

// config is the command line as the rest of the package wants it.
type config struct {
	terms      []search.Term
	paths      []string
	threshold  float64
	invert     bool
	recursive  bool
	globs      []string
	hidden     bool
	noIgnore   bool
	lineNumber bool
	filenames  output.Filenames
	model      string

	// The three options that do something instead of searching. They are read
	// even when parsing failed, because --help has to work on a command line
	// that is otherwise wrong — that is usually why it is being asked for.
	help, version, login bool
}

// usageErrorf builds the first line of the usage error message. The errors
// parsing returns are all of this kind, which is why nothing here wraps an
// underlying error: there is none, only a sentence for the user.
func usageErrorf(format string, a ...any) error {
	return fmt.Errorf(format, a...)
}

// token is one command line word after the option syntax has been taken apart:
// an operand, or an option together with the argument it was given.
type token struct {
	// opt is nil for an operand.
	opt *Option
	// text is the operand, or the option's argument.
	text string
}

// parse turns the arguments into a config. It returns a config even when it
// fails, because the caller still has to honor --help / --version / --login.
func parse(args []string) (config, error) {
	cfg := config{threshold: defaultThreshold, model: jev.DefaultModel}

	tokens, err := tokenize(args)
	for _, t := range tokens {
		if t.opt == nil {
			continue
		}
		switch t.opt.Long {
		case "help":
			cfg.help = true
		case "version":
			cfg.version = true
		case "login":
			cfg.login = true
		}
	}
	if cfg.help || cfg.version || cfg.login || err != nil {
		return cfg, err
	}
	return interpret(cfg, tokens)
}

// tokenizer splits the arguments into operands and options with their
// arguments. It remembers the first problem it found but keeps going, so that
// a --help further along the command line is still seen.
type tokenizer struct {
	args   []string
	i      int
	tokens []token
	err    error
}

func tokenize(args []string) ([]token, error) {
	t := &tokenizer{args: args}
	for ; t.i < len(t.args); t.i++ {
		arg := t.args[t.i]
		switch {
		// Everything after "--" is an operand, however it is spelled.
		case arg == "--":
			for _, rest := range t.args[t.i+1:] {
				t.tokens = append(t.tokens, token{text: rest})
			}
			return t.tokens, t.err
		case strings.HasPrefix(arg, "--"):
			t.long(arg[2:])
		// A lone "-" is stdin, an operand. Anything longer is a cluster of
		// short options.
		case len(arg) > 1 && strings.HasPrefix(arg, "-"):
			t.cluster(arg[1:])
		default:
			t.tokens = append(t.tokens, token{text: arg})
		}
	}
	return t.tokens, t.err
}

func (t *tokenizer) fail(err error) {
	if t.err == nil {
		t.err = err
	}
}

func (t *tokenizer) long(arg string) {
	name, value, hasValue := strings.Cut(arg, "=")
	opt := longOption(name)
	switch {
	case opt == nil:
		// Echoed as the user wrote it: they are looking for their own typo,
		// not for our spelling of it.
		t.fail(usageErrorf("unknown option: --%s", name))
		return
	case opt.Arg == "" && hasValue:
		t.fail(usageErrorf("option --%s takes no argument", opt.Long))
		return
	case opt.Arg != "" && !hasValue:
		var ok bool
		if value, ok = t.next(opt); !ok {
			return
		}
	}
	t.tokens = append(t.tokens, token{opt: opt, text: value})
}

// cluster takes apart one bundle of short options, "-nt0.7" and the like.
func (t *tokenizer) cluster(rest string) {
	for rest != "" {
		r, size := utf8.DecodeRuneInString(rest)
		name := string(r)
		rest = rest[size:]

		opt := shortOption(name)
		switch {
		case opt == nil:
			t.fail(usageErrorf("unknown option: -%s", name))
			continue
		case opt.Arg == "":
			t.tokens = append(t.tokens, token{opt: opt})
			continue
		}

		// An option with an argument ends the cluster: the rest of it is the
		// argument, and if there is no rest, the next word is.
		value := rest
		if value == "" {
			var ok bool
			if value, ok = t.next(opt); !ok {
				return
			}
		}
		t.tokens = append(t.tokens, token{opt: opt, text: value})
		return
	}
}

// next takes the following argument as opt's value.
func (t *tokenizer) next(opt *Option) (string, bool) {
	if t.i+1 >= len(t.args) {
		t.fail(needsArgument(opt))
		return "", false
	}
	t.i++
	return t.args[t.i], true
}

// needsArgument names the option the way §5.1 does: by its canonical long
// name, whichever form the user typed, so one option has one message.
func needsArgument(opt *Option) error {
	return usageErrorf("option --%s needs an argument", opt.Long)
}

// interpret turns the tokens into the config, once it is known that this is a
// search and not --help / --version / --login.
func interpret(cfg config, tokens []token) (config, error) {
	// The first operand is the MEANING only when no -e was given anywhere,
	// including after it: "jevgrep foo -e bar" searches for bar in foo, the
	// same rule grep applies to its PATTERN.
	meaningWanted := true
	for _, t := range tokens {
		if t.opt != nil && t.opt.Long == "meaning" {
			meaningWanted = false
			break
		}
	}

	for _, t := range tokens {
		if t.opt == nil {
			if meaningWanted {
				cfg.terms = append(cfg.terms, search.Term{Meaning: t.text})
				meaningWanted = false
				continue
			}
			cfg.paths = append(cfg.paths, t.text)
			continue
		}

		switch t.opt.Long {
		case "meaning":
			cfg.terms = append(cfg.terms, search.Term{Meaning: t.text})
		case "and", "not":
			if len(cfg.terms) == 0 {
				return cfg, usageErrorf("--%s must follow a meaning", t.opt.Long)
			}
			last := &cfg.terms[len(cfg.terms)-1]
			if t.opt.Long == "and" {
				last.And = append(last.And, t.text)
			} else {
				last.Not = append(last.Not, t.text)
			}
		case "invert-match":
			cfg.invert = true
		case "threshold":
			n, err := strconv.ParseFloat(t.text, 64)
			// The range is checked here and not left to search.Compile so that
			// the message can quote what the user actually typed.
			if err != nil || !(n >= 0 && n <= 1) {
				return cfg, usageErrorf("--threshold: not a number between 0 and 1: %q", t.text)
			}
			cfg.threshold = n
		case "recursive":
			cfg.recursive = true
		case "glob":
			cfg.globs = append(cfg.globs, t.text)
		case "hidden":
			cfg.hidden = true
		case "no-ignore":
			cfg.noIgnore = true
		case "line-number":
			cfg.lineNumber = true
		// Later wins, so that a -h in an alias can be undone by a -H on the
		// command line.
		case "with-filename":
			cfg.filenames = output.Always
		case "no-filename":
			cfg.filenames = output.Never
		case "model":
			cfg.model = t.text
		}
	}
	return cfg, nil
}

// compileError translates what search.Compile rejects into §5.1 wording. The
// checks live in search, which is where the expression is built; only the
// sentence is the CLI's.
func compileError(err error) error {
	switch {
	case errors.Is(err, search.ErrNoTerms):
		return usageErrorf("missing MEANING")
	case errors.Is(err, search.ErrEmptyMeaning):
		return usageErrorf("empty MEANING")
	}
	return err
}
