// Package search turns a stream of input lines into the lines that match an
// expression: it schedules the scoring of every line, in parallel, and reports
// the matches in the order the lines were read.
package search

import (
	"errors"
	"slices"
)

// Term is one MEANING together with the --and / --not that were attached to it
// on the command line. A line satisfies a term when it means Meaning and
// everything in And, and none of what is in Not.
type Term struct {
	Meaning string
	And     []string
	Not     []string
}

// Compile's errors. The command line rejects all three with its own wording
// before it ever gets here (a MEANING is required, --and must follow one, the
// threshold is validated on parse); they exist so that a bug upstream fails
// loudly instead of quietly searching for something else.
var (
	ErrNoTerms      = errors.New("search: no meaning to search for")
	ErrEmptyMeaning = errors.New("search: empty meaning")
	ErrBadThreshold = errors.New("search: threshold is not between 0 and 1")
)

// Expr is a compiled expression: terms OR'd together, optionally inverted.
type Expr struct {
	// meanings holds each distinct meaning once, in first-seen order. Terms
	// refer to meanings by index, so a meaning repeated across terms is scored
	// once per line instead of once per mention -- every score is a paid
	// request.
	meanings []string
	terms    []term

	threshold float64
	invert    bool
}

// term is one Term with its meanings resolved to indices into Expr.meanings.
type term struct {
	// all must score at or above the threshold, none must score below it.
	all  []int
	none []int
}

// Compile builds the expression the terms describe. threshold is the score at
// which a meaning counts as present, and invert is -v, which flips the result
// of the whole expression and nothing smaller.
func Compile(terms []Term, threshold float64, invert bool) (*Expr, error) {
	if len(terms) == 0 {
		return nil, ErrNoTerms
	}
	// Written as a negation so that a NaN threshold, which compares false
	// against everything, is rejected rather than accepted.
	if !(threshold >= 0 && threshold <= 1) {
		return nil, ErrBadThreshold
	}

	e := &Expr{threshold: threshold, invert: invert}
	index := make(map[string]int)
	intern := func(meaning string) (int, error) {
		if meaning == "" {
			return 0, ErrEmptyMeaning
		}
		if i, ok := index[meaning]; ok {
			return i, nil
		}
		index[meaning] = len(e.meanings)
		e.meanings = append(e.meanings, meaning)
		return len(e.meanings) - 1, nil
	}

	for _, t := range terms {
		var compiled term
		for _, meaning := range append([]string{t.Meaning}, t.And...) {
			i, err := intern(meaning)
			if err != nil {
				return nil, err
			}
			compiled.all = append(compiled.all, i)
		}
		for _, meaning := range t.Not {
			i, err := intern(meaning)
			if err != nil {
				return nil, err
			}
			compiled.none = append(compiled.none, i)
		}
		e.terms = append(e.terms, compiled)
	}

	return e, nil
}

// Meanings are the distinct meanings a line has to be scored against, in the
// order Match expects their scores.
func (e *Expr) Meanings() []string { return slices.Clone(e.meanings) }

// Match reports whether a line whose scores are these is selected. scores must
// have one entry per Meanings, in that order; a line that was never sent to the
// model (an empty one) scores 0 for every meaning, which is what makes it never
// match and always be selected under -v.
func (e *Expr) Match(scores []float64) bool {
	matched := false
	for _, t := range e.terms {
		if t.holds(scores, e.threshold) {
			matched = true
			break
		}
	}
	return matched != e.invert
}

func (t term) holds(scores []float64, threshold float64) bool {
	for _, i := range t.all {
		if scores[i] < threshold {
			return false
		}
	}
	for _, i := range t.none {
		if scores[i] >= threshold {
			return false
		}
	}
	return true
}

// Headline reduces a line's scores to the single number -p prints and --json
// reports as "score": the highest of the head meanings' scores, one per OR
// branch. The --and and --not meanings are left out because they qualify a
// branch rather than being what the user asked to see, and -v does not change
// it either -- inverting happens to the verdict, not to what the model
// measured. Anyone who needs every number asks for --json, which is what it is
// for.
//
// scores must have one entry per Meanings, like Match's.
func (e *Expr) Headline(scores []float64) float64 {
	// Compile refuses an expression with no term, so there is always a head
	// meaning to report.
	best := scores[e.terms[0].all[0]]
	for _, t := range e.terms[1:] {
		best = max(best, scores[t.all[0]])
	}
	return best
}
