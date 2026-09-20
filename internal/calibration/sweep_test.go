package calibration

import (
	"math"
	"strings"
	"testing"
)

// scoredFor builds cases with given scores, half labelled match. Only Score
// and Match matter to the sweep.
func scoredFor(t *testing.T, pairs ...any) []Scored {
	t.Helper()
	if len(pairs)%2 != 0 {
		t.Fatal("want score, match pairs")
	}
	var out []Scored
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, Scored{
			Case:  Case{Text: "line", Meaning: "meaning", Match: pairs[i+1].(bool)},
			Score: pairs[i].(float64),
		})
	}
	return out
}

func TestTheThresholdItselfSelects(t *testing.T) {
	// -t is "score of NUM or above" (search.term.holds uses >=). A sweep that
	// used > would report a different table than the binary produces.
	rows := Sweep(scoredFor(t, 0.5, true), []float64{0.5})
	if rows[0].TP != 1 {
		t.Fatalf("a score exactly at the threshold must match: %+v", rows[0])
	}
}

func TestTheCountsAndRatesAgree(t *testing.T) {
	scored := scoredFor(t,
		0.9, true, // TP
		0.8, true, // TP
		0.2, true, // FN
		0.7, false, // FP
		0.1, false, // TN
		0.05, false, // TN
	)
	rows := Sweep(scored, []float64{0.5})
	got := rows[0]

	if got.TP != 2 || got.FP != 1 || got.FN != 1 || got.TN != 2 {
		t.Fatalf("counts: %+v", got)
	}
	want := map[string]float64{
		"precision": 2.0 / 3,
		"recall":    2.0 / 3,
		"F1":        2.0 / 3,
		"accuracy":  4.0 / 6,
	}
	for name, get := range map[string]float64{
		"precision": got.Precision(), "recall": got.Recall(),
		"F1": got.F1(), "accuracy": got.Accuracy(),
	} {
		if math.Abs(get-want[name]) > 1e-9 {
			t.Errorf("%s = %v, want %v", name, get, want[name])
		}
	}
}

func TestSelectingNothingIsNotPerfectPrecision(t *testing.T) {
	rows := Sweep(scoredFor(t, 0.1, true, 0.2, false), []float64{0.9})
	got := rows[0]
	if got.Precision() != 0 || got.F1() != 0 {
		t.Fatalf("a threshold that selects nothing scored %v precision, %v F1", got.Precision(), got.F1())
	}
	// It still gets the non-match right, and accuracy is the number that says
	// so. Reporting both is the reason they are both in the table.
	if got.Accuracy() != 0.5 {
		t.Fatalf("accuracy = %v, want 0.5", got.Accuracy())
	}
}

func TestBestPrefersTheMiddleOfATie(t *testing.T) {
	// One match at 0.9, one non-match at 0.1: every threshold in between is
	// perfect, and the one with the most headroom on both sides wins.
	rows := Sweep(scoredFor(t, 0.9, true, 0.1, false), Thresholds())
	best := Best(rows)
	if best.F1() != 1 {
		t.Fatalf("best F1 = %v, want 1", best.F1())
	}
	if math.Abs(best.Threshold-0.5) > 1e-9 {
		t.Fatalf("best threshold = %v, want the middle of the tie", best.Threshold)
	}
}

func TestSeparationSaysWhetherTheClassesOverlap(t *testing.T) {
	low, high := Separation(scoredFor(t, 0.9, true, 0.7, true, 0.3, false))
	if low != 0.7 || high != 0.3 {
		t.Fatalf("separation = (%v, %v), want (0.7, 0.3)", low, high)
	}
	if !(low > high) {
		t.Fatal("these classes do not overlap; low must be above high")
	}

	low, high = Separation(scoredFor(t, 0.4, true, 0.6, false))
	if !(low <= high) {
		t.Fatalf("overlapping classes reported as separated: (%v, %v)", low, high)
	}
}

func TestSeparationOfOneSidedInputIsNotANumber(t *testing.T) {
	// A subset with no non-matches has no highest non-match, and reporting 0
	// for it would read as "a non-match scored zero".
	low, high := Separation(scoredFor(t, 0.4, true))
	if math.IsNaN(low) || !math.IsNaN(high) {
		t.Fatalf("separation = (%v, %v), want (0.4, NaN)", low, high)
	}
}

func TestOnlyKeepsTheSubsetAsked(t *testing.T) {
	scored := []Scored{
		{Case: Case{Text: "a", Boundary: true}, Score: 0.9},
		{Case: Case{Text: "b"}, Score: 0.1},
	}
	got := Only(scored, func(c Case) bool { return c.Boundary })
	if len(got) != 1 || got[0].Text != "a" {
		t.Fatalf("Only returned %+v", got)
	}
}

func TestMistakesComeWorstFirst(t *testing.T) {
	scored := scoredFor(t,
		0.45, true, // missed by 0.05
		0.05, true, // missed by 0.45
		0.95, false, // selected by 0.45 the other way
		0.9, true, // right, must not appear
	)
	got := Mistakes(scored, 0.5)
	if len(got) != 3 {
		t.Fatalf("got %d mistakes, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if math.Abs(got[i-1].Score-0.5) < math.Abs(got[i].Score-0.5) {
			t.Fatalf("mistakes are not ordered by distance: %v", got)
		}
	}
}

func TestReportPrintsEveryThresholdWithItsCounts(t *testing.T) {
	rows := Sweep(scoredFor(t, 0.9, true, 0.1, false), Thresholds())
	out := Report("corpus", rows)

	if !strings.HasPrefix(out, "corpus\n") {
		t.Errorf("report does not start with its title:\n%s", out)
	}
	if got := strings.Count(out, "\n"); got != len(rows)+2 {
		t.Errorf("report has %d lines, want a title, a header and %d rows", got, len(rows))
	}
	if !strings.Contains(out, "precision") || !strings.Contains(out, "0.50") {
		t.Errorf("report is missing its header or its rows:\n%s", out)
	}
}
