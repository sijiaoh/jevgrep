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
	// Measured against the live API (§2): around 30 lines answer in about a
	// second, which is the latency budget for "first results in 2 seconds".
	maxLinesPerChunk = 30

	// The server caps a request at 64k tokens. We stay well under it because
	// estimateTokens is a guess, and because the cost of an over-cautious split
	// is one extra request while the cost of overshooting is a failed batch.
	chunkTokenBudget = 48000

	// Rough cost of the JSON scaffolding around one line: its id appears as a
	// key in both state and questions, plus the question's type field.
	perLineOverheadTokens = 12
)

// Split partitions lines into chunks, preserving order. Lines are never
// reordered or dropped: a line too large for the budget on its own still gets a
// chunk, because refusing to score it would silently lose a match.
func Split(meaning string, lines []string) []Chunk {
	if len(lines) == 0 {
		return nil
	}

	// The meaning is repeated in every question, so it is charged per line
	// rather than once per request.
	perLine := perLineOverheadTokens + estimateTokens(instructions(lineID(1), meaning))

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

// estimateTokens approximates the tokens a string costs, without pulling in a
// tokenizer for a number that only has to be in the right ballpark. Three bytes
// per token is about right for CJK (one 3-byte rune per token) and an
// over-estimate for English (~4 bytes per token), and erring high is the safe
// direction here.
func estimateTokens(s string) int {
	return len(s)/3 + 1
}
