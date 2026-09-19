package jev

import (
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  []Chunk
	}{
		{
			name:  "no lines needs no request",
			lines: nil,
		},
		{
			name:  "a short input stays in one chunk",
			lines: []string{"a", "b", "c"},
			want:  []Chunk{{Offset: 0, Lines: []string{"a", "b", "c"}}},
		},
		{
			name:  "a chunk holds at most maxLinesPerChunk lines",
			lines: repeat("x", maxLinesPerChunk+1),
			want: []Chunk{
				{Offset: 0, Lines: repeat("x", maxLinesPerChunk)},
				{Offset: maxLinesPerChunk, Lines: repeat("x", 1)},
			},
		},
		{
			name:  "long lines split below the line count",
			lines: repeat(strings.Repeat("x", 2*chunkTokenBudget), 3),
			want: []Chunk{
				{Offset: 0, Lines: repeat(strings.Repeat("x", 2*chunkTokenBudget), 1)},
				{Offset: 1, Lines: repeat(strings.Repeat("x", 2*chunkTokenBudget), 1)},
				{Offset: 2, Lines: repeat(strings.Repeat("x", 2*chunkTokenBudget), 1)},
			},
		},
		{
			name:  "a line over the budget still gets scored",
			lines: []string{strings.Repeat("x", 4*chunkTokenBudget)},
			want:  []Chunk{{Offset: 0, Lines: []string{strings.Repeat("x", 4*chunkTokenBudget)}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Split("an error", tt.lines)

			if len(got) != len(tt.want) {
				t.Fatalf("got %d chunks, want %d", len(got), len(tt.want))
			}
			for i, chunk := range got {
				if chunk.Offset != tt.want[i].Offset {
					t.Errorf("chunk %d offset = %d, want %d", i, chunk.Offset, tt.want[i].Offset)
				}
				if len(chunk.Lines) != len(tt.want[i].Lines) {
					t.Errorf("chunk %d has %d lines, want %d", i, len(chunk.Lines), len(tt.want[i].Lines))
				}
			}
		})
	}
}

// Every line must be scored exactly once and in order, whatever the split.
func TestSplitCoversEveryLineInOrder(t *testing.T) {
	lines := make([]string, 0, 100)
	for i := range 100 {
		lines = append(lines, strings.Repeat("x", i*400))
	}

	var seen []string
	for _, chunk := range Split("an error", lines) {
		if got := lines[chunk.Offset]; got != chunk.Lines[0] {
			t.Errorf("chunk at offset %d starts with the wrong line", chunk.Offset)
		}
		seen = append(seen, chunk.Lines...)
	}

	if len(seen) != len(lines) {
		t.Fatalf("got %d lines back, want %d", len(seen), len(lines))
	}
	for i := range lines {
		if seen[i] != lines[i] {
			t.Fatalf("line %d came back changed", i)
		}
	}
}

// A longer meaning is repeated in every question, so it must cost chunk room.
func TestSplitChargesForTheMeaning(t *testing.T) {
	lines := repeat(strings.Repeat("x", chunkTokenBudget), 4)

	short := Split("err", lines)
	long := Split(strings.Repeat("a description of the meaning ", 1000), lines)

	if len(long) <= len(short) {
		t.Errorf("a long meaning produced %d chunks, want more than %d", len(long), len(short))
	}
}

func repeat(s string, n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = s
	}
	return lines
}
