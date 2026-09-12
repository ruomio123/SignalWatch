package document

import (
	"strings"
	"testing"
)

func TestSplitPreservesPagesAndOverlap(t *testing.T) {
	chunks := Split("doc", []string{strings.Repeat("alpha ", 700), "Evaluation uses a separate held out dataset."})
	if len(chunks) != 3 || chunks[0].Page != 1 || chunks[1].Page != 1 || chunks[2].Page != 2 || chunks[2].Number != 3 {
		t.Fatalf("bad chunks: %+v", chunks)
	}
	if len(strings.Fields(chunks[1].Text)) != 180 {
		t.Fatal("overlap missing")
	}
}
func TestBM25RanksRelevantPassagesAndDoesNotInventHits(t *testing.T) {
	chunks := []Chunk{{Number: 1, Page: 1, Text: "An introduction to language models and their motivation."}, {Number: 2, Page: 2, Text: "Our experiment compares retrieval precision and recall using held out data."}, {Number: 3, Page: 3, Text: "Experiments report precision and recall."}}
	out := Search(chunks, "retrieval precision", 2)
	if len(out) == 0 || out[0].Number != 2 {
		t.Fatalf("wrong ranking: %v", out)
	}
	if len(Search(chunks, "unmentionedconcept", 4)) != 0 {
		t.Fatal("invented match")
	}
}
