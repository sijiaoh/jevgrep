package jev

// Chunk is a run of consecutive input lines small enough to be scored in one
// request. How big a chunk is stays an implementation detail on purpose: §4
// rules out a chunk-size option for v0.1, so nothing outside this package gets
// to pick it.
type Chunk struct {
	// Offset is the index of Lines[0] in the slice that was split, so a caller
	// scoring chunks concurrently can put the probabilities back in order.
	Offset int
	Lines  []string
}

const (
	// Measured against the live API on 2026-09-20 with real source lines
	// (`make calibrate`, internal/calibration): a request takes about the
	// same time at 10, 30 and 60 lines -- medians across four runs of 0.31s,
	// 0.27-0.66s and 0.34-0.96s -- and only doubles at 120 (0.61-1.45s).
	// What does change the whole way is the price per line, because the fixed
	// overhead below is spread wider: 66, 50, 46 and 44 tokens per line.
	//
	// 60 is where that stops being free. It is 8.5% cheaper per line than 30
	// and halves the requests a search makes, which at Options.Concurrency 8
	// halves the wall clock of a large search too, while the first results
	// still arrive well inside the two seconds §10 asks for. 120 buys another
	// 3% for twice the latency, which is the wrong trade for a grep that
	// prints as it goes -- and a failed batch would take twice as many lines
	// down with it.
	//
	// The answers do not mind: the same lines score within about 0.1 of each
	// other alone, padded, reversed and in a full batch of unrelated source
	// lines -- and within 0.01-0.04 unless they were ambiguous to begin with
	// -- which is the spread two identical requests already have.
	maxLinesPerChunk = 60

	// The server caps a request at 64k tokens. We stay well under it because
	// estimateTokens is a guess, and because the cost of an over-cautious split
	// is one extra request while the cost of overshooting is a failed batch.
	chunkTokenBudget = 48000

	// Rough cost of the JSON scaffolding around one line: its id appears as a
	// key in both state and questions, plus the question's type field.
	perLineOverheadTokens = 12

	// What a request costs before a single line is in it: the model's own
	// preamble. Measured against the live API on 2026-09-19 -- one 16-byte
	// line was billed 286 input tokens and ten were billed 540, so around 258
	// of that is fixed and the rest is per line.
	//
	// Re-derived on 2026-09-20 with `make calibrate`, which is the reason the
	// number is trustworthy: fitted over batches of 1 to 60 identical lines
	// for meanings of 12, 67 and 527 bytes, the fixed part came out at 256
	// tokens every time. It really is per request, not per meaning, so
	// --dry-run does not have to grow a second term for long meanings.
	//
	// EstimateTokens counts it because a price that leaves it out reads too
	// cheap, which is the one direction a price must not be wrong in. Split
	// does not, deliberately: there the number only has to keep a request
	// under the server's cap, and the budget already holds 16k tokens of
	// headroom for exactly this kind of unknown.
	perRequestOverheadTokens = 258
)

// Split partitions lines into chunks, preserving order. Lines are never
// reordered or dropped: a line too large for the budget on its own still gets a
// chunk, because refusing to score it would silently lose a match.
func Split(meaning string, lines []string) []Chunk {
	if len(lines) == 0 {
		return nil
	}

	perLine := perLineTokens(meaning)

	var chunks []Chunk
	start, tokens := 0, 0
	for i, line := range lines {
		cost := perLine + estimateTokens(line)
		if i > start && (i-start >= maxLinesPerChunk || tokens+cost > chunkTokenBudget) {
			chunks = append(chunks, Chunk{Offset: start, Lines: lines[start:i]})
			start, tokens = i, 0
		}
		tokens += cost
	}
	return append(chunks, Chunk{Offset: start, Lines: lines[start:]})
}

// EstimateTokens approximates the input tokens one request costs: one chunk of
// lines, each asked meaning. It is what --dry-run prices and what --stats falls
// back to when the API does not report a count of its own, and it is the only
// estimate of its kind -- a second one elsewhere would sooner or later quote a
// different price for the same run.
func EstimateTokens(meaning string, lines []string) int {
	if len(lines) == 0 {
		return 0
	}
	perLine := perLineTokens(meaning)
	tokens := perRequestOverheadTokens
	for _, line := range lines {
		tokens += perLine + estimateTokens(line)
	}
	return tokens
}

// perLineTokens is what one line costs beyond its own text. The meaning is
// repeated in every question, so it is charged per line rather than once per
// request.
func perLineTokens(meaning string) int {
	return perLineOverheadTokens + estimateTokens(instructions(lineID(1), meaning))
}

// estimateTokens approximates the tokens a string costs, without pulling in a
// tokenizer for a number that only has to be in the right ballpark. Three bytes
// per token is about right for CJK (one 3-byte rune per token) and an
// over-estimate for English (~4 bytes per token), and erring high is the safe
// direction here.
func estimateTokens(s string) int {
	return len(s)/3 + 1
}
