package search

import (
	"errors"
	"math"
	"slices"
	"testing"
)

func TestCompileRejectsExpressionsItCannotSearchFor(t *testing.T) {
	tests := []struct {
		name      string
		terms     []Term
		threshold float64
		want      error
	}{
		{"no term", nil, 0.5, ErrNoTerms},
		{"empty meaning", []Term{{Meaning: ""}}, 0.5, ErrEmptyMeaning},
		{"empty --and", []Term{{Meaning: "a", And: []string{""}}}, 0.5, ErrEmptyMeaning},
		{"empty --not", []Term{{Meaning: "a", Not: []string{""}}}, 0.5, ErrEmptyMeaning},
		{"threshold below 0", []Term{{Meaning: "a"}}, -0.1, ErrBadThreshold},
		{"threshold above 1", []Term{{Meaning: "a"}}, 1.1, ErrBadThreshold},
		{"threshold NaN", []Term{{Meaning: "a"}}, math.NaN(), ErrBadThreshold},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Compile(tt.terms, tt.threshold, false); !errors.Is(err, tt.want) {
				t.Errorf("Compile() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCompileAcceptsTheClosedThresholdRange(t *testing.T) {
	for _, threshold := range []float64{0, 1} {
		if _, err := Compile([]Term{{Meaning: "a"}}, threshold, false); err != nil {
			t.Errorf("Compile(threshold=%v) = %v, want no error", threshold, err)
		}
	}
}

func TestMeaningsAreDeduplicated(t *testing.T) {
	// Every mention of a meaning would otherwise be a paid request per line.
	e := mustCompile(t, []Term{
		{Meaning: "error", And: []string{"database"}},
		{Meaning: "timeout", Not: []string{"error"}},
	}, 0.5, false)

	if got, want := e.Meanings(), []string{"error", "database", "timeout"}; !slices.Equal(got, want) {
		t.Errorf("Meanings() = %q, want %q", got, want)
	}
}

func TestMeaningsCannotBeChangedThroughTheReturnedSlice(t *testing.T) {
	e := mustCompile(t, []Term{{Meaning: "error"}}, 0.5, false)
	e.Meanings()[0] = "something else"

	if got := e.Meanings()[0]; got != "error" {
		t.Errorf("Meanings()[0] = %q, want %q", got, "error")
	}
}

func TestMatch(t *testing.T) {
	terms := []Term{
		{Meaning: "error", And: []string{"database"}},
		{Meaning: "timeout"},
	}

	tests := []struct {
		name      string
		terms     []Term
		threshold float64
		invert    bool
		scores    map[string]float64
		want      bool
	}{
		{
			name:   "a single meaning at the threshold matches",
			terms:  []Term{{Meaning: "error"}},
			scores: map[string]float64{"error": 0.5},
			want:   true,
		},
		{
			name:   "a single meaning just below the threshold does not",
			terms:  []Term{{Meaning: "error"}},
			scores: map[string]float64{"error": 0.4999},
			want:   false,
		},
		{
			name:   "--and needs both meanings",
			terms:  terms,
			scores: map[string]float64{"error": 0.9, "database": 0.9},
			want:   true,
		},
		{
			name:   "--and fails when only one holds",
			terms:  []Term{terms[0]},
			scores: map[string]float64{"error": 0.9, "database": 0.1},
			want:   false,
		},
		{
			name:   "a second -e is an OR",
			terms:  terms,
			scores: map[string]float64{"error": 0.1, "database": 0.1, "timeout": 0.9},
			want:   true,
		},
		{
			name:   "no term holding is no match",
			terms:  terms,
			scores: nil,
			want:   false,
		},
		{
			name:   "--not holds below the threshold",
			terms:  []Term{{Meaning: "error", Not: []string{"timeout"}}},
			scores: map[string]float64{"error": 0.9, "timeout": 0.1},
			want:   true,
		},
		{
			name:   "--not fails at the threshold",
			terms:  []Term{{Meaning: "error", Not: []string{"timeout"}}},
			scores: map[string]float64{"error": 0.9, "timeout": 0.5},
			want:   false,
		},
		{
			name:   "--not alone does not make a line match",
			terms:  []Term{{Meaning: "error", Not: []string{"timeout"}}},
			scores: map[string]float64{"error": 0.1, "timeout": 0.1},
			want:   false,
		},
		{
			name:      "a lower threshold accepts a weaker score",
			terms:     []Term{{Meaning: "error"}},
			threshold: 0.2,
			scores:    map[string]float64{"error": 0.3},
			want:      true,
		},
		{
			name:      "a higher threshold rejects it",
			terms:     []Term{{Meaning: "error"}},
			threshold: 0.95,
			scores:    map[string]float64{"error": 0.9},
			want:      false,
		},
		{
			name:   "-v inverts a match",
			terms:  terms,
			invert: true,
			scores: map[string]float64{"error": 0.9, "database": 0.9},
			want:   false,
		},
		{
			name:   "-v inverts the whole expression, not each term",
			terms:  terms,
			invert: true,
			// The first term fails and the second holds: inverting per term
			// would select this line, inverting the OR does not.
			scores: map[string]float64{"error": 0.1, "timeout": 0.9},
			want:   false,
		},
		{
			name:   "-v selects a line no term holds for",
			terms:  terms,
			invert: true,
			scores: nil,
			want:   true,
		},
		{
			name:   "-v selects an unsent line, which scores zero throughout",
			terms:  []Term{{Meaning: "error", Not: []string{"timeout"}}},
			invert: true,
			scores: nil,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			threshold := tt.threshold
			if threshold == 0 {
				threshold = 0.5
			}
			e := mustCompile(t, tt.terms, threshold, tt.invert)

			meanings := e.Meanings()
			scores := make([]float64, len(meanings))
			for i, meaning := range meanings {
				scores[i] = tt.scores[meaning]
			}

			if got := e.Match(scores); got != tt.want {
				t.Errorf("Match(%v) = %v, want %v", scores, got, tt.want)
			}
		})
	}
}

func mustCompile(t *testing.T, terms []Term, threshold float64, invert bool) *Expr {
	t.Helper()
	e, err := Compile(terms, threshold, invert)
	if err != nil {
		t.Fatalf("Compile() = %v", err)
	}
	return e
}

// Headline is the one number -p prints, and which of the scores it comes from
// is the whole of what it decides.
func TestHeadlineIsTheBestOfTheHeadMeanings(t *testing.T) {
	tests := []struct {
		name   string
		terms  []Term
		invert bool
		scores []float64
		want   float64
	}{
		{
			name:   "one meaning is its own headline",
			terms:  []Term{{Meaning: "a"}},
			scores: []float64{0.91},
			want:   0.91,
		},
		{
			name:   "two -e report the higher one",
			terms:  []Term{{Meaning: "a"}, {Meaning: "b"}},
			scores: []float64{0.2, 0.7},
			want:   0.7,
		},
		{
			name:   "whichever order they came in",
			terms:  []Term{{Meaning: "a"}, {Meaning: "b"}},
			scores: []float64{0.7, 0.2},
			want:   0.7,
		},
		{
			// --and and --not qualify a branch; they are not what the user
			// asked to see, and --json is where every number is.
			name:   "--and and --not are left out",
			terms:  []Term{{Meaning: "a", And: []string{"b"}, Not: []string{"c"}}},
			scores: []float64{0.4, 0.99, 0.98},
			want:   0.4,
		},
		{
			// -v turns the verdict over, not the measurement behind it.
			name:   "-v does not change it",
			terms:  []Term{{Meaning: "a"}},
			invert: true,
			scores: []float64{0.91},
			want:   0.91,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := Compile(tt.terms, 0.5, tt.invert)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if got := expr.Headline(tt.scores); got != tt.want {
				t.Errorf("Headline = %v, want %v", got, tt.want)
			}
		})
	}
}
