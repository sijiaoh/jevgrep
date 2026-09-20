package jev

import (
	"math"
	"strings"
	"testing"
)

func TestCostUSDFollowsThePrice(t *testing.T) {
	if got := CostUSD(0); got != 0 {
		t.Errorf("CostUSD(0) = %v, want 0", got)
	}
	if got, want := CostUSD(1_000_000), PriceUSDPerMTokInput; math.Abs(got-want) > 1e-12 {
		t.Errorf("CostUSD(1M) = %v, want the price of one Mtok, %v", got, want)
	}
	if got, want := CostUSD(500_000), PriceUSDPerMTokInput/2; math.Abs(got-want) > 1e-12 {
		t.Errorf("CostUSD(500k) = %v, want %v", got, want)
	}
}

func TestEstimateTokensChargesTheRequestAndEveryLine(t *testing.T) {
	if got := EstimateTokens("an error", nil); got != 0 {
		t.Errorf("EstimateTokens of no lines = %d, want 0: a request that is never sent costs nothing", got)
	}

	one := EstimateTokens("an error", []string{"x"})
	two := EstimateTokens("an error", []string{"x", "x"})
	if one <= 0 || two <= one {
		t.Errorf("EstimateTokens = %d for one line and %d for two, want a positive number that grows", one, two)
	}
	// The scaffolding of a request is charged once and the meaning once per
	// line, which is what makes a longer meaning cost more on every line.
	if long := EstimateTokens(strings.Repeat("an error ", 20), []string{"x", "x"}); long <= two {
		t.Errorf("a longer meaning estimated %d for two lines, want more than %d", long, two)
	}
}

// The estimate is what --dry-run quotes a price from, so it is worth holding
// against what the API actually charged. These three are measurements, taken
// against the live API on 2026-09-19 with the request jevgrep itself sends.
//
// The bound is deliberately one-sided and loose: an estimate that comes out
// high makes a user expect to pay more than they do, and an estimate that
// comes out low is a quote that was wrong in the one direction that matters.
func TestEstimateTokensMatchesWhatTheAPICharged(t *testing.T) {
	line := "the disk is full"
	long := strings.Repeat(line, 10)[:160]

	tests := []struct {
		name    string
		lines   []string
		charged int
	}{
		{name: "one short line", lines: []string{line}, charged: 286},
		{name: "ten short lines", lines: repeat(line, 10), charged: 540},
		{name: "ten long lines", lines: repeat(long, 10), charged: 900},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens("a disk error", tt.lines)
			if got < tt.charged*3/4 {
				t.Errorf("estimated %d tokens, want no less than three quarters of the %d charged", got, tt.charged)
			}
			if got > 2*tt.charged {
				t.Errorf("estimated %d tokens, want no more than twice the %d charged", got, tt.charged)
			}
		})
	}
}
