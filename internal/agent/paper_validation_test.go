package agent

import (
	"encoding/json"
	"errors"
	"signalwatch/internal/generation"
	"strings"
	"testing"
)

func TestPaperOutputRejectsForgeryAndAmbiguousJSON(t *testing.T) {
	evidence := []Citation{{ID: "p1-c1", Quote: "Accuracy was 80 percent on a held out dataset."}}
	valid := `{"status":"supported","claims":[{"text":"准确率为80%。","evidence":[{"id":"p1-c1"}]}]}`
	if _, err := decodeField([]byte(valid), evidence); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		strings.Replace(valid, "p1-c1", "p999-c1", 1),
		strings.Replace(valid, `"id":`, `"quote":"fabricated","id":`, 1),
		strings.Replace(valid, `"status":"supported"`, `"status":"supported","status":"supported"`, 1),
		strings.Replace(valid, `"text":`, `"tool":"search_document","text":`, 1),
		`{"status":"supported","claims":[]}`,
		`{"status":"not_stated","claims":null}`,
		valid + ` {}`,
	} {
		if _, err := decodeField([]byte(raw), evidence); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	claims := []reviewClaim{{ID: "results-1"}}
	for _, raw := range []string{`{"verdicts":[]}`, `{"verdicts":[{"id":"results-1"}]}`, `{"verdicts":[{"id":"results-2","supported":true}]}`, `{"verdicts":[{"id":"results-1","supported":true,"text":"invented"}]}`} {
		if _, err := decodeVerdicts([]byte(raw), claims); err == nil {
			t.Fatalf("accepted invalid review %s", raw)
		}
	}
}
func TestPaperPartitionDoesNotTruncateOversizedEvidence(t *testing.T) {
	pc := &PaperCheckpoint{}
	if _, err := partitionPaper(pc, []Citation{{ID: "oversized", Quote: strings.Repeat("a", paperInputLimit)}}); err == nil {
		t.Fatal("oversized chunk truncated")
	}
	evidence := []Citation{}
	for i := 0; i < 200; i++ {
		evidence = append(evidence, Citation{ID: strings.Repeat("x", i+1), Quote: strings.Repeat("word ", 100)})
	}
	batches, err := partitionPaper(pc, evidence)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, batch := range batches {
		raw, _ := json.Marshal(paperInput(pc, "all_fields", batch))
		if len(raw) > paperInputLimit {
			t.Fatal("unbounded request")
		}
		seen += len(batch)
	}
	if seen != len(evidence) {
		t.Fatal("lost evidence")
	}
}

func TestEvidenceBindingUsesServerTextAndIdentity(t *testing.T) {
	// Real Qwen regression: the model omitted this interleaved PDF footnote
	// when copying the sentence. Selection must retain every source byte.
	quote := "Most existing pipelines follow an offline decoupled workflow. This sequential 1 †Equal contribution. author. 2 ∗Corresponding dependency introduces prohibitive latency."
	evidence := []Citation{
		{ID: "p1-c1-s0", DocumentID: "current-version", ContentHash: "current-hash", Page: 1, Quote: quote, URL: "https://arxiv.org/pdf/2609.00610v1#page=1"},
		{ID: "p1-c1-s1000", DocumentID: "current-version", Page: 1, Quote: "Another source passage."},
	}
	raw, _ := json.Marshal(fieldOutput{Status: "supported", Claims: []claimOutput{{Text: "顺序工作流导致较高延迟。", Evidence: []evidenceOutput{{ID: evidence[0].ID}}}}})
	value, err := decodeField(raw, evidence)
	if err != nil || value.Claims[0].Evidence[0].Quote != quote {
		t.Fatalf("binding: %+v %v", value, err)
	}
	if _, err := decodeField(raw, evidence[1:]); err == nil {
		t.Fatal("accepted evidence outside this call")
	}
	cp := &Checkpoint{Paper: &PaperCheckpoint{Mode: "fulltext", ContentHash: "current-hash"}}
	_, _, citations := renderPaperResult(Run{Task: TaskPaperReport}, cp, []string{"problem"}, map[string]FieldAnalysis{"problem": value}, map[string][]Citation{"problem": evidence}, map[string]bool{"problem-1": true})
	if len(citations) != 1 || citations[0].Quote != quote || citations[0].Page != 1 || citations[0].ContentHash != "current-hash" || citations[0].URL != evidence[0].URL {
		t.Fatal(citations)
	}
	// Existing ID alone never establishes semantic support. False verdicts
	// remove the claim and its citations rather than publishing an empty success.
	_, result, citations := renderPaperResult(Run{Task: TaskPaperReport}, cp, []string{"problem"}, map[string]FieldAnalysis{"problem": value}, map[string][]Citation{"problem": evidence}, map[string]bool{"problem-1": false})
	if len(citations) != 0 || result.Fields["problem"].Status != "insufficient_evidence" {
		t.Fatal(result)
	}
}

func TestOutputFailureDiagnosticsDistinguishSchemaEvidenceAndReview(t *testing.T) {
	evidence := []Citation{{ID: "p1-c1", Quote: "Accuracy was 80 percent on the held out dataset."}}
	for _, tc := range []struct{ raw, code, path string }{
		{`{"status":`, "output_invalid_json", "$"},
		{`{"status":"not_stated","claims":null}`, "output_schema_mismatch", "$.claims"},
		{`{"status":"supported","claims":[{"text":"结果","evidence":[{"id":"invented"}]}]}`, "evidence_id_unknown", "$.claims[0].evidence[0].id"},
		{`{"status":"supported","claims":[{"text":"结果","evidence":[{"id":""}]}]}`, "evidence_id_unknown", "$.claims[0].evidence[0].id"},
		{`{"status":"supported","claims":[{"text":"结果","evidence":[{"id":"p1-c1","quote":"Accuracy was 80 percent"}]}]}`, "output_schema_mismatch", "$.claims[0].evidence[0]"},
	} {
		_, err := decodeField([]byte(tc.raw), evidence)
		var output *generation.OutputError
		if !errors.Is(err, ErrOutput) || !errors.As(err, &output) || output.Code != tc.code || output.Path != tc.path || paperFailureCode(err) != tc.code {
			t.Fatalf("%s: %+v", tc.raw, err)
		}
	}
	_, err := decodeVerdicts([]byte(`{"verdicts":[]}`), []reviewClaim{{ID: "results-1"}})
	if paperFailureCode(err) != "review_incomplete" {
		t.Fatal(err)
	}
	verdicts, err := decodeVerdicts([]byte(`{"verdicts":[{"id":"results-1","supported":false}]}`), []reviewClaim{{ID: "results-1"}})
	if err != nil || verdicts["results-1"] {
		t.Fatal("semantic rejection must remain a valid review")
	}
}

func TestChineseReportLanguageGuardPreservesTermsAndEvidence(t *testing.T) {
	evidence := []Citation{{ID: "p1-c1-s0", Quote: "The paper addresses the challenge of detecting brain metastases in MRI."}}
	for _, tc := range []struct {
		name   string
		text   string
		reject bool
	}{
		{"english problem", "The paper addresses the challenge of detecting brain metastases in MRI, where lesions vary widely in size and appearance.", true},
		{"english limitations", "Patch validation used a patch-level rather than patient-level split, meaning patch metrics do not evidence patient-independent generalization.", true},
		{"terms and formula", "采用3D U-Net处理MRI，融合权重α=0.60，F1从0.505提升至0.592。", false},
		{"chinese", "论文未进行患者层面的独立泛化验证。", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(fieldOutput{Status: "supported", Claims: []claimOutput{{Text: tc.text, Evidence: []evidenceOutput{{ID: evidence[0].ID}}}}})
			value, err := decodeReportField(raw, evidence)
			if tc.reject {
				var failure *generation.OutputError
				if !errors.As(err, &failure) || failure.Code != "output_language_mismatch" || failure.Path != "$.claims[0].text" || !generation.IsOutputFailure(failure.Code) {
					t.Fatalf("%+v", err)
				}
				// English followups remain supported; only fixed reports require Chinese.
				if _, err := decodeField(raw, evidence); err != nil {
					t.Fatal(err)
				}
			} else if err != nil || value.Claims[0].Text != tc.text || value.Claims[0].Evidence[0].Quote != evidence[0].Quote {
				t.Fatalf("%+v %v", value, err)
			}
		})
	}
	for _, status := range []string{"not_stated", "insufficient_evidence"} {
		raw, _ := json.Marshal(fieldOutput{Status: status, Claims: []claimOutput{}})
		if _, err := decodeReportField(raw, evidence); err != nil {
			t.Fatal(err)
		}
	}
}
