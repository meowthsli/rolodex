package facts

const (
	DefaultChunkRunes   = 4000
	DefaultOverlapRunes = 400
)

// TextChunker splits a long readable text into overlapping, sentence/paragraph
// aware chunks small enough to send to the LLM. Offsets are rune-based so they
// stay correct for multi-byte (UTF-8) content.
type TextChunker struct {
	MaxRunes    int
	OverlapRunes int
}

func NewTextChunker() *TextChunker {
	return &TextChunker{MaxRunes: DefaultChunkRunes, OverlapRunes: DefaultOverlapRunes}
}

// Chunk is one slice of the source text handed to the analyzer. Start/End are
// rune offsets into the original text (End is exclusive).
type Chunk struct {
	Index int
	Start int
	End   int
	Text  string
}

type chunkUnit struct {
	start int
	end   int
}

// Chunk splits text into overlapping chunks using a sliding window. Each chunk
// is ~MaxRunes long and its *end* is snapped forward to the next sentence
// boundary so facts are never cut mid-sentence. The next chunk starts
// OverlapRunes before the previous end, so consecutive chunks overlap by about
// OverlapRunes. The window always advances by (MaxRunes - OverlapRunes), which
// guarantees termination and prevents the same chunk from being re-emitted.
func (c *TextChunker) Chunk(text string) []Chunk {
	max := c.MaxRunes
	if max <= 0 {
		max = DefaultChunkRunes
	}
	overlap := c.OverlapRunes
	if overlap < 0 {
		overlap = 0
	}
	if overlap > max/2 {
		overlap = max / 2
	}

	r := []rune(text)
	n := len(r)
	if n == 0 {
		return nil
	}
	if n <= max {
		return []Chunk{{Index: 0, Start: 0, End: n, Text: string(r)}}
	}

	units := buildChunkUnits(r, max)
	if len(units) == 0 {
		return []Chunk{{Index: 0, Start: 0, End: n, Text: string(r)}}
	}

	var chunks []Chunk
	idx := 0
	pos := 0
	for pos < n {
		rawEnd := pos + max
		if rawEnd > n {
			rawEnd = n
		}
		// Snap the end forward to the next unit boundary (or n) so a chunk
		// never ends mid-sentence; the leading overlap may start mid-unit.
		chunkEnd := boundaryAtOrAfter(units, rawEnd)
		if chunkEnd > n {
			chunkEnd = n
		}
		chunks = append(chunks, Chunk{Index: idx, Start: pos, End: chunkEnd, Text: string(r[pos:chunkEnd])})
		idx++
		if chunkEnd >= n {
			break
		}
		nextPos := chunkEnd - overlap
		if nextPos <= pos {
			nextPos = pos + 1
		}
		pos = nextPos
	}
	return chunks
}

// boundaryAtOrAfter returns the smallest unit end that is >= pos (i.e. the next
// sentence/paragraph boundary at or after pos). If none exists it returns the
// last unit end.
func boundaryAtOrAfter(units []chunkUnit, pos int) int {
	for _, u := range units {
		if u.end >= pos {
			return u.end
		}
	}
	return units[len(units)-1].end
}

// buildChunkUnits splits text into sentence-sized units (falling back to hard
// rune blocks for any sentence longer than max). Paragraph/newline boundaries
// are intentionally ignored: only sentences define chunk units.
func buildChunkUnits(r []rune, max int) []chunkUnit {
	var units []chunkUnit
	sents := splitSentences(r)
	for _, s := range sents {
		if s.end-s.start == 0 {
			continue
		}
		if s.end-s.start <= max {
			units = append(units, s)
			continue
		}
		for k := s.start; k < s.end; k += max {
			e := k + max
			if e > s.end {
				e = s.end
			}
			units = append(units, chunkUnit{k, e})
		}
	}
	return units
}

func splitSentences(r []rune) []chunkUnit {
	var out []chunkUnit
	start := 0
	for i := 0; i < len(r); i++ {
		if isSentenceTerminator(r[i]) {
			out = append(out, chunkUnit{start, i + 1})
			start = i + 1
		}
	}
	if start < len(r) {
		out = append(out, chunkUnit{start, len(r)})
	}
	return out
}

func isSentenceTerminator(r rune) bool {
	switch r {
	case '.', '!', '?', '。', '！', '？':
		return true
	}
	return false
}
