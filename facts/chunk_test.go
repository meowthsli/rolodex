package facts

import (
	"strings"
	"testing"
)

// TestChunkerShortTextIsSingleChunk verifies a text shorter than the limit is
// returned as one chunk covering the whole text.
func TestChunkerShortTextIsSingleChunk(t *testing.T) {
	c := NewTextChunker()
	text := "Apple acquired NeXT in 1996."
	chunks := c.Chunk(text)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Start != 0 || chunks[0].End != len([]rune(text)) {
		t.Errorf("chunk bounds = %d..%d, want 0..%d", chunks[0].Start, chunks[0].End, len([]rune(text)))
	}
	if chunks[0].Text != text {
		t.Errorf("chunk text = %q", chunks[0].Text)
	}
}

// TestChunkerOverlaps verifies a long text is split into multiple chunks that
// overlap (so facts are not cut at boundaries) and fully cover the source.
func TestChunkerOverlaps(t *testing.T) {
	c := &TextChunker{MaxRunes: 200, OverlapRunes: 40}
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("Sentence number ")
		b.WriteString(string(rune('A' + i%26)))
		b.WriteString(" is about something interesting. ")
	}
	text := b.String()
	r := []rune(text)
	chunks := c.Chunk(text)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for long text, got %d", len(chunks))
	}

	if chunks[0].Start != 0 {
		t.Errorf("first chunk should start at 0, got %d", chunks[0].Start)
	}
	if chunks[len(chunks)-1].End != len(r) {
		t.Errorf("last chunk should end at %d, got %d", len(r), chunks[len(chunks)-1].End)
	}

	for i := 1; i < len(chunks); i++ {
		prev := chunks[i-1]
		cur := chunks[i]
		if cur.Start >= prev.End {
			t.Errorf("chunk %d does not overlap previous: prev.End=%d cur.Start=%d", i, prev.End, cur.Start)
		}
		if cur.Start < prev.Start {
			t.Errorf("chunk %d moved backwards: prev.Start=%d cur.Start=%d", i, prev.Start, cur.Start)
		}
		// Reconstructed chunk text must equal the source slice.
		if cur.Text != string(r[cur.Start:cur.End]) {
			t.Errorf("chunk %d text does not match source slice", i)
		}
	}
}

// TestChunkerConfigurableSizes verifies MaxRunes/OverlapRunes are honored from
// the struct (the values main.go reads from .env and assigns).
func TestChunkerConfigurableSizes(t *testing.T) {
	c := &TextChunker{MaxRunes: 50, OverlapRunes: 10}
	text := strings.Repeat("word ", 100)
	chunks := c.Chunk(text)
	// Every chunk except possibly the last should be near the max size.
	for i, ch := range chunks {
		size := ch.End - ch.Start
		if i < len(chunks)-1 && size > c.MaxRunes {
			t.Errorf("chunk %d size %d exceeds MaxRunes %d", i, size, c.MaxRunes)
		}
	}
}
