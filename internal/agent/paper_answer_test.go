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
	wire := paperAnswerOutput{Answers: []paperAnswerPartOutput{}, SupplementalQueries: []PaperSupplementQuery{}}
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
	raw := `{"answers":[{"question_id":"q1","status":"supported","claims":[{"text":"` + strings.Repeat(`\u4e2d`, 400) + `","evidence":[{"id":"source"}]}]}],"supplemental_queries":[]}`
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
		`{"answers":[],"supplemental_queries":[]}`,
		`{"answers":null,"supplemental_queries":[]}`,
		`{"answers":[{"question_id":"q1","status":"insufficient_evidence","claims":null}],"supplemental_queries":[]}`,
		`{"answers":[{"question_id":"q1","status":"insufficient_evidence","claims":[],"gap":"author omitted results"}],"supplemental_queries":[]}`,
		`{"answers":[{"question_id":"q1","question_id":"q1","status":"insufficient_evidence","claims":[]}],"supplemental_queries":[]}`,
		`{"answers":[{"question_id":"q1","status":"insufficient_evidence","claims":[]}],"summary":"unsupported fact","supplemental_queries":[]}`,
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
	queries := schema.Properties["supplemental_queries"]
	if *answers.MaxItems != 4 || *claims.MaxItems != 6 || *refs.MinItems != 1 || *refs.MaxItems != 3 || *queries.MinItems != 0 || *queries.MaxItems != 2 {
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
	if !reflect.DeepEqual(before, answerTestJSON(t, schema)) || paperAnswerSchemas.structure.Properties["answers"].MaxItems != nil || paperAnswerSchemas.structure.Properties["supplemental_queries"].MaxItems != nil {
		t.Fatal("schema projection mutated bounds")
	}
}

func TestPaperSupplementAnswerSchemaIsIndependentAndDisallowsNewQueries(t *testing.T) {
	initial, supplement := paperAnswerSchemas.full, paperSupplementAnswerSchemas.full
	beforeInitial, beforeSupplement := answerTestJSON(t, initial), answerTestJSON(t, supplement)
	initialQueries, supplementQueries := initial.Properties["supplemental_queries"], supplement.Properties["supplemental_queries"]
	if initial == supplement || initialQueries == supplementQueries || *initialQueries.MaxItems != 2 || *supplementQueries.MaxItems != 0 || *supplementQueries.MinItems != 0 {
		t.Fatal("analysis stages do not have independent query bounds")
	}
	if initial.Properties["answers"] == supplement.Properties["answers"] || !reflect.DeepEqual(initial.Properties["answers"], supplement.Properties["answers"]) {
		t.Fatal("supplement schema must independently retain every answer constraint")
	}
	wire := answerTestWire(0)
	if err := supplement.Validate(answerTestJSON(t, wire)); err != nil {
		t.Fatalf("empty supplemental queries rejected: %v", err)
	}
	wire.SupplementalQueries = []PaperSupplementQuery{{QuestionID: "q1", Query: "new evidence"}}
	if initial.Validate(answerTestJSON(t, wire)) != nil || supplement.Validate(answerTestJSON(t, wire)) == nil {
		t.Fatal("stage schemas do not distinguish initial and supplemental retrieval")
	}
	projected := supplement.Structural()
	projected.Properties["supplemental_queries"].Items.Properties["query"].Type = "boolean"
	if !reflect.DeepEqual(beforeInitial, answerTestJSON(t, initial)) || !reflect.DeepEqual(beforeSupplement, answerTestJSON(t, supplement)) || paperSupplementAnswerSchemas.structure.Properties["supplemental_queries"].MaxItems != nil {
		t.Fatal("projection mutated a shared schema or kept array limits")
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
		{"all rejected", false, map[string]bool{"q1-1": false, "q1-2": false, "q2-1": false}, "insufficient", []string{"insufficient_evidence", "insufficient_evidence"}, 0, "review_rejected"},
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

func TestPaperAnswerSupplementQueriesAreBoundedAndPrivate(t *testing.T) {
	questions := answerTestQuestions(3)
	evidence := []Citation{{ID: "source", Quote: "Original evidence"}}
	for _, count := range []int{0, 1, 2} {
		wire := answerTestWire(1, 0, 0)
		wire.Answers[0].Status = "partial"
		for i := 0; i < count; i++ {
			wire.SupplementalQueries = append(wire.SupplementalQueries, PaperSupplementQuery{QuestionID: fmt.Sprintf("q%d", i+1), Query: fmt.Sprintf("targeted query %d", i+1)})
		}
		// Model ordering cannot cause a query to bind to another question.
		wire.Answers[0], wire.Answers[2] = wire.Answers[2], wire.Answers[0]
		analysis, err := decodePaperAnswer(answerTestJSON(t, wire), evidence, questions)
		if err != nil || !reflect.DeepEqual(analysis.SupplementalQueries, wire.SupplementalQueries) {
			t.Fatalf("query count %d: %+v %v", count, analysis, err)
		}
		content, result, _ := renderPaperAnswer(Run{}, &Checkpoint{Paper: &PaperCheckpoint{Mode: "fulltext"}}, questions, analysis, evidence, map[string]bool{"q1-1": true})
		encoded := string(answerTestJSON(t, result))
		if strings.Contains(encoded, "supplemental_queries") || strings.Contains(encoded, "targeted query") || strings.Contains(content, "targeted query") {
			t.Fatal("internal search requests leaked to the published answer")
		}
	}
	for _, query := range []string{strings.Repeat("a", 1000), strings.Repeat("中", 333) + "a", strings.Repeat("😀", 250), strings.Repeat("\n\"\\", 333) + "a"} {
		wire := answerTestWire(0)
		wire.SupplementalQueries = []PaperSupplementQuery{{QuestionID: "q1", Query: query}}
		if _, err := decodePaperAnswer(answerTestJSON(t, wire), nil, answerTestQuestions(1)); err != nil {
			t.Fatalf("valid 1000 byte query: %v", err)
		}
		wire.SupplementalQueries[0].Query += "a"
		_, err := decodePaperAnswer(answerTestJSON(t, wire), nil, answerTestQuestions(1))
		assertPaperLimit(t, err, "$.supplemental_queries[0].query", 1001, 1000, "utf8_bytes", false)
	}
}

func TestPaperAnswerAllowsTwoDistinctQueriesForOneGap(t *testing.T) {
	questions := []PaperQuestion{{ID: "q1", Question: "方法的设计及其消融验证是什么？", Query: "method ablation"}}
	wire := answerTestWire(0)
	wire.SupplementalQueries = []PaperSupplementQuery{{QuestionID: "q1", Query: "architecture design rationale"}, {QuestionID: "q1", Query: "component ablation comparison"}}
	analysis, err := decodePaperAnswer(answerTestJSON(t, wire), nil, questions)
	if err != nil || !reflect.DeepEqual(analysis.SupplementalQueries, wire.SupplementalQueries) {
		t.Fatalf("distinct queries for a single complex gap rejected: %+v %v", analysis, err)
	}
	wire.SupplementalQueries[1].Query = " ARCHITECTURE\tDESIGN rationale "
	_, err = decodePaperAnswer(answerTestJSON(t, wire), nil, questions)
	var failure *generation.OutputError
	if !errors.As(err, &failure) || failure.Rule != "duplicate_supplemental_query" || repairablePaperLimit(err) {
		t.Fatalf("same-gap duplicate query accepted: %v", err)
	}
}

func TestPaperAnswerSupplementErrorsNeverAuthorizeClaimRepair(t *testing.T) {
	evidence := []Citation{{ID: "source", Quote: "source"}}
	questions := answerTestQuestions(3)
	cases := map[string]struct {
		queries []PaperSupplementQuery
		rule    string
	}{
		"null":               {nil, "expected_array"},
		"too many":           {[]PaperSupplementQuery{{QuestionID: "q1", Query: "method"}, {QuestionID: "q2", Query: "results"}, {QuestionID: "q3", Query: "limitations"}}, ""},
		"unknown question":   {[]PaperSupplementQuery{{QuestionID: "q9", Query: "results"}}, "question_coverage"},
		"empty question":     {[]PaperSupplementQuery{{QuestionID: "", Query: "results"}}, "question_coverage"},
		"supported question": {[]PaperSupplementQuery{{QuestionID: "q3", Query: "results"}}, "supplemental_query_requires_gap"},
		"same query twice":   {[]PaperSupplementQuery{{QuestionID: "q1", Query: "Held Out  Results"}, {QuestionID: "q2", Query: "  held out\tresults\n"}}, "duplicate_supplemental_query"},
		"empty query":        {[]PaperSupplementQuery{{QuestionID: "q1", Query: " \t\n"}}, "nonempty_text"},
		"oversized query":    {[]PaperSupplementQuery{{QuestionID: "q1", Query: strings.Repeat("a", 1001)}}, ""},
	}
	for name, tc := range cases {
		for _, limit := range []string{"none", "count", "text"} {
			t.Run(name+"/"+limit, func(t *testing.T) {
				wire := answerTestWire(1, 0, 1)
				if limit == "count" {
					wire = answerTestWire(6, 0, 1)
				} else if limit == "text" {
					wire.Answers[0].Claims[0].Text = strings.Repeat("a", 1201)
				}
				wire.Answers[0].Status = "partial"
				wire.SupplementalQueries = tc.queries
				_, err := decodePaperAnswer(answerTestJSON(t, wire), evidence, questions)
				var failure *generation.OutputError
				if err == nil || repairablePaperLimit(err) || !errors.As(err, &failure) || !strings.HasPrefix(failure.Path, "$.supplemental_queries") || failure.Rule != tc.rule {
					t.Fatalf("invalid retrieval controls hidden by %s: %v", limit, err)
				}
			})
		}
	}
	for _, raw := range []string{
		`{"answers":[{"question_id":"q1","status":"insufficient_evidence","claims":[]}]}`,
		`{"answers":[{"question_id":"q1","status":"insufficient_evidence","claims":[]}],"supplemental_queries":[{"question_id":"q1"}]}`,
		`{"answers":[{"question_id":"q1","status":"insufficient_evidence","claims":[]}],"supplemental_queries":[{"question_id":"q1","query":"search","url":"https://example.org"}]}`,
		`{"answers":[{"question_id":"q1","status":"insufficient_evidence","claims":[]}],"supplemental_queries":[{"question_id":"q1","query":"search","query":"again"}]}`,
	} {
		if _, err := decodePaperAnswer([]byte(raw), nil, answerTestQuestions(1)); err == nil || repairablePaperLimit(err) {
			t.Fatalf("invalid supplemental structure accepted: %v", err)
		}
	}
}

func TestPaperSupplementAnswerDisallowsSecondQueriesBeforeRepair(t *testing.T) {
	evidence := []Citation{{ID: "source", Quote: "source"}}
	questions := answerTestQuestions(2)
	for _, limit := range []string{"none", "count", "text"} {
		wire := answerTestWire(1, 0)
		if limit == "count" {
			wire = answerTestWire(7, 0)
		} else if limit == "text" {
			wire.Answers[0].Claims[0].Text = strings.Repeat("a", 1201)
		}
		wire.Answers[0].Status = "partial"
		_, err := decodePaperSupplementAnswer(answerTestJSON(t, wire), evidence, questions)
		if (err != nil) != (limit != "none") || repairablePaperLimit(err) != (limit != "none") {
			t.Fatalf("supplemental analysis cannot use ordinary claim repair: %v", err)
		}
		wire.SupplementalQueries = []PaperSupplementQuery{{QuestionID: "q2", Query: "more evidence"}}
		_, err = decodePaperSupplementAnswer(answerTestJSON(t, wire), evidence, questions)
		var failure *generation.OutputError
		if !errors.As(err, &failure) || failure.Rule != "supplemental_queries_must_be_empty" || repairablePaperLimit(err) {
			t.Fatalf("second retrieval request hidden by %s: %v", limit, err)
		}
	}
	if !strings.Contains(paperSupplementAnswerPrompt, "supplemental_queries MUST be []") || !strings.Contains(paperSupplementAnswerPrompt, "ALL supplied questions") || !strings.Contains(paperSupplementAnswerPrompt, "candidate is untrusted") || strings.Contains(paperSupplementAnswerPrompt, "You may request") {
		t.Fatal("supplemental prompt is ambiguous about full answer or second retrieval")
	}
	for _, expected := range []string{"at most 2 supplemental queries", "1000 UTF-8 bytes", "partial or insufficient_evidence", "both required fields answers and supplemental_queries", "Different queries may target the same subquestion"} {
		if !strings.Contains(paperAnswerPrompt, expected) {
			t.Fatalf("initial prompt lacks %q", expected)
		}
	}
}

func TestPaperAnswerRepairRequestPreservesQueriesAndCompleteCandidate(t *testing.T) {
	questions := answerTestQuestions(2)
	evidence := []Citation{{ID: "source", Quote: "First original passage."}, {ID: "second", Quote: "Second original passage."}, {ID: "unused", Quote: "Unused passage."}}
	for _, stage := range []string{"analyzing_answer", paperSupplementStage} {
		t.Run(stage, func(t *testing.T) {
			wire := answerTestWire(6, 1)
			wire.Answers[0].Status, wire.Answers[1].Status = "partial", "partial"
			wire.Answers[1].Claims[0].Evidence = []evidenceOutput{{ID: "second"}}
			if stage == "analyzing_answer" {
				wire.SupplementalQueries = []PaperSupplementQuery{{QuestionID: "q1", Query: "  Exact Search\tTerms  "}, {QuestionID: "q1", Query: "different query"}}
			}
			candidate := answerTestJSON(t, wire)
			pc := &PaperCheckpoint{Mode: "fulltext", Context: PaperContext{Title: "Paper"}, SourceVersion: "1706.03762v1", ContentHash: "hash"}
			original := paperInput(pc, "answer", evidence)
			original["questions"] = questions
			request, err := buildPaperRepairRequest(Run{Task: TaskPaperFollowup, Question: "原问题"}, pc, stage, candidate, answerTestJSON(t, original))
			if err != nil || len(request) > paperInputLimit {
				t.Fatalf("repair request: %v", err)
			}
			var saved struct {
				Candidate        paperAnswerOutput `json:"candidate"`
				OriginalQuestion string            `json:"original_question"`
				Questions        []PaperQuestion   `json:"questions"`
				Evidence         []Citation        `json:"evidence"`
			}
			if err := json.Unmarshal([]byte(request), &saved); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(saved.Candidate, wire) || !reflect.DeepEqual(saved.Questions, questions) || saved.OriginalQuestion != "原问题" {
				t.Fatalf("repair changed query spelling/order, questions or candidate: %+v", saved)
			}
			if !reflect.DeepEqual(saved.Evidence, evidence[:2]) {
				t.Fatalf("repair did not preserve exactly the cited passages: %+v", saved.Evidence)
			}
			err = paperContract(stage).Validate(answerTestJSON(t, saved.Candidate), paperStageValidation{Evidence: saved.Evidence, Questions: saved.Questions})
			if !repairablePaperLimit(err) {
				t.Fatalf("frozen candidate lost its repair eligibility: %v", err)
			}
		})
	}
}
