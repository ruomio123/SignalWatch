package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"signalwatch/internal/generation"
	"strings"
	"testing"
)

func answerTestQuestions(count int) []PaperQuestion {
	questions := make([]PaperQuestion, count)
	for i := range questions {
		questions[i] = PaperQuestion{ID: fmt.Sprintf("q%d", i+1), Question: fmt.Sprintf("问题 %d？", i+1), Query: fmt.Sprintf("question %d", i+1)}
	}
	return questions
}

func answerTestWire(counts ...int) paperAnswerOutput {
	wire := paperAnswerOutput{Answers: []paperAnswerPartOutput{}}
	for i, count := range counts {
		part := paperAnswerPartOutput{QuestionID: fmt.Sprintf("q%d", i+1), Status: "supported", Claims: []claimOutput{}}
		if count == 0 {
			part.Status = "insufficient_evidence"
		}
		for j := 0; j < count; j++ {
			part.Claims = append(part.Claims, claimOutput{Text: fmt.Sprintf("有依据的结论 %d/%d。", i+1, j+1), Evidence: []evidenceOutput{{ID: "source"}}})
		}
		wire.Answers = append(wire.Answers, part)
	}
	return wire
}

func answerTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPaperQuestionsAssignStableIDsAndEnforceBounds(t *testing.T) {
	for _, count := range []int{0, 1, 4, 5} {
		wire := paperQuestionsOutput{Questions: []paperQuestionItemOutput{}}
		for i := 0; i < count; i++ {
			wire.Questions = append(wire.Questions, paperQuestionItemOutput{Question: "方法和实验分别是什么？", Query: "method experiment"})
		}
		questions, err := decodePaperQuestions(answerTestJSON(t, wire))
		if count == 0 || count == 5 {
			if err == nil || repairablePaperLimit(err) {
				t.Fatalf("count %d accepted or repairable: %v", count, err)
			}
			continue
		}
		if err != nil || len(questions) != count {
			t.Fatalf("count %d: %+v %v", count, questions, err)
		}
		for i, question := range questions {
			if question.ID != fmt.Sprintf("q%d", i+1) || question.Question != wire.Questions[i].Question || question.Query != wire.Questions[i].Query {
				t.Fatalf("question not preserved: %+v", question)
			}
		}
	}
	for _, raw := range []string{
		`{"questions":null}`,
		`{"questions":[{"question":" ","query":"search"}]}`,
		`{"questions":[{"question":"Q?","query":" "}]}`,
		`{"questions":[{"question":"Q?","query":"search","id":"q1"}]}`,
		`{"questions":[{"question":"Q?","query":"search","query":"other"}]}`,
	} {
		if _, err := decodePaperQuestions([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, overflow := range []bool{false, true} {
		extra := ""
		if overflow {
			extra = "a"
		}
		for _, item := range []paperQuestionItemOutput{
			{Question: strings.Repeat("a", 8000) + extra, Query: "search"},
			{Question: "Q?", Query: strings.Repeat("😀", 250) + extra},
		} {
			_, err := decodePaperQuestions(answerTestJSON(t, paperQuestionsOutput{Questions: []paperQuestionItemOutput{item}}))
			if (err != nil) != overflow || repairablePaperLimit(err) {
				t.Fatalf("question/query byte bound: overflow=%t err=%v", overflow, err)
			}
		}
	}
}

func TestPaperAnswerGlobalClaimBudgetAndStableQuestionOrder(t *testing.T) {
	evidence := []Citation{{ID: "source", Quote: "Immutable source."}}
	questions := answerTestQuestions(4)
	for _, counts := range [][]int{{1, 1, 1, 2}, {1, 2, 1, 2}, {2, 2, 1, 2}} {
		wire := answerTestWire(counts...)
		wire.Answers[0], wire.Answers[3] = wire.Answers[3], wire.Answers[0]
		analysis, err := decodePaperAnswer(answerTestJSON(t, wire), evidence, questions)
		total := counts[0] + counts[1] + counts[2] + counts[3]
		if total > 6 {
			assertPaperLimit(t, err, "$.answers", total, 6, "claims", true)
		} else if err != nil {
			t.Fatal(err)
		}
		if len(analysis.Answers) != 4 || analysis.Answers[0].QuestionID != "q1" || analysis.Answers[3].QuestionID != "q4" {
			t.Fatalf("noncanonical order: %+v", analysis)
		}
		for i, part := range analysis.Answers {
			if len(part.Claims) != counts[i] || part.Claims[0].Evidence[0].Quote != evidence[0].Quote {
				t.Fatalf("lost claims or canonical source: %+v", part)
			}
		}
		claims := paperAnswerReviewClaims(analysis)
		if claims[0].ID != "q1-1" || claims[len(claims)-1].ID != "q4-2" || len(claims) != total {
			t.Fatalf("review IDs: %+v", claims)
		}
	}
}

func TestPaperAnswerTextLimitCountsDecodedUTF8Bytes(t *testing.T) {
	evidence := []Citation{{ID: "source", Quote: "source"}}
	for _, value := range []string{strings.Repeat("a", 1200), strings.Repeat("中", 400), strings.Repeat("😀", 300), strings.Repeat("\"\\\n", 400), strings.Repeat("中a😀", 150)} {
		for _, extra := range []string{"", "a"} {
			wire := answerTestWire(1)
			wire.Answers[0].Claims[0].Text = value + extra
			analysis, err := decodePaperAnswer(answerTestJSON(t, wire), evidence, answerTestQuestions(1))
			if extra != "" {
				assertPaperLimit(t, err, "$.answers[0].claims[0].text", 1201, 1200, "utf8_bytes", true)
			} else if err != nil || analysis.Answers[0].Claims[0].Text != value {
				t.Fatalf("byte boundary: %v", err)
			}
		}
	}
	// JSON character escapes are measured after decoding, not on the wire.
	raw := `{"answers":[{"question_id":"q1","status":"supported","claims":[{"text":"` + strings.Repeat(`\u4e2d`, 400) + `","evidence":[{"id":"source"}]}]}]}`
	if _, err := decodePaperAnswer([]byte(raw), evidence, answerTestQuestions(1)); err != nil {
		t.Fatal(err)
	}
}

func TestPaperAnswerNeverRepairsMixedStructuralOrEvidenceFailures(t *testing.T) {
	evidence := []Citation{{ID: "source", Quote: "source"}}
	cases := map[string]func(*paperAnswerOutput){
		"unknown question":        func(w *paperAnswerOutput) { w.Answers[1].QuestionID = "q9" },
		"duplicate question":      func(w *paperAnswerOutput) { w.Answers[1].QuestionID = "q1" },
		"missing question":        func(w *paperAnswerOutput) { w.Answers = w.Answers[:1] },
		"invalid status":          func(w *paperAnswerOutput) { w.Answers[1].Status = "not_stated" },
		"empty supported":         func(w *paperAnswerOutput) { w.Answers[1].Claims = []claimOutput{} },
		"empty partial":           func(w *paperAnswerOutput) { w.Answers[1].Status = "partial"; w.Answers[1].Claims = []claimOutput{} },
		"insufficient with claim": func(w *paperAnswerOutput) { w.Answers[1].Status = "insufficient_evidence" },
		"empty text":              func(w *paperAnswerOutput) { w.Answers[1].Claims[0].Text = " " },
		"no evidence":             func(w *paperAnswerOutput) { w.Answers[1].Claims[0].Evidence = []evidenceOutput{} },
		"unknown evidence":        func(w *paperAnswerOutput) { w.Answers[1].Claims[0].Evidence[0].ID = "old-history-id" },
		"duplicate evidence": func(w *paperAnswerOutput) {
			w.Answers[1].Claims[0].Evidence = []evidenceOutput{{ID: "source"}, {ID: "source"}}
		},
		"too much evidence": func(w *paperAnswerOutput) {
			w.Answers[1].Claims[0].Evidence = []evidenceOutput{{ID: "source"}, {ID: "source"}, {ID: "source"}, {ID: "source"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			wire := answerTestWire(6, 1)
			wire.Answers[0].Claims[0].Text = strings.Repeat("a", 1201)
			mutate(&wire)
			_, err := decodePaperAnswer(answerTestJSON(t, wire), evidence, answerTestQuestions(2))
			if err == nil || repairablePaperLimit(err) {
				t.Fatalf("mixed invalid answer authorized repair: %v", err)
			}
		})
	}
	for _, raw := range []string{
		`{"answers":[]}`,
		`{"answers":null}`,
		`{"answers":[{"question_id":"q1","status":"insufficient_evidence","claims":null}]}`,
		`{"answers":[{"question_id":"q1","status":"insufficient_evidence","claims":[],"gap":"author omitted results"}]}`,
		`{"answers":[{"question_id":"q1","question_id":"q1","status":"insufficient_evidence","claims":[]}]}`,
		`{"answers":[{"question_id":"q1","status":"insufficient_evidence","claims":[]}],"summary":"unsupported fact"}`,
		strings.Repeat(" ", paperResponseLimit) + `{}`,
	} {
		if _, err := decodePaperAnswer([]byte(raw), evidence, answerTestQuestions(1)); err == nil || repairablePaperLimit(err) {
			t.Fatalf("malformed answer accepted/repaired: %v", err)
		}
	}
}

func TestPaperAnswerSchemaAndPromptShareLimitsWithoutMutatingProjection(t *testing.T) {
	schema := paperAnswerSchemas.full
	before := answerTestJSON(t, schema)
	answers := schema.Properties["answers"]
	claims := answers.Items.Properties["claims"]
	refs := claims.Items.Properties["evidence"]
	if *answers.MaxItems != 4 || *claims.MaxItems != 6 || *refs.MinItems != 1 || *refs.MaxItems != 3 {
		t.Fatal("missing array constraints")
	}
	if strings.Contains(string(before), "maxLength") {
		t.Fatal("character count substituted for byte count")
	}
	for _, expected := range []string{"at most 6 claims", "1200 UTF-8 bytes", "1-3", "50000 UTF-8 bytes"} {
		if !strings.Contains(paperAnswerPrompt, expected) {
			t.Fatal(paperAnswerPrompt)
		}
	}
	_ = schema.Structural()
	if !reflect.DeepEqual(before, answerTestJSON(t, schema)) || paperAnswerSchemas.structure.Properties["answers"].MaxItems != nil {
		t.Fatal("schema projection mutated bounds")
	}
}

func TestPaperAnswerReviewRecomputesCoverageAndPublishesOnlySupportedFacts(t *testing.T) {
	evidence := []Citation{{ID: "source", Quote: "Pinned original passage.", DocumentID: "document", ContentHash: "hash", Page: 8, URL: "https://arxiv.org/pdf/1706.03762v1#page=8"}}
	questions := answerTestQuestions(2)
	for _, tc := range []struct {
		name      string
		partial   bool
		verdicts  map[string]bool
		status    string
		parts     []string
		citations int
		gap       string
	}{
		{"complete", false, map[string]bool{"q1-1": true, "q1-2": true, "q2-1": true}, "complete", []string{"supported", "supported"}, 3, ""},
		{"initial partial", true, map[string]bool{"q1-1": true, "q1-2": true, "q2-1": true}, "partial", []string{"partial", "supported"}, 3, "insufficient_evidence"},
		{"one rejected", false, map[string]bool{"q1-1": true, "q1-2": false, "q2-1": true}, "partial", []string{"partial", "supported"}, 2, "review_rejected"},
		{"part rejected", false, map[string]bool{"q1-1": false, "q1-2": false, "q2-1": true}, "partial", []string{"insufficient_evidence", "supported"}, 1, "review_rejected"},
		{"all rejected", false, map[string]bool{}, "insufficient", []string{"insufficient_evidence", "insufficient_evidence"}, 0, "review_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := answerTestWire(2, 1)
			if tc.partial {
				wire.Answers[0].Status = "partial"
			}
			analysis, err := decodePaperAnswer(answerTestJSON(t, wire), evidence, questions)
			if err != nil {
				t.Fatal(err)
			}
			cp := &Checkpoint{DocumentID: "document", Paper: &PaperCheckpoint{Mode: "fulltext", PaperHash: "paper", ContentHash: "hash", SourceVersion: "1706.03762v1"}}
			content, result, citations := renderPaperAnswer(Run{WorkflowVersion: PaperWorkflowVersion}, cp, questions, analysis, evidence, tc.verdicts)
			if result.Answer.Status != tc.status || len(citations) != tc.citations || result.Report != nil || result.Coverage != "retrieved_passages" {
				t.Fatalf("unexpected result: %+v, citations=%+v", result, citations)
			}
			for i, part := range result.Answer.Parts {
				if part.Status != tc.parts[i] || part.QuestionID != questions[i].ID || part.Question != questions[i].Question || !strings.Contains(content, part.Question) {
					t.Fatalf("part metadata/status lost: %+v", part)
				}
			}
			gap := result.Answer.Parts[0].Gap
			if (tc.gap == "" && gap != nil) || (tc.gap != "" && (gap == nil || gap.Reason != tc.gap)) {
				t.Fatalf("wrong gap: %+v", gap)
			}
			if !tc.verdicts["q1-2"] && strings.Contains(content, wire.Answers[0].Claims[1].Text) {
				t.Fatal("rejected fact leaked into history/Markdown")
			}
			for _, citation := range citations {
				if citation.Quote != evidence[0].Quote || citation.DocumentID != evidence[0].DocumentID || citation.ContentHash != evidence[0].ContentHash || citation.URL != evidence[0].URL || citation.Page != 8 || !strings.Contains(content, "["+citation.ID+"]") {
					t.Fatalf("citation is not the pinned source: %+v", citation)
				}
			}
			encoded := string(answerTestJSON(t, result))
			if strings.Contains(encoded, "query") || strings.Contains(encoded, "Pinned original") {
				t.Fatal("internal query/evidence duplicated into public answer")
			}
		})
	}
}

func TestPaperAnswerAllMissingUsesControlledGapsAndAbstractScope(t *testing.T) {
	questions := answerTestQuestions(2)
	analysis, err := decodePaperAnswer(answerTestJSON(t, answerTestWire(0, 0)), nil, questions)
	if err != nil {
		t.Fatal(err)
	}
	content, result, citations := renderPaperAnswer(Run{}, &Checkpoint{Paper: &PaperCheckpoint{Mode: "abstract"}}, questions, analysis, nil, map[string]bool{})
	if result.Answer.Status != "insufficient" || result.Coverage != "abstract_only" || len(citations) != 0 || !strings.Contains(content, "不代表论文全文没有相关内容") || len(paperAnswerReviewClaims(analysis)) != 0 {
		t.Fatalf("missing scope: %+v %s", result, content)
	}
	for _, part := range result.Answer.Parts {
		if len(part.Claims) != 0 || part.Gap == nil || part.Gap.Reason != "insufficient_evidence" {
			t.Fatalf("missing safe gap: %+v", part)
		}
	}
}

func TestPaperAnswerRepairCollectsReferencesAcrossParts(t *testing.T) {
	wire := answerTestWire(1, 1)
	wire.Answers[1].Claims[0].Evidence = []evidenceOutput{{ID: "second"}}
	refs, err := paperAnswerCandidateEvidence(answerTestJSON(t, wire))
	if err != nil || !reflect.DeepEqual(refs, []evidenceOutput{{ID: "source"}, {ID: "second"}}) {
		t.Fatalf("lost repair source: %+v %v", refs, err)
	}
	_, err = decodePaperAnswer(answerTestJSON(t, wire), []Citation{{ID: "source"}}, answerTestQuestions(2))
	var failure *generation.OutputError
	if !errors.As(err, &failure) || failure.Code != "evidence_id_unknown" || repairablePaperLimit(err) {
		t.Fatalf("unknown reference accepted: %v", err)
	}
}
