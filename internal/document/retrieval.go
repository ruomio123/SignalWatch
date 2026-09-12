package document

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Split preserves page boundaries; the overlap keeps sentences near boundaries retrievable.
func Split(id string, pages []string) []Chunk {
	out := []Chunk{}
	for page, text := range pages {
		words := strings.Fields(text)
		for start := 0; start < len(words); {
			end := min(start+600, len(words))
			out = append(out, Chunk{DocumentID: id, Number: len(out) + 1, Page: page + 1, Text: strings.Join(words[start:end], " ")})
			if end == len(words) {
				break
			}
			start = end - 80
		}
	}
	return out
}
func terms(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// Search ranks only the supplied document. No user text is used as SQL syntax.
func Search(chunks []Chunk, query string, limit int) []Chunk {
	if limit < 1 {
		return []Chunk{}
	}
	limit = min(limit, 8)
	type scored struct {
		index int
		score float64
	}
	frequency := map[string]int{}
	counts := make([]map[string]int, len(chunks))
	lengths := make([]int, len(chunks))
	total := 0
	for i, c := range chunks {
		counts[i] = map[string]int{}
		for _, t := range terms(c.Text) {
			counts[i][t]++
			lengths[i]++
			total++
		}
		for t := range counts[i] {
			frequency[t]++
		}
	}
	if total == 0 {
		return []Chunk{}
	}
	avg := float64(total) / float64(len(chunks))
	scoredRows := []scored{}
	seen := map[string]bool{}
	queryTerms := []string{}
	for _, t := range terms(query) {
		if !seen[t] {
			queryTerms = append(queryTerms, t)
			seen[t] = true
		}
	}
	for i := range chunks {
		score := 0.0
		for _, t := range queryTerms {
			tf := float64(counts[i][t])
			df := float64(frequency[t])
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(chunks))-df+0.5)/(df+0.5))
			score += idf * tf * 2.2 / (tf + 1.2*(0.25+0.75*float64(lengths[i])/avg))
		}
		if score > 0 {
			scoredRows = append(scoredRows, scored{i, score})
		}
	}
	sort.SliceStable(scoredRows, func(i, j int) bool { return scoredRows[i].score > scoredRows[j].score })
	out := []Chunk{}
	selected := map[int]bool{}
	for _, r := range scoredRows[:min(limit, len(scoredRows))] {
		selected[r.index] = true
		out = append(out, chunks[r.index])
	}
	// At most two neighbors, within the same bounded result budget.
	for _, r := range scoredRows[:min(2, len(scoredRows))] {
		n := r.index + 1
		if n < len(chunks) && !selected[n] && len(out) < 8 {
			selected[n] = true
			out = append(out, chunks[n])
		}
	}
	return out
}
