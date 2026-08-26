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

// Chunk splits text into overlapping chunks. Units (paragraph -> sentence ->
// hard rune block) are accumulated until they reach MaxRunes; the next chunk
// starts OverlapRunes earlier (by rune position) so boundaries are not cut
// mid-fact. Chunk *ends* land on unit boundaries; the overlap may start
// mid-unit, which is fine because it is only redundant context.
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
		u := unitIndexAt(units, pos)
		size := 0
		j := u
		for j < len(units) && (size == 0 || size+(units[j].end-units[j].start) <= max) {
			size += units[j].end - units[j].start
			j++
		}
		if j == u {
			j = u + 1
		}
		chunkStart := units[u].start
		chunkEnd := units[j-1].end
		chunks = append(chunks, Chunk{Index: idx, Start: chunkStart, End: chunkEnd, Text: string(r[chunkStart:chunkEnd])})
		idx++
		if j >= len(units) {
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

// unitIndexAt returns the index of the unit containing the rune position pos.
func unitIndexAt(units []chunkUnit, pos int) int {
	for i := 0; i < len(units); i++ {
		if units[i].start <= pos && pos < units[i].end {
			return i
		}
	}
	return len(units) - 1
}

func buildChunkUnits(r []rune, max int) []chunkUnit {
	var units []chunkUnit
	paras := splitOnRune(r, '\n')
	for _, p := range paras {
		if p.end-p.start == 0 {
			continue
		}
		if p.end-p.start <= max {
			units = append(units, p)
			continue
		}
		sents := splitSentences(r[p.start:p.end])
		for _, s := range sents {
			ss := p.start + s.start
			se := p.start + s.end
			if se-ss <= max {
				units = append(units, chunkUnit{ss, se})
				continue
			}
			for k := ss; k < se; k += max {
				e := k + max
				if e > se {
					e = se
				}
				units = append(units, chunkUnit{k, e})
			}
		}
	}
	return units
}

func splitOnRune(r []rune, sep rune) []chunkUnit {
	var out []chunkUnit
	start := 0
	for i := 0; i < len(r); i++ {
		if r[i] == sep {
			out = append(out, chunkUnit{start, i})
			start = i + 1
		}
	}
	out = append(out, chunkUnit{start, len(r)})
	return out
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
