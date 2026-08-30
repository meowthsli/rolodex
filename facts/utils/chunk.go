package utils

import (
	"strings"
	"unicode"
)

const (
	DefaultChunkRunes   = 4000
	DefaultOverlapRunes = 400
)

// abbrevTokens are multi-letter abbreviations whose trailing dot must NOT be
// treated as a sentence boundary (e.g. "руб.", "тыс.", "млн.", "коп.").
// Single-letter abbreviations ("г.", "п.", initials "В. А.") are handled
// separately by isAbbreviationDot without needing to be listed here.
var abbrevTokens = map[string]bool{
	"руб":   true,
	"тыс":   true,
	"млн":   true,
	"млрд":  true,
	"коп":   true,
	"см":    true,
	"рис":   true,
	"табл":  true,
	"им":    true,
	"проф":  true,
	"доц":   true,
	"акад":  true,
	"др":    true,
	"пр":    true,
	"тов":   true,
}

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

// Chunk splits text into overlapping chunks, each consisting ONLY of whole
// sentences (never a partial sentence). Sentences are packed greedily up to
// MaxRunes; a single sentence longer than MaxRunes is kept intact as its own
// chunk even though it exceeds the limit. Consecutive chunks overlap by about
// OverlapRunes: the next chunk starts at a sentence boundary inside the
// previous chunk and re-includes those trailing sentences, so every chunk
// boundary is a real sentence end.
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

	units := buildChunkUnits(r)
	if len(units) == 0 {
		return []Chunk{{Index: 0, Start: 0, End: n, Text: string(r)}}
	}

	var chunks []Chunk
	idx := 0
	start := 0
	total := len(units)
	for start < total {
		// Pack whole sentences until adding the next would exceed MaxRunes.
		size := 0
		j := start
		for j < total && (size == 0 || size+(units[j].end-units[j].start) <= max) {
			size += units[j].end - units[j].start
			j++
		}
		if j == start {
			j = start + 1
		}
		end := j
		chunkStart := units[start].start
		chunkEnd := units[end-1].end
		chunks = append(chunks, Chunk{Index: idx, Start: chunkStart, End: chunkEnd, Text: string(r[chunkStart:chunkEnd])})
		idx++
		if end >= total {
			break
		}
		// Next chunk starts at a sentence boundary inside this chunk so the two
		// share trailing sentences (the overlap). Advance at least one sentence
		// to guarantee termination.
		target := chunkEnd - overlap
		next := start + 1
		for next < end-1 && units[next+1].start <= target {
			next++
		}
		start = next
	}
	return chunks
}

// buildChunkUnits splits text into whole-sentence units. A sentence longer than
// MaxRunes is kept intact (it becomes a single oversized unit) — callers must
// never split a sentence.
func buildChunkUnits(r []rune) []chunkUnit {
	var units []chunkUnit
	for _, s := range splitSentences(r) {
		if s.end-s.start == 0 {
			continue
		}
		units = append(units, s)
	}
	return units
}

func splitSentences(r []rune) []chunkUnit {
	var out []chunkUnit
	start := 0
	for i := 0; i < len(r); i++ {
		c := r[i]
		if isSentenceTerminator(c) {
			// A dot ending an abbreviation (e.g. "г.", "руб.", "п.п.", "п. п.")
			// is not a sentence boundary; keep scanning.
			if c == '.' && isAbbreviationDot(r, i) {
				continue
			}
			// Extend past any trailing whitespace so each sentence unit ends on
			// the terminator (and the next unit starts on a real character),
			// preventing a leading space from becoming part of the next
			// sentence or a whitespace-only fragment of its own.
			end := i + 1
			for end < len(r) && isSpace(r[end]) {
				end++
			}
			out = append(out, chunkUnit{start, end})
			start = end
			i = start - 1
		} else if c == '\n' || c == '\r' {
			// A newline is a sentence separator even when the line has no
			// terminator punctuation, so unpunctuated lines stay whole.
			if i > start {
				out = append(out, chunkUnit{start, i})
			}
			start = i + 1
			for start < len(r) && isSpace(r[start]) {
				start++
			}
			i = start - 1
		}
	}
	if start < len(r) {
		out = append(out, chunkUnit{start, len(r)})
	}
	return out
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

func isSentenceTerminator(r rune) bool {
	switch r {
	case '.', '!', '?', '。', '！', '？':
		return true
	}
	return false
}

// isAbbreviationDot reports whether the dot at r[i] ends an abbreviation rather
// than a sentence. It returns true when the dot is adjacent to another dot
// (ellipsis or dotted abbreviations like "п.п."), when the letter immediately
// before it is a single letter ("г.", "п.", initials "В. А."), or when the word
// before it is a known multi-letter abbreviation ("руб.", "тыс.", ...).
func isAbbreviationDot(r []rune, i int) bool {
	if i <= 0 {
		return false
	}
	// Ellipsis or dotted abbreviations ("п.п.", "т.е."): a dot next to another
	// dot is never a sentence terminator.
	if r[i-1] == '.' || (i+1 < len(r) && r[i+1] == '.') {
		return true
	}
	// Collect the letters immediately preceding the dot.
	j := i - 1
	for j >= 0 && unicode.IsLetter(r[j]) {
		j--
	}
	token := string(r[j+1 : i])
	if token == "" {
		return false
	}
	if len([]rune(token)) == 1 {
		// Single-letter abbreviation, e.g. "г.", "п.", or a name initial "В.".
		return true
	}
	if abbrevTokens[strings.ToLower(token)] {
		return true
	}
	return false
}
