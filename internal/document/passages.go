package document

import (
	"strings"
	"unicode/utf8"
)

// Passage is an exact byte range within an extracted chunk. Boundaries are for
// citation selection, not semantic interpretation; no source bytes are removed.
type Passage struct {
	Start int
	End   int
	Text  string
}

// Passages bounds evidence size without asking a model to reconstruct quotations.
// Prefer word boundaries; even unbroken text remains lossless and valid UTF-8.
func Passages(text string, limit int) []Passage {
	if limit < utf8.UTFMax {
		panic("passage limit too small")
	}
	out := []Passage{}
	for start := 0; start < len(text); {
		end := min(start+limit, len(text))
		if end < len(text) {
			for !utf8.RuneStart(text[end]) {
				end--
			}
			if space := strings.LastIndexAny(text[start:end], " \n\t\r"); space >= limit/2 {
				end = start + space + 1
			}
		}
		out = append(out, Passage{Start: start, End: end, Text: text[start:end]})
		start = end
	}
	return out
}
