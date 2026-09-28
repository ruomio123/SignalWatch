package document

import (
	"fmt"
	"reflect"
	"testing"
)

func passageIDs(passages []SearchPassage) []string {
	out := make([]string, len(passages))
	for i, passage := range passages {
		out[i] = passage.ID
	}
	return out
}

func TestRankPassagesCoversQueriesBeforeNeighborExpansion(t *testing.T) {
	passages := []SearchPassage{
		{ID: "alpha", Page: 1, Chunk: 1, Start: 1000, Text: "alpha"},
		{ID: "before", Page: 1, Chunk: 1, Start: 0, Text: "The comparison applies only under these conditions."},
		{ID: "after", Page: 1, Chunk: 1, Start: 2000, Text: "The reported benefit has a narrow confidence interval."},
		{ID: "beta", Page: 2, Chunk: 2, Start: 0, Text: "beta"},
	}
	for _, tc := range []struct {
		limit int
		want  []string
	}{
		{0, []string{}},
		{1, []string{"alpha"}},
		{2, []string{"alpha", "beta"}},
		{3, []string{"alpha", "beta", "before"}},
		{24, []string{"alpha", "beta", "before", "after"}},
	} {
		t.Run(fmt.Sprint(tc.limit), func(t *testing.T) {
			got := passageIDs(RankPassages(passages, []string{"alpha", "beta"}, tc.limit))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("limit %d: got %v, want %v", tc.limit, got, tc.want)
			}
		})
	}
}

func TestRankPassagesFusesPositiveRanksAndBreaksTiesBySource(t *testing.T) {
	passages := []SearchPassage{
		{ID: "shared", Page: 5, Chunk: 5, Text: "alpha beta filler filler"},
		{ID: "beta-second", Page: 4, Chunk: 4, Text: "beta filler filler filler"},
		{ID: "alpha-first", Page: 1, Chunk: 1, Text: "alpha alpha alpha filler"},
		{ID: "alpha-second", Page: 3, Chunk: 3, Text: "alpha filler filler filler"},
		{ID: "beta-first", Page: 2, Chunk: 2, Text: "beta beta beta filler"},
		{ID: "unrelated", Page: 6, Chunk: 6, Text: "gamma gamma filler filler"},
	}
	want := []string{"alpha-first", "beta-first", "shared", "alpha-second", "beta-second"}
	before := append([]SearchPassage(nil), passages...)
	got := passageIDs(RankPassages(passages, []string{"alpha", "beta"}, 24))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if !reflect.DeepEqual(passages, before) {
		t.Fatal("retrieval changed the supplied evidence")
	}
	for i, j := 0, len(passages)-1; i < j; i, j = i+1, j-1 {
		passages[i], passages[j] = passages[j], passages[i]
	}
	if got := passageIDs(RankPassages(passages, []string{"alpha", "beta"}, 24)); !reflect.DeepEqual(got, want) {
		t.Fatalf("source input order changed results: %v", got)
	}
}

func TestRankPassagesNeighborsStayInsidePageAndChunk(t *testing.T) {
	passages := []SearchPassage{
		{ID: "other-page", Page: 1, Chunk: 2, Text: "Earlier page unrelated text."},
		{ID: "hit", Page: 2, Chunk: 2, Start: 0, Text: "alpha"},
		{ID: "adjacent", Page: 2, Chunk: 2, Start: 1000, Text: "Exact quote: \"中文🙂\"\n\t\\ no clipping."},
		{ID: "nonadjacent", Page: 2, Chunk: 2, Start: 2000, Text: "Further within the same chunk."},
		{ID: "other-chunk", Page: 2, Chunk: 3, Text: "Different chunk unrelated text."},
	}
	got := RankPassages(passages, []string{"alpha"}, 24)
	if !reflect.DeepEqual(passageIDs(got), []string{"hit", "adjacent"}) {
		t.Fatalf("included unrelated neighbors: %v", got)
	}
	if got[1] != passages[2] {
		t.Fatalf("changed source text or metadata: %+v", got[1])
	}
	got = RankPassages(passages, []string{"clipping"}, 24)
	if !reflect.DeepEqual(passageIDs(got), []string{"adjacent", "hit", "nonadjacent"}) {
		t.Fatalf("missing immediate context: %v", got)
	}
}

func TestRankPassagesUsesPageChunkAndOffsetForEqualScores(t *testing.T) {
	passages := []SearchPassage{
		{ID: "page-two", Page: 2, Chunk: 3, Text: "alpha"},
		{ID: "chunk-two", Page: 1, Chunk: 2, Text: "alpha"},
		{ID: "offset-two", Page: 1, Chunk: 1, Start: 1000, Text: "alpha"},
		{ID: "first", Page: 1, Chunk: 1, Start: 0, Text: "alpha"},
	}
	want := []string{"first", "offset-two", "chunk-two", "page-two"}
	if got := passageIDs(RankPassages(passages, []string{"alpha"}, 24)); !reflect.DeepEqual(got, want) {
		t.Fatalf("unstable equal-score ordering: got %v, want %v", got, want)
	}
}

func TestRankPassagesDeduplicatesQueriesAndPassages(t *testing.T) {
	passages := []SearchPassage{
		{ID: "alpha", Page: 1, Chunk: 1, Text: "alpha alpha"},
		{ID: "beta", Page: 2, Chunk: 2, Text: "beta beta"},
		{ID: "both", Page: 3, Chunk: 3, Text: "alpha beta"},
	}
	want := RankPassages(passages, []string{"alpha beta", "beta"}, 24)
	duplicated := append(append([]SearchPassage(nil), passages...), passages...)
	duplicated = append(duplicated, SearchPassage{Text: "alpha beta", Page: 0})
	got := RankPassages(duplicated, []string{"  ALPHA beta alpha ", "beta,alpha", "", "beta", "BETA"}, 24)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("duplicate queries or sources affected ranking: got %v, want %v", got, want)
	}
}

func TestRankPassagesDoesNotInventHits(t *testing.T) {
	passages := []SearchPassage{
		{ID: "one", Page: 1, Chunk: 1, Text: "The paper evaluates retrieval accuracy."},
		{ID: "two", Page: 1, Chunk: 1, Start: 1000, Text: "Evaluation uses held out data."},
	}
	for _, queries := range [][]string{nil, {"", "!?!"}, {"unmentionedconcept"}} {
		if got := RankPassages(passages, queries, 24); len(got) != 0 {
			t.Fatalf("invented evidence for %v: %v", queries, got)
		}
	}
	if got := RankPassages([]SearchPassage{{ID: "empty", Text: "?!"}}, []string{"accuracy"}, 24); len(got) != 0 {
		t.Fatalf("invented evidence from empty corpus: %v", got)
	}
}

func TestRankPassagesBoundsQueryCandidatesAndTotalSelection(t *testing.T) {
	passages := []SearchPassage{}
	queries := []string{}
	for query := 1; query <= 5; query++ {
		word := fmt.Sprintf("topic%d", query)
		queries = append(queries, word)
		for hit := 1; hit <= 12; hit++ {
			index := (query-1)*12 + hit
			passages = append(passages, SearchPassage{ID: fmt.Sprintf("%s-%d", word, hit), Page: index, Chunk: index, Text: word})
		}
	}
	got := RankPassages(passages, queries[:1], 32)
	if len(got) != 8 || got[0].ID != "topic1-1" || got[7].ID != "topic1-8" {
		t.Fatalf("per-query candidate limit or source tie order violated: %v", got)
	}
	got = RankPassages(passages, queries, 100)
	if len(got) != 32 {
		t.Fatalf("unexpected global result count: %d", len(got))
	}
	seen := map[string]bool{}
	for _, passage := range got {
		if seen[passage.ID] || passage.Text == "topic5" {
			t.Fatalf("duplicate passage or excess query selected: %v", passage)
		}
		seen[passage.ID] = true
	}
	for i, id := range []string{"topic1-1", "topic2-1", "topic3-1", "topic4-1"} {
		if got[i].ID != id {
			t.Fatalf("query coverage lost at %d: %+v", i, got)
		}
	}
	if got := RankPassages(passages, queries, 24); len(got) != 24 {
		t.Fatalf("initial selection limit violated: %d", len(got))
	}
}
