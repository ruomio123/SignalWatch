package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"signalwatch/internal/generation"
	"strings"
	"testing"
	"time"
)

func TestPaperReportPackingUsesModelBudgetAndWholePassages(t *testing.T) {
	pc := &PaperCheckpoint{Limits: generation.ModelLimits{ContextTokens: 16000, MaxOutputTokens: 1024}, Mode: "fulltext"}
	useful := Citation{ID: "small", Quote: strings.Repeat("source ", 60)}
	input, err := packPaperReportInput(pc, "method", []Citation{
		{ID: "large", Quote: strings.Repeat("x", 16000), SourceType: "html"},
		useful, useful,
	})
	must(t, err)
	if !reflect.DeepEqual(input.Evidence, []Citation{useful}) || pc.Coverage != "retrieved_passages" || pc.StructuredGap != "input_budget" {
		t.Fatalf("oversized structure displaced usable text or evidence changed: %+v %+v", input, pc)
	}
	if !generation.RequestFits(pc.Limits, paperPolicy+"\n"+fieldPromptFor("method", true), []byte(input.Request), paperStageSchema("analyzing_method"), paperOutputTokens(pc, "analyzing_method")) {
		t.Fatal("packed request exceeds model context")
	}
}

func TestPaperAnswerPackingReducesHistoryBeforeEvidence(t *testing.T) {
	pc := &PaperCheckpoint{Limits: generation.ModelLimits{ContextTokens: 16000, MaxOutputTokens: 1024}, Mode: "fulltext", ConversationContext: PaperConversationContext{Turns: []PaperConversationTurn{{Question: "previous", Answer: strings.Repeat("historical context ", 900)}}}}
	snapshot := copyPaperConversationContext(pc.ConversationContext)
	questions := []PaperQuestion{{ID: "q1", Question: "论文方法是什么？", Query: "method"}}
	evidence := []Citation{{ID: "p1-c1-s0", Quote: strings.Repeat("method ", 90)}}
	input, err := packPaperAnswerInput(Run{Task: TaskPaperFollowup, Question: "论文方法是什么？"}, pc, questions, evidence)
	must(t, err)
	if !reflect.DeepEqual(input.Evidence, evidence) || !reflect.DeepEqual(pc.ConversationContext, snapshot) {
		t.Fatal("history projection removed evidence or mutated frozen history")
	}
	var request struct {
		ConversationContext PaperConversationContext `json:"conversation_context"`
	}
	must(t, json.Unmarshal([]byte(input.Request), &request))
	if len(request.ConversationContext.Turns) != 0 || !request.ConversationContext.Truncated {
		t.Fatal("oversized optional history was retained")
	}
	must(t, paperFitsSerialized(pc, "analyzing_answer", paperAnswerPrompt, []byte(input.Request)))
}

func TestPaperReviewModelBudgetSplitsAllClaims(t *testing.T) {
	pc := &PaperCheckpoint{Limits: generation.ModelLimits{ContextTokens: 16000, MaxOutputTokens: 1024}}
	claims, sources := reviewFixture(25, 650)
	for id, source := range sources {
		if id != "shared" {
			source.Quote = strings.Repeat("x", 650)
			sources[id] = source
		}
	}
	plan, err := planPaperReview(pc, TaskPaperReport, claims, sources)
	must(t, err)
	if len(plan) < 2 {
		t.Fatal("model budget did not split review despite business input fitting 64 KiB")
	}
	must(t, validatePaperReviewPlan(plan, TaskPaperReport, claims))
	for i, batch := range plan {
		must(t, paperFitsSerialized(pc, paperReviewStage(i), verdictPrompt, []byte(batch.Request)))
	}
}

func TestPaperAnswerDistinguishesMissingReviewFromRejection(t *testing.T) {
	pc := &PaperCheckpoint{Mode: "fulltext", Issues: []PaperIssue{{Stage: "validating_paper_2", Code: "output_invalid_json"}}}
	questions := []PaperQuestion{{ID: "q1", Question: "方法？"}, {ID: "q2", Question: "结果？"}}
	evidence := []Citation{{ID: "source", Quote: "source text", URL: "https://example.test/paper"}}
	claim := func(text string) PaperClaim {
		return PaperClaim{Text: text, Evidence: []EvidenceRef{{ID: "source", Quote: "source text"}}}
	}
	analysis := PaperAnswerAnalysis{Answers: []PaperAnswerAnalysisPart{
		{QuestionID: "q1", Status: "supported", Claims: []PaperClaim{claim("已审核事实。"), claim("未审核事实。")}},
		{QuestionID: "q2", Status: "supported", Claims: []PaperClaim{claim("被拒绝事实。")}},
	}}
	verdicts := map[string]bool{"q1-1": true, "q2-1": false}
	content, result, refs := renderPaperAnswer(Run{}, &Checkpoint{Paper: pc}, questions, analysis, evidence, verdicts)
	if result.Outcome != "partial" || result.Answer.Parts[0].Gap.Reason != "review_incomplete" || result.Answer.Parts[1].Gap.Reason != "review_rejected" || len(refs) != 1 {
		t.Fatalf("review states conflated: %+v", result)
	}
	if strings.Contains(content, "未审核事实") || strings.Contains(content, "被拒绝事实") || !strings.Contains(content, "未完成证据审核") {
		t.Fatalf("unsafe or misleading partial content: %s", content)
	}
	must(t, paperPublicationError(pc, verdicts))
	if paperPublicationError(pc, map[string]bool{"q1-1": false}) == nil {
		t.Fatal("technical failure without any audited fact became a successful empty answer")
	}
	if err := paperPublicationError(&PaperCheckpoint{}, nil); err != nil {
		t.Fatalf("normal material gap must remain publishable: %v", err)
	}
}

func TestPaperSupplementPackingPinsDraftEvidenceUnderModelBudget(t *testing.T) {
	pc := &PaperCheckpoint{Limits: generation.ModelLimits{ContextTokens: 18000, MaxOutputTokens: 1024}, Mode: "fulltext"}
	questions := []PaperQuestion{{ID: "q1", Question: "方法？", Query: "method"}}
	initial := PaperAnswerInput{Evidence: []Citation{{ID: "pinned", Quote: "original source"}}}
	for i := range 8 {
		initial.Evidence = append(initial.Evidence, Citation{ID: fmt.Sprintf("old-%d", i), Quote: strings.Repeat("optional ", 500)})
	}
	candidate := []byte(`{"answers":[{"question_id":"q1","status":"partial","claims":[{"text":"已有结论。","evidence":[{"id":"pinned"}]}]}],"supplemental_queries":[]}`)
	fresh := Citation{ID: "new", Quote: "new relevant source"}
	input, added, err := packPaperSupplementInput(Run{Task: TaskPaperFollowup, Question: "方法？"}, pc, questions, initial, candidate, []Citation{fresh})
	must(t, err)
	if added != 1 || len(input.Evidence) >= len(initial.Evidence)+1 || input.Evidence[0] != initial.Evidence[0] || input.Evidence[1] != fresh {
		t.Fatalf("budget reduction lost pinned or fresh sources: %+v", input)
	}
	must(t, paperFitsSerialized(pc, paperSupplementStage, paperSupplementAnswerPrompt, []byte(input.Request)))
}

func TestPaperSupplementReservesActualReviewsWithSixtySecondCalls(t *testing.T) {
	now := time.Now()
	deadline := now.Add(180 * time.Second)
	question := PaperQuestion{ID: "q1", Question: "方法？", Query: "method"}
	evidence := Citation{ID: "source", Quote: "source"}
	candidate := []byte(`{"answers":[{"question_id":"q1","status":"partial","claims":[{"text":"论文方法。","evidence":[{"id":"source"}]}]}],"supplemental_queries":[]}`)
	pc := &PaperCheckpoint{CallTimeout: 60 * time.Second, Outputs: map[string]json.RawMessage{"analyzing_answer": candidate}, QA: &PaperQACheckpoint{Questions: []PaperQuestion{question}, Initial: &PaperAnswerInput{Evidence: []Citation{evidence}}}}
	checkpoint := &Checkpoint{Paper: pc, Calls: 2}
	run := Run{Task: TaskPaperFollowup, Deadline: &deadline}
	if reason := paperSupplementBudget(context.Background(), run, checkpoint, now); reason != "" {
		t.Fatalf("fast initial analysis disabled optional supplement for normal provider timeout: %s", reason)
	}
	deadline = now.Add(124 * time.Second)
	if reason := paperSupplementBudget(context.Background(), run, checkpoint, now); reason != "time_budget" {
		t.Fatalf("supplement did not reserve actual review and finalization time: %s", reason)
	}
}
