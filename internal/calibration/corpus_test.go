package calibration

import (
	"strings"
	"testing"
	"unicode"
)

// The corpus is data a human wrote, so what can be checked about it offline is
// that it is still the shape the calibration run assumes: balanced, labelled,
// and above all still asking meanings of lines in other languages. These run in
// make check and touch nothing outside this package.

func TestEveryCaseIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Cases() {
		switch {
		case c.Text == "":
			t.Errorf("a case for %q has no text", c.Meaning)
		case c.Meaning == "":
			t.Errorf("the case %q has no meaning", c.Text)
		case c.Lang != "en" && c.Lang != "ja" && c.Lang != "zh":
			t.Errorf("case %q: unknown language %q", c.Text, c.Lang)
		case strings.ContainsFunc(c.Text, func(r rune) bool { return r == '\n' || r == '\r' }):
			// input.Line hands the scorer one line at a time; a case with a
			// newline in it would be scoring something no run can produce.
			t.Errorf("case %q spans more than one line", c.Text)
		}
		key := c.Meaning + "\x00" + c.Text
		if seen[key] {
			t.Errorf("the case (%q, %q) appears twice, which would weight it double", c.Text, c.Meaning)
		}
		seen[key] = true
	}
}

func TestBothClassesAreWellRepresented(t *testing.T) {
	var match, nonMatch int
	for _, c := range Cases() {
		if c.Match {
			match++
			continue
		}
		nonMatch++
	}
	// Not an even split, just no landslide: a corpus that is 90% non-matches
	// makes "select nothing" look like a good threshold.
	if min(match, nonMatch)*2 < max(match, nonMatch) {
		t.Errorf("corpus is lopsided: %d matches, %d non-matches", match, nonMatch)
	}
}

func TestCrossLanguageRetrievalIsCovered(t *testing.T) {
	pairs := map[string]int{}
	var cross int
	for _, c := range Cases() {
		if !c.Cross() {
			continue
		}
		cross++
		pairs[MeaningLang(c.Meaning)+"->"+c.Lang]++
	}

	if cross*3 < len(Cases()) {
		t.Errorf("only %d of %d cases are cross-language; that is what jevgrep is for", cross, len(Cases()))
	}
	// The two directions the task names explicitly, plus their mirrors: a
	// threshold that works in one direction and not the other is exactly the
	// failure a single-language corpus would hide.
	for _, want := range []string{"zh->en", "en->ja", "ja->en", "en->zh"} {
		if pairs[want] == 0 {
			t.Errorf("no case searches %s", want)
		}
	}
}

func TestEveryMeaningIsAskedOfBothClasses(t *testing.T) {
	type counts struct{ match, nonMatch, boundary int }
	byMeaning := map[string]*counts{}
	for _, c := range Cases() {
		got := byMeaning[c.Meaning]
		if got == nil {
			got = &counts{}
			byMeaning[c.Meaning] = got
		}
		switch {
		case c.Match:
			got.match++
		default:
			got.nonMatch++
		}
		if c.Boundary {
			got.boundary++
		}
	}

	for meaning, got := range byMeaning {
		// A meaning with matches only measures nothing: every threshold below
		// the lowest score gets it perfectly right.
		if got.match == 0 || got.nonMatch == 0 {
			t.Errorf("%q has %d matches and %d non-matches; a meaning needs both", meaning, got.match, got.nonMatch)
		}
		// The band around the threshold is where the default is decided, so
		// every meaning has to reach into it.
		if got.boundary == 0 {
			t.Errorf("%q has no boundary case", meaning)
		}
	}
}

func TestEveryBoundaryCaseSaysWhyItIsLabelledThatWay(t *testing.T) {
	// A boundary case is one a reader will want to argue with. The note is
	// what they argue with, instead of with the label alone.
	for _, c := range Cases() {
		if c.Boundary && c.Note == "" {
			t.Errorf("boundary case %q has no note", c.Text)
		}
	}
}

func TestTheCorpusStaysSmallEnoughToRerun(t *testing.T) {
	// §11 says calibration has to be redone after a model update, and the only
	// corpus that gets redone is one that is cheap. Five hundred questions
	// would still be under a cent, but it is a size nobody re-reads either.
	if n := len(Cases()); n > 120 {
		t.Errorf("corpus has grown to %d cases; keep it re-runnable", n)
	}
}

func TestCasesAreNotPaddedWithWhitespace(t *testing.T) {
	// Leading tabs are deliberate on the source-code cases -- that is how the
	// line appears in a file -- but a trailing space is a typo, and it is
	// billed and scored like any other byte.
	for _, c := range Cases() {
		if strings.TrimRightFunc(c.Text, unicode.IsSpace) != c.Text {
			t.Errorf("case %q has trailing whitespace", c.Text)
		}
		if strings.TrimSpace(c.Meaning) != c.Meaning {
			t.Errorf("meaning %q has surrounding whitespace", c.Meaning)
		}
	}
}

func TestCasesIsACopy(t *testing.T) {
	got := Cases()
	first := got[0]
	got[0] = Case{Text: "clobbered"}
	if Cases()[0] != first {
		t.Fatal("Cases handed out the corpus itself; a caller that reorders it would reorder the corpus")
	}
}

// MeaningLang is a switch over the meanings by name, so a meaning added
// without a case there is silently English -- and a Japanese meaning read as
// English turns every cross-language case that uses it into a same-language
// one, quietly emptying out the property this corpus exists to measure. The
// script the meaning is written in is the check that catches it.
func TestEveryMeaningIsFiledUnderTheLanguageItIsWrittenIn(t *testing.T) {
	for _, c := range Cases() {
		var kana, han bool
		for _, r := range c.Meaning {
			switch {
			case unicode.In(r, unicode.Hiragana, unicode.Katakana):
				kana = true
			case unicode.Is(unicode.Han, r):
				han = true
			}
		}
		want := "en"
		switch {
		case kana:
			want = "ja"
		case han:
			want = "zh"
		}
		if got := MeaningLang(c.Meaning); got != want {
			t.Errorf("MeaningLang(%q) = %q, want %q by the script it is written in", c.Meaning, got, want)
		}
	}
}
