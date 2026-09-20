package calibration

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
)

// Scored is a corpus case and the probability the model gave it.
type Scored struct {
	Case
	Score float64
}

// Row is what one threshold would have decided about a set of scored cases.
// Counts, not rates, are the stored form: rates are derived, and a reader who
// distrusts a rate can check the four numbers it came from.
type Row struct {
	Threshold      float64
	TP, FP, FN, TN int
}

// Precision is the share of selected cases that should have been selected.
// A threshold that selects nothing scores 0 rather than "undefined": it found
// none of what the user asked for, which is the same failure as finding only
// wrong things, and a sweep table with a hole in it is harder to read than one
// that says so.
func (r Row) Precision() float64 { return ratio(r.TP, r.TP+r.FP) }

// Recall is the share of the lines that should have been selected that were.
func (r Row) Recall() float64 { return ratio(r.TP, r.TP+r.FN) }

// F1 is the harmonic mean of precision and recall. It is the number the
// default threshold is picked by, because a grep is wrong in both directions
// at once: a missed line is a line the user will never know about, and a spare
// line is one they have to read and pay for.
func (r Row) F1() float64 {
	p, rec := r.Precision(), r.Recall()
	if p+rec == 0 {
		return 0
	}
	return 2 * p * rec / (p + rec)
}

// Accuracy is the share of all cases decided correctly. It is reported next to
// F1 because a corpus with more non-matches than matches can look excellent by
// accuracy while missing half of what was asked for.
func (r Row) Accuracy() float64 { return ratio(r.TP+r.TN, r.TP+r.FP+r.FN+r.TN) }

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// Thresholds is the grid a sweep walks: every 0.05 from 0.05 to 0.95. Finer
// than that would report differences smaller than the run-to-run jitter the
// online test measures, which is a false precision.
func Thresholds() []float64 {
	out := make([]float64, 0, 19)
	for i := 1; i <= 19; i++ {
		out = append(out, float64(i)/20)
	}
	return out
}

// Sweep counts what each threshold would have decided. A line matches at a
// score of t or above, which is exactly what -t means (search.term.holds), so
// the sweep and the binary cannot disagree about the boundary itself.
func Sweep(scored []Scored, thresholds []float64) []Row {
	rows := make([]Row, 0, len(thresholds))
	for _, t := range thresholds {
		row := Row{Threshold: t}
		for _, s := range scored {
			switch selected := s.Score >= t; {
			case selected && s.Match:
				row.TP++
			case selected && !s.Match:
				row.FP++
			case !selected && s.Match:
				row.FN++
			default:
				row.TN++
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// Best returns the row with the highest F1, breaking a tie towards the middle
// of the range. The tie-break is the point: on a small corpus several
// thresholds share the top score, and the one furthest from both ends is the
// one with the most room before the next model update moves the scores.
func Best(rows []Row) Row {
	var best Row
	for i, r := range rows {
		switch {
		case i == 0, r.F1() > best.F1():
			best = r
		case r.F1() == best.F1() && math.Abs(r.Threshold-0.5) < math.Abs(best.Threshold-0.5):
			best = r
		}
	}
	return best
}

// Separation is the range of thresholds that get every case right: the lowest
// score given to a match and the highest given to a non-match. When low > high
// any threshold in between is perfect and the corpus cannot choose among them;
// when low <= high the two classes overlap and no threshold is, which is the
// interesting case and the reason the corpus has boundary cases at all.
func Separation(scored []Scored) (lowestMatch, highestNonMatch float64) {
	lowestMatch, highestNonMatch = math.NaN(), math.NaN()
	for _, s := range scored {
		if s.Match {
			if math.IsNaN(lowestMatch) || s.Score < lowestMatch {
				lowestMatch = s.Score
			}
			continue
		}
		if math.IsNaN(highestNonMatch) || s.Score > highestNonMatch {
			highestNonMatch = s.Score
		}
	}
	return lowestMatch, highestNonMatch
}

// Only returns the cases keep accepts, so a sweep can be repeated over a
// subset -- the cross-language cases, the boundary ones -- without a second
// copy of the sweep.
func Only(scored []Scored, keep func(Case) bool) []Scored {
	var out []Scored
	for _, s := range scored {
		if keep(s.Case) {
			out = append(out, s)
		}
	}
	return out
}

// Report renders a sweep as a table for a human to read in the test log. It is
// the output the default threshold is argued from, so it prints the counts as
// well as the rates.
func Report(title string, rows []Row) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", title)
	fmt.Fprintf(&b, "  %9s %4s %4s %4s %4s %10s %7s %7s %9s\n",
		"threshold", "TP", "FP", "FN", "TN", "precision", "recall", "F1", "accuracy")
	for _, r := range rows {
		fmt.Fprintf(&b, "  %9.2f %4d %4d %4d %4d %10.3f %7.3f %7.3f %9.3f\n",
			r.Threshold, r.TP, r.FP, r.FN, r.TN, r.Precision(), r.Recall(), r.F1(), r.Accuracy())
	}
	return b.String()
}

// Mistakes returns the cases a threshold decides wrongly, furthest from the
// threshold first. That is the other half of the argument: a rate says how
// often, and only the cases themselves say whether being wrong there matters
// -- a match missed by 0.01 is a tuning question, one missed by 0.6 is a case
// the model disagrees with us about.
func Mistakes(scored []Scored, threshold float64) []Scored {
	var out []Scored
	for _, s := range scored {
		if (s.Score >= threshold) != s.Match {
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b Scored) int {
		return cmp.Compare(math.Abs(b.Score-threshold), math.Abs(a.Score-threshold))
	})
	return out
}
