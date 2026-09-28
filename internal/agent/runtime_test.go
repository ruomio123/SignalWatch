package agent

import (
	"encoding/json"
	"testing"
)

func TestActionProtocolRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	for _, raw := range []string{`{"type":"answer","content":"ok","sql":"drop"}`, `{"type":"answer","content":"ok"} {}`, `{"type":"execute","content":"ok"}`, `{"type":"tool_call"}`, `{"type":"answer","content":""}`} {
		if _, err := DecodeAction([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := DecodeAction([]byte(`{"type":"tool_call","tool":"search_document","arguments":{"query":"method"}}`)); err != nil {
		t.Fatal(err)
	}
}
func TestCitationValidationUsesOnlyReturnedEvidence(t *testing.T) {
	evidence := []Citation{{ID: "p1-c1", DocumentID: "doc", ContentHash: "hash", Page: 1, Quote: "A randomized trial showed improved accuracy.", URL: "https://arxiv.org/pdf/1706.03762v1#page=1"}}
	a := Action{Type: "answer", Content: "Evidence [p1-c1]", Citations: []Citation{{ID: "p1-c1", Quote: "randomized trial", URL: "https://evil.test"}}}
	out, err := ValidateCitations(a, evidence)
	if err != nil || out[0].URL != evidence[0].URL || out[0].ContentHash != "hash" {
		t.Fatalf("server metadata missing: %v %v", out, err)
	}
	for _, mutation := range []func(*Action){func(a *Action) { a.Citations[0].ID = "p99-c9" }, func(a *Action) { a.Citations[0].Quote = "unreported numbers" }, func(a *Action) { a.Content = "no evidence marker" }} {
		raw, _ := json.Marshal(a)
		var copy Action
		_ = json.Unmarshal(raw, &copy)
		mutation(&copy)
		if _, err := ValidateCitations(copy, evidence); err == nil {
			t.Fatal("accepted forged citation")
		}
	}
	if _, err := ValidateCitations(Action{InsufficientEvidence: true}, evidence); err != nil {
		t.Fatal(err)
	}
}

func TestFullChunkReplacesOutlineExcerptForCitationValidation(t *testing.T) {
	old := []Citation{{ID: "p1-c1", DocumentID: "d", ContentHash: "h", Quote: "Introduction."}}
	merged := mergeEvidence(old, []Citation{{ID: "p1-c1", DocumentID: "d", ContentHash: "h", Quote: "Introduction. The experiment uses a held out dataset."}})
	a := Action{Content: "Evaluation [p1-c1]", Citations: []Citation{{ID: "p1-c1", Quote: "held out dataset"}}}
	if _, err := ValidateCitations(a, merged); err != nil {
		t.Fatal("later tool evidence discarded", err)
	}
}
