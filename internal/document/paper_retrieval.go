package document

import (
	"math"
	"sort"
	"strings"
)

// SearchPassage identifies one immutable, server-created passage in a document.
// Start is the byte offset within Chunk; Text is returned without modification.
type SearchPassage struct {
	ID    string
	Chunk int
	Page  int
	Start int
	Text  string
}

const (
	paperSearchQueryLimit   = 4
	paperSearchQueryHits    = 8
	paperSearchPassageLimit = 32
	paperSearchRRFConstant  = 60
)

// RankPassages searches only the supplied document. Each distinct query first
// contributes its best positive BM25 match, followed by those matches' immediate
// neighbors within the same chunk and page. Reciprocal-rank fusion fills the
// remaining budget. Results contain whole passages with distinct IDs and never
// exceed limit (or the hard ceiling of 32).
//
// Paper followups use limit=24 initially. Supplemental retrieval can rank 32
// passages from the same full corpus, exclude already selected IDs, and retain
// at most eight new passages. Keeping the corpus intact preserves exact neighbor
// relationships. The caller owns the serialized model-input byte budget.
func RankPassages(passages []SearchPassage, queries []string, limit int) []SearchPassage {
	return rankPassages(passages, queries, limit, paperSearchQueryLimit)
}

// Reproduction searches all six fixed categories without changing normal QA.
func RankReproductionPassages(passages []SearchPassage, queries []string, limit int) []SearchPassage {
	return rankPassages(passages, queries, limit, 6)
}

func rankPassages(passages []SearchPassage, queries []string, limit, queryLimit int) []SearchPassage {
	limit = min(limit, paperSearchPassageLimit)
	if limit < 1 || len(passages) == 0 {
		return []SearchPassage{}
	}
	// Canonical source order makes ranking and neighbor selection independent of
	// the order in which the document store supplied passages.
	ordered := append([]SearchPassage(nil), passages...)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Page != b.Page {
			return a.Page < b.Page
		}
		if a.Chunk != b.Chunk {
			return a.Chunk < b.Chunk
		}
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Text < b.Text
	})
	corpus := make([]SearchPassage, 0, len(ordered))
	seenIDs := make(map[string]bool, len(ordered))
	for _, passage := range ordered {
		if passage.ID != "" && !seenIDs[passage.ID] {
			corpus = append(corpus, passage)
			seenIDs[passage.ID] = true
		}
	}
	counts := make([]map[string]int, len(corpus))
	lengths := make([]int, len(corpus))
	frequency := map[string]int{}
	total := 0
	for i, passage := range corpus {
		counts[i] = map[string]int{}
		for _, term := range terms(passage.Text) {
			counts[i][term]++
			lengths[i]++
			total++
		}
		for term := range counts[i] {
			frequency[term]++
		}
	}
	if total == 0 {
		return []SearchPassage{}
	}
	average := float64(total) / float64(len(corpus))
	type rankedPassage struct {
		index int
		score float64
	}
	seeds := []int{}
	fused := make([]float64, len(corpus))
	seenQueries := map[string]bool{}
	for _, query := range queries {
		queryTerms := terms(query)
		sort.Strings(queryTerms)
		uniqueTerms := make([]string, 0, len(queryTerms))
		for _, term := range queryTerms {
			if len(uniqueTerms) == 0 || uniqueTerms[len(uniqueTerms)-1] != term {
				uniqueTerms = append(uniqueTerms, term)
			}
		}
		key := strings.Join(uniqueTerms, " ")
		if key == "" || seenQueries[key] {
			continue
		}
		if len(seenQueries) == queryLimit {
			break
		}
		seenQueries[key] = true
		ranked := []rankedPassage{}
		for i := range corpus {
			score := 0.0
			for _, term := range uniqueTerms {
				tf := float64(counts[i][term])
				if tf == 0 {
					continue
				}
				df := float64(frequency[term])
				idf := math.Log(1 + (float64(len(corpus))-df+0.5)/(df+0.5))
				score += idf * tf * 2.2 / (tf + 1.2*(0.25+0.75*float64(lengths[i])/average))
			}
			if score > 0 {
				ranked = append(ranked, rankedPassage{index: i, score: score})
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
		ranked = ranked[:min(len(ranked), paperSearchQueryHits)]
		if len(ranked) > 0 {
			seeds = append(seeds, ranked[0].index)
		}
		for rank, passage := range ranked {
			fused[passage.index] += 1 / float64(paperSearchRRFConstant+rank+1)
		}
	}
	selected := make(map[int]bool, limit)
	out := make([]SearchPassage, 0, min(limit, len(corpus)))
	appendPassage := func(index int) {
		if len(out) < limit && !selected[index] {
			selected[index] = true
			out = append(out, corpus[index])
		}
	}
	// Protect coverage of distinct query intents before adding any context.
	for _, index := range seeds {
		appendPassage(index)
	}
	for _, index := range seeds {
		seed := corpus[index]
		for _, neighbor := range []int{index - 1, index + 1} {
			if neighbor >= 0 && neighbor < len(corpus) && seed.Page > 0 && seed.Chunk > 0 && corpus[neighbor].Page == seed.Page && corpus[neighbor].Chunk == seed.Chunk {
				appendPassage(neighbor)
			}
		}
	}
	ranked := []rankedPassage{}
	for i, score := range fused {
		if score > 0 {
			ranked = append(ranked, rankedPassage{index: i, score: score})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	for _, passage := range ranked {
		appendPassage(passage.index)
	}
	return out
}
