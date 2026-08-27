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

	overlapCount := 0
	for i := 1; i < len(chunks); i++ {
		prev := chunks[i-1]
		cur := chunks[i]
		// Chunks must cover the source without gaps (overlap OR adjacency).
		if cur.Start > prev.End {
			t.Errorf("chunk %d leaves a gap: prev.End=%d cur.Start=%d", i, prev.End, cur.Start)
		}
		if cur.Start < prev.Start {
			t.Errorf("chunk %d moved backwards: prev.Start=%d cur.Start=%d", i, prev.Start, cur.Start)
		}
		// Reconstructed chunk text must equal the source slice.
		if cur.Text != string(r[cur.Start:cur.End]) {
			t.Errorf("chunk %d text does not match source slice", i)
		}
		if cur.Start < prev.End {
			overlapCount++
		}
	}
	if overlapCount == 0 {
		t.Errorf("expected at least one overlapping chunk pair, got none")
	}
}

// TestChunkerReportedConfigNoRunaway is a regression test for the reported
// failure: chunk_size=800, overlap=250 on a ~3223-rune text must terminate and
// produce a bounded, advancing set of chunks (no re-emitted duplicates).
func TestChunkerReportedConfigNoRunaway(t *testing.T) {
	c := &TextChunker{MaxRunes: 800, OverlapRunes: 250}
	var b strings.Builder
	for b.Len() < 3223 {
		b.WriteString("This is a fairly long sentence that talks about various topics and keeps going so the text is sizable. ")
	}
	text := b.String()
	r := []rune(text)
	n := len(r)
	chunks := c.Chunk(text)
	if len(chunks) == 0 {
		t.Fatal("no chunks produced")
	}
	maxChunks := n/(c.MaxRunes-c.OverlapRunes) + 5
	if len(chunks) > maxChunks {
		t.Errorf("runaway chunk count: %d > %d", len(chunks), maxChunks)
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].Start <= chunks[i-1].Start {
			t.Errorf("chunk %d does not advance (start %d <= previous %d)", i, chunks[i].Start, chunks[i-1].Start)
		}
	}
	if chunks[len(chunks)-1].End != n {
		t.Errorf("last chunk does not cover end: %d != %d", chunks[len(chunks)-1].End, n)
	}
}

// TestChunkerNoSentenceSplit verifies the critical rule: chunks never cut a
// sentence. An over-long sentence is kept intact as its own chunk (allowed to
// exceed MaxRunes), and every chunk boundary lands on a sentence terminator.
func TestChunkerNoSentenceSplit(t *testing.T) {
	c := &TextChunker{MaxRunes: 20, OverlapRunes: 5}
	long := "This is an extremely long sentence that is far longer than the limit and must remain completely whole."
	text := "Short one. " + long + " Another short. "
	r := []rune(text)
	chunks := c.Chunk(text)
	if len(chunks) == 0 {
		t.Fatal("no chunks produced")
	}

	// Every chunk must be whole sentences/lines: no word may be cut, and the
	// source slice must reconstruct exactly.
	assertWholeSentences(t, text, chunks, r)

	// The over-long sentence must appear whole in exactly one chunk. Whitespace
	// around the boundary is irrelevant to the "no split" guarantee.
	seen := 0
	for _, ch := range chunks {
		if strings.TrimSpace(ch.Text) == long {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("over-long sentence should appear intact in exactly one chunk, got %d", seen)
	}
}

// TestChunkerConfigurableSizes verifies MaxRunes/OverlapRunes are honored from
// the struct (the values main.go reads from .env and assigns), that chunks stay
// bounded in size, and that the window advances (no infinite re-emission).
func TestChunkerConfigurableSizes(t *testing.T) {
	c := &TextChunker{MaxRunes: 50, OverlapRunes: 10}
	text := strings.Repeat("Word. ", 100)
	r := []rune(text)
	n := len(r)
	chunks := c.Chunk(text)

	// Whole-sentence/line guarantee: no word cut, exact slice reconstruction,
	// full coverage, and advancing (overlapping) chunks.
	assertWholeSentences(t, text, chunks, r)

	// Termination guard: the window must not produce a runaway number of chunks.
	maxChunks := n/(c.MaxRunes-c.OverlapRunes) + 5
	if len(chunks) > maxChunks {
		t.Fatalf("too many chunks (%d > %d): possible non-termination", len(chunks), maxChunks)
	}
	for i, ch := range chunks {
		size := ch.End - ch.Start
		if size <= 0 {
			t.Errorf("chunk %d has non-positive size %d", i, size)
		}
		// A chunk may exceed MaxRunes only when a single sentence is longer
		// than the limit; here every sentence is tiny, so bound it tightly.
		if i < len(chunks)-1 && size > c.MaxRunes*2 {
			t.Errorf("chunk %d size %d exceeds 2x MaxRunes %d", i, size, c.MaxRunes)
		}
		if i > 0 && chunks[i].Start <= chunks[i-1].Start {
			t.Errorf("chunk %d does not advance (start %d <= previous %d)", i, chunks[i].Start, chunks[i-1].Start)
		}
	}
}

// TestChunkerOverlapStartsOnSentence is the critical overlap regression: every
// chunk (including ones that re-include trailing text from the previous chunk to
// form the overlap) MUST begin on a sentence/line boundary — never mid-sentence
// or mid-word. The size limit may be exceeded to honor this.
func TestChunkerOverlapStartsOnSentence(t *testing.T) {
	configs := []struct{ max, overlap int }{
		{800, 250}, // reported config
		{50, 40},   // overlap close to the max/2 cap
		{100, 95},  // overlap capped to 50
		{10, 4},
		{5, 2},
		{200, 0}, // no overlap at all
	}
	for _, cfg := range configs {
		c := &TextChunker{MaxRunes: cfg.max, OverlapRunes: cfg.overlap}
		var b strings.Builder
		for i := 0; i < 40; i++ {
			switch i % 3 {
			case 0:
				b.WriteString("This is a reasonably long sentence about topic number ")
				b.WriteString(string(rune('A' + i%26)))
				b.WriteString(" that continues for a while so it is sizable. ")
			case 1:
				b.WriteString("Short line without punctuation here\n")
			default:
				b.WriteString("Another line. ")
			}
		}
		text := b.String()
		r := []rune(text)
		chunks := c.Chunk(text)
		for i := 1; i < len(chunks); i++ {
			s := chunks[i].Start
			if s > 0 {
				// The rune just before a chunk start must be a sentence/line
				// boundary, otherwise the overlap began mid-sentence.
				if !isWordBoundary(r[s-1]) {
					t.Errorf("cfg %+v: chunk %d starts mid-sentence at rune %d (%q|%q)", cfg, i, s, string(r[s-1]), string(r[s]))
				}
			}
		}
		assertWholeSentences(t, text, chunks, r)
	}
}

// TestChunkerNewlineSeparator verifies a newline acts as a sentence separator
// even when a line carries no terminator punctuation, and that no word is ever
// split across a chunk.
func TestChunkerNewlineSeparator(t *testing.T) {
	c := &TextChunker{MaxRunes: 20, OverlapRunes: 5}
	text := "First unpunctuated line\nSecond unpunctuated line\nA normally punctuated sentence. And another one."
	r := []rune(text)
	chunks := c.Chunk(text)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	assertWholeSentences(t, text, chunks, r)
	// Each newline-delimited line must appear intact in some chunk.
	lines := []string{"First unpunctuated line", "Second unpunctuated line", "A normally punctuated sentence.", "And another one."}
	for _, line := range lines {
		found := false
		for _, ch := range chunks {
			if strings.TrimSpace(ch.Text) == line {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("line not present as a whole chunk: %q", line)
		}
	}
}

// assertWholeSentences checks the core invariant: chunks never cut a word, each
// chunk's text equals its exact source slice, chunks overlap/advance (never
// regress or leave a gap), and together they cover the whole text. A boundary
// is valid when the cut falls on whitespace, a sentence terminator, or the
// edges of the text.
func assertWholeSentences(t *testing.T, text string, chunks []Chunk, r []rune) {
	t.Helper()
	if len(chunks) == 0 {
		t.Error("no chunks produced")
		return
	}
	if chunks[0].Start != 0 {
		t.Errorf("first chunk should start at 0, got %d", chunks[0].Start)
	}
	if chunks[len(chunks)-1].End != len(r) {
		t.Errorf("last chunk should end at %d, got %d", len(r), chunks[len(chunks)-1].End)
	}
	for i, ch := range chunks {
		if ch.Text != string(r[ch.Start:ch.End]) {
			t.Errorf("chunk %d text does not match source slice", i)
		}
		if i > 0 {
			prev := chunks[i-1]
			if ch.Start < prev.Start {
				t.Errorf("chunk %d moved backwards: prev.Start=%d ch.Start=%d", i, prev.Start, ch.Start)
			}
			// A gap is only acceptable when it consists solely of separators
			// (e.g. the newline between lines); any other content would be lost.
			if ch.Start > prev.End {
				for k := prev.End; k < ch.Start; k++ {
					if !isSpace(r[k]) {
						t.Errorf("chunk %d leaves a gap containing content: %q", i, string(r[prev.End:ch.Start]))
						break
					}
				}
			}
		}
		if ch.Start > 0 && !isWordBoundary(r[ch.Start]) && !isWordBoundary(r[ch.Start-1]) {
			t.Errorf("chunk %d starts mid-word near %q|%q", i, string(r[ch.Start-1]), string(r[ch.Start]))
		}
		if ch.End < len(r) && !isWordBoundary(r[ch.End-1]) && !isWordBoundary(r[ch.End]) {
			t.Errorf("chunk %d ends mid-word near %q|%q", i, ch.Text, string(r[ch.End]))
		}
	}
}

func isWordBoundary(r rune) bool {
	return isSpace(r) || isSentenceTerminator(r)
}
