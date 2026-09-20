package jev

// PriceUSDPerMTokInput is what TypeSafe charges for a million input tokens.
// Output tokens are free. Checked against https://docs.typesafe.ai/models on
// 2026-09-19, where it is quoted as $42 per Btok / $0.042 per Mtok.
//
// The number lives here and nowhere else: --dry-run, --stats and the JSON
// record all render this one constant, so a price change is one edit and
// nothing can go on quoting the old one.
const PriceUSDPerMTokInput = 0.042

// tokensPerMTok is the "M" in the price.
const tokensPerMTok = 1e6

// CostUSD is what n input tokens cost.
func CostUSD(n int) float64 {
	return float64(n) * PriceUSDPerMTokInput / tokensPerMTok
}
