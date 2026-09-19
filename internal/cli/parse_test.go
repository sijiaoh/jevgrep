package cli

import (
	"reflect"
	"testing"

	"github.com/sijiaoh/jevgrep/internal/output"
	"github.com/sijiaoh/jevgrep/internal/search"
)

// The parser is hand-written because flag cannot spell any of this. These are
// the spellings §2 promises, in the forms a user or an alias actually types.
func TestParse(t *testing.T) {
	tests := []struct {
		name string
		args []string
		// want changes the default config into the expected one, so that a
		// case names only what it is about.
		want func(*config)
	}{
		{
			name: "a meaning and a path",
			args: []string{"a failure", "app.log"},
			want: func(c *config) { c.terms = []search.Term{{Meaning: "a failure"}}; c.paths = []string{"app.log"} },
		},
		{
			name: "options may follow the operands",
			args: []string{"a failure", "app.log", "-n"},
			want: func(c *config) {
				c.terms = []search.Term{{Meaning: "a failure"}}
				c.paths = []string{"app.log"}
				c.lineNumber = true
			},
		},
		{
			name: "short options bundle up",
			args: []string{"-nvH", "a failure"},
			want: func(c *config) {
				c.terms = []search.Term{{Meaning: "a failure"}}
				c.lineNumber = true
				c.invert = true
				c.filenames = output.Always
			},
		},
		{
			name: "an argument may end a bundle",
			args: []string{"-nt0.7", "a failure"},
			want: func(c *config) {
				c.terms = []search.Term{{Meaning: "a failure"}}
				c.lineNumber = true
				c.threshold = 0.7
			},
		},
		{
			name: "an argument may be the next word",
			args: []string{"-nt", "0.7", "a failure"},
			want: func(c *config) {
				c.terms = []search.Term{{Meaning: "a failure"}}
				c.lineNumber = true
				c.threshold = 0.7
			},
		},
		{
			name: "a long option takes its argument either way",
			args: []string{"--threshold=0.7", "--model", "jev-2", "a failure"},
			want: func(c *config) { c.terms = []search.Term{{Meaning: "a failure"}}; c.threshold = 0.7; c.model = "jev-2" },
		},
		{
			name: "and and not attach to the meaning before them",
			args: []string{"-e", "cold", "--and", "morning", "--not", "rain", "-e", "warm", "--and", "evening"},
			want: func(c *config) {
				c.terms = []search.Term{
					{Meaning: "cold", And: []string{"morning"}, Not: []string{"rain"}},
					{Meaning: "warm", And: []string{"evening"}},
				}
			},
		},
		{
			name: "and attaches to a positional meaning too",
			args: []string{"cold", "--and", "morning", "app.log"},
			want: func(c *config) {
				c.terms = []search.Term{{Meaning: "cold", And: []string{"morning"}}}
				c.paths = []string{"app.log"}
			},
		},
		{
			name: "the later of -H and -h wins",
			args: []string{"-H", "-h", "a failure"},
			want: func(c *config) { c.terms = []search.Term{{Meaning: "a failure"}}; c.filenames = output.Never },
		},
		{
			name: "a lone dash is a path",
			args: []string{"a failure", "-"},
			want: func(c *config) { c.terms = []search.Term{{Meaning: "a failure"}}; c.paths = []string{"-"} },
		},
		{
			name: "nothing after -- is an option",
			args: []string{"-e", "a failure", "--", "-n", "--nope"},
			want: func(c *config) { c.terms = []search.Term{{Meaning: "a failure"}}; c.paths = []string{"-n", "--nope"} },
		},
		{
			name: "an option-looking word can still be the meaning",
			args: []string{"--", "-v", "app.log"},
			want: func(c *config) { c.terms = []search.Term{{Meaning: "-v"}}; c.paths = []string{"app.log"} },
		},
		{
			name: "an argument is never read as an option",
			args: []string{"-e", "--help", "app.log"},
			want: func(c *config) { c.terms = []search.Term{{Meaning: "--help"}}; c.paths = []string{"app.log"} },
		},
		{
			name: "the bounds of the threshold are inclusive",
			args: []string{"-t", "1", "-e", "a failure"},
			want: func(c *config) { c.terms = []search.Term{{Meaning: "a failure"}}; c.threshold = 1 },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := defaultConfig()
			tt.want(&want)

			got, err := parse(tt.args)

			if err != nil {
				t.Fatalf("parse(%q) failed: %v", tt.args, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("parse(%q) = %+v, want %+v", tt.args, got, want)
			}
		})
	}
}
