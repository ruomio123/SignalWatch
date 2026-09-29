package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func reproductionTestPlan() paperReproductionPlanOutput {
	wire := paperReproductionPlanOutput{Categories: []paperReproductionPlanItemOutput{}}
	for _, category := range paperReproductionCategories() {
		wire.Categories = append(wire.Categories, paperReproductionPlanItemOutput{CategoryID: category.ID, Query: category.ID + " settings"})
	}
	return wire
}

func reproductionTestQuestions(t *testing.T) []PaperQuestion {
	t.Helper()
	questions, err := decodePaperReproductionPlan(budgetJSON(t, reproductionTestPlan()))
	must(t, err)
	return questions
}

func TestPaperReproductionPlanUsesSixFixedCategoriesAndServerQuestions(t *testing.T) {
	wire := reproductionTestPlan()
	wire.Categories[0], wire.Categories[5] = wire.Categories[5], wire.Categories[0]
	questions, err := decodePaperReproductionPlan(budgetJSON(t, wire))
	must(t, err)
	categories := paperReproductionCategories()
	if len(questions) != 6 {
		t.Fatalf("category count=%d", len(questions))
	}
	for i, question := range questions {
		if question.ID != fmt.Sprintf("q%d", i+1) || question.Question != categories[i].Question || question.Query != categories[i].ID+" settings" {
			t.Fatalf("planner changed category identity or order: %+v", questions)
		}
	}
	categories[0].Title = "mutated"
	if paperReproductionCategories()[0].Title != "数据与预处理" {
		t.Fatal("workflow categories were mutable")
	}
	count := paperReproductionPlanSchema.Properties["categories"]
	if count.MinItems == nil || *count.MinItems != 6 || count.MaxItems == nil || *count.MaxItems != 6 {
		t.Fatal("planner schema lost the fixed category count")
	}
}

func TestPaperReproductionPlanRejectsMissingDuplicatedAndInjectedCategories(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*paperReproductionPlanOutput)
	}{
		{"missing", func(w *paperReproductionPlanOutput) { w.Categories = w.Categories[:5] }},
		{"extra", func(w *paperReproductionPlanOutput) { w.Categories = append(w.Categories, w.Categories[0]) }},
		{"duplicate", func(w *paperReproductionPlanOutput) { w.Categories[5].CategoryID = "data" }},
		{"unknown", func(w *paperReproductionPlanOutput) { w.Categories[5].CategoryID = "custom" }},
		{"empty query", func(w *paperReproductionPlanOutput) { w.Categories[5].Query = " \n" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := reproductionTestPlan()
			tc.mutate(&wire)
			if _, err := decodePaperReproductionPlan(budgetJSON(t, wire)); err == nil || repairablePaperLimit(err) {
				t.Fatalf("invalid plan was accepted or repairable: %v", err)
			}
		})
	}
	base := string(budgetJSON(t, reproductionTestPlan()))
	for _, raw := range []string{
		strings.Replace(base, `"category_id":"data"`, `"category_id":"data","category_id":"model"`, 1),
		strings.Replace(base, `"query":"data settings"`, `"query":"data settings","title":"untrusted title"`, 1),
		base + `{}`,
	} {
		if _, err := decodePaperReproductionPlan([]byte(raw)); err == nil {
			t.Fatal("planner accepted duplicate keys or fields outside the wire contract")
		}
	}
	for _, size := range []int{1000, 1001} {
		wire := reproductionTestPlan()
		wire.Categories[0].Query = strings.Repeat("🙂", 250) + strings.Repeat("a", size-1000)
		_, err := decodePaperReproductionPlan(budgetJSON(t, wire))
		if size == 1000 {
			must(t, err)
		} else {
			assertPaperLimit(t, err, "$.categories[0].query", 1001, 1000, "utf8_bytes", false)
		}
	}
}

func TestPaperReproductionAnalysisHasIndependentTwelveItemBudget(t *testing.T) {
	questions := reproductionTestQuestions(t)
	evidence := []Citation{{ID: "source", Quote: "The paper explicitly states the reproduced settings."}}
	for _, extra := range []bool{false, true} {
		wire := answerTestWire(2, 2, 2, 2, 2, 2)
		if extra {
			wire.Answers[5].Claims = append(wire.Answers[5].Claims, wire.Answers[5].Claims[0])
		}
		analysis, err := decodePaperReproduction(budgetJSON(t, wire), evidence, questions, true)
		if extra {
			assertPaperLimit(t, err, "$.answers", 13, 12, "claims", true)
		} else {
			must(t, err)
			if len(analysis.Answers) != 6 || len(paperAnswerReviewClaims(analysis)) != 12 {
				t.Fatal("six categories or twelve review items were lost")
			}
		}
	}
	if _, err := decodePaperAnswer(budgetJSON(t, answerTestWire(2, 2, 1, 2)), evidence, answerTestQuestions(4)); !repairablePaperLimit(err) {
		t.Fatal("reproduction budget unexpectedly expanded ordinary QA")
	}
	for _, schemas := range []paperFieldSchemas{paperReproductionSchemas, paperReproductionSupplementSchemas} {
		answers := schemas.full.Properties["answers"]
		if answers.MaxItems == nil || *answers.MaxItems != 6 || answers.Items.Properties["claims"].MaxItems == nil || *answers.Items.Properties["claims"].MaxItems != 12 {
			t.Fatal("reproduction schema uses ordinary QA limits")
		}
	}
}

func TestPaperReproductionChecksEveryItemBeforeRepairEligibility(t *testing.T) {
	evidence := []Citation{{ID: "source", Quote: "Immutable factual source."}}
	questions := reproductionTestQuestions(t)
	for _, text := range []string{strings.Repeat("a", 1197) + "中", strings.Repeat("中", 400), strings.Repeat("🙂", 299) + "中a", strings.Repeat("<", 1197) + "中"} {
		wire := answerTestWire(1, 0, 0, 0, 0, 0)
		wire.Answers[0].Claims[0].Text = text
		_, err := decodePaperReproduction(budgetJSON(t, wire), evidence, questions, true)
		must(t, err)
		wire.Answers[0].Claims[0].Text += "a"
		_, err = decodePaperReproduction(budgetJSON(t, wire), evidence, questions, true)
		assertPaperLimit(t, err, "$.answers[0].claims[0].text", 1201, 1200, "utf8_bytes", true)
	}
	wire := answerTestWire(3, 2, 2, 2, 2, 2)
	wire.Answers[0].Claims[0].Text = strings.Repeat("中", 401)
	wire.Answers[5].Claims[1].Evidence = []evidenceOutput{{ID: "unknown"}}
	if _, err := decodePaperReproduction(budgetJSON(t, wire), evidence, questions, true); err == nil || repairablePaperLimit(err) {
		t.Fatalf("early item overflow hid invalid evidence in a later category: %v", err)
	}
	wire.Answers[5].Claims[1].Evidence = []evidenceOutput{{ID: "source"}}
	wire.Answers[5].Claims[1].Text = "English-only settings must not pass the reproduction language contract."
	if _, err := decodePaperReproduction(budgetJSON(t, wire), evidence, questions, true); err == nil || repairablePaperLimit(err) {
		t.Fatalf("early item overflow hid wrong report language in a later category: %v", err)
	}
}

func TestPaperReproductionOnlySupplementalGapsCanRequestRetrieval(t *testing.T) {
	questions := reproductionTestQuestions(t)
	evidence := []Citation{{ID: "source", Quote: "Immutable factual source."}}
	wire := answerTestWire(1, 0, 0, 0, 0, 0)
	wire.SupplementalQueries = []PaperSupplementQuery{{QuestionID: "q6", Query: "source code weights"}, {QuestionID: "q6", Query: "implementation repository"}}
	_, err := decodePaperReproduction(budgetJSON(t, wire), evidence, questions, true)
	must(t, err)
	if _, err := decodePaperReproduction(budgetJSON(t, wire), evidence, questions, false); err == nil || repairablePaperLimit(err) {
		t.Fatal("supplementary reproduction requested another retrieval")
	}
	wire.SupplementalQueries[0].QuestionID = "q1"
	if _, err := decodePaperReproduction(budgetJSON(t, wire), evidence, questions, true); err == nil || repairablePaperLimit(err) {
		t.Fatal("supported category requested retrieval")
	}
	questions[0].Question = "A model supplied category title"
	if _, err := decodePaperReproduction(budgetJSON(t, wire), evidence, questions, true); err == nil {
		t.Fatal("reproduction accepted a changed category scope in checkpoint")
	}
}

func TestPaperReproductionRenderingKeepsGlobalNumbersAndPerCategoryGaps(t *testing.T) {
	questions := reproductionTestQuestions(t)
	evidence := []Citation{structuredAgentCitation(structuredAgentTable(2, "accuracy"))}
	boundary := evidence[0]
	boundary.ID = "source"
	evidence = []Citation{boundary}
	wire := answerTestWire(2, 1, 0, 1, 0, 1)
	analysis, err := decodePaperReproduction(budgetJSON(t, wire), evidence, questions, true)
	must(t, err)
	verdicts := map[string]bool{"q1-1": true, "q1-2": false, "q2-1": true, "q4-1": true, "q6-1": true}
	cp := &Checkpoint{DocumentID: "pinned-document", Paper: &PaperCheckpoint{Context: PaperContext{Title: "Paper"}, Mode: "fulltext", SourceVersion: boundary.SourceVersion, ContentHash: boundary.ContentHash, StructuredGap: "incomplete"}}
	content, result, citations := renderPaperReproduction(Run{WorkflowVersion: PaperWorkflowVersion}, cp, questions, analysis, evidence, verdicts)
	if result.Answer != nil || result.Report != nil || result.Reproduction == nil || result.Reproduction.Status != "partial" || result.Fields["reproduction"].Status != "partial" || len(result.Fields) != 1 || result.StructuredGap != "incomplete" {
		t.Fatalf("wrong independent reproduction result: %+v", result)
	}
	checklist := result.Reproduction
	if len(checklist.Categories) != 6 || checklist.Categories[0].Status != "partial" || checklist.Categories[0].Gap == nil || checklist.Categories[0].Gap.Reason != "review_rejected" || checklist.Categories[2].Status != "insufficient_evidence" || checklist.Categories[2].Gap == nil || checklist.Categories[2].Gap.Reason != "insufficient_evidence" {
		t.Fatalf("lost category status or gap: %+v", checklist)
	}
	visible := 0
	ids := []string{}
	for _, category := range checklist.Categories {
		for _, item := range category.Items {
			visible++
			if item.Number != visible || len(item.CitationIDs) != 1 || !strings.Contains(content, fmt.Sprintf("%d. %s", visible, item.Text)) {
				t.Fatal("visible checklist numbering or content drifted")
			}
			ids = append(ids, item.CitationIDs...)
		}
	}
	if visible != 4 || len(citations) != 4 || !reflect.DeepEqual(ids, result.Fields["reproduction"].CitationIDs) || strings.Contains(content, "answer-q1-2-1") {
		t.Fatal("rejected items or unstable references leaked into publication")
	}
	for _, ref := range citations {
		ref.ID = boundary.ID
		if !reflect.DeepEqual(ref, boundary) {
			t.Fatal("reproduction lost structured evidence provenance")
		}
	}
	raw := string(budgetJSON(t, result))
	if strings.Contains(raw, "supplemental_queries") || strings.Contains(raw, "query") {
		t.Fatal("private search controls leaked into public reproduction")
	}
}

func TestPaperReproductionAllEvidenceMissingKeepsAllSixCategories(t *testing.T) {
	questions := reproductionTestQuestions(t)
	analysis, err := decodePaperReproduction(budgetJSON(t, answerTestWire(0, 0, 0, 0, 0, 0)), nil, questions, true)
	must(t, err)
	cp := &Checkpoint{Paper: &PaperCheckpoint{Mode: "abstract"}}
	content, result, citations := renderPaperReproduction(Run{}, cp, questions, analysis, nil, map[string]bool{})
	if result.Reproduction.Status != "insufficient" || len(result.Reproduction.Categories) != 6 || len(citations) != 0 || !strings.Contains(content, "仅基于摘要") {
		t.Fatal("missing evidence was converted into an invented checklist")
	}
	for _, category := range result.Reproduction.Categories {
		if category.Items == nil || len(category.Items) != 0 || category.Gap == nil || category.Status != "insufficient_evidence" {
			t.Fatal("missing category was omitted instead of retaining its gap")
		}
	}
	var roundtrip PaperResult
	must(t, json.Unmarshal(budgetJSON(t, result), &roundtrip))
	if !reflect.DeepEqual(result, roundtrip) {
		t.Fatal("completed reproduction result did not round trip")
	}
}

func TestReproductionContextSummaryPreservesAllTwelveVisibleItems(t *testing.T) {
	checklist := &PaperReproduction{Status: "partial", Categories: []PaperReproductionCategory{}}
	for i, spec := range paperReproductionCategories() {
		category := PaperReproductionCategory{ID: spec.ID, Title: spec.Title, Status: "partial", Items: []PaperReproductionItem{}, Gap: &PaperAnswerGap{Reason: "insufficient_evidence"}}
		if i == 5 {
			category.Gap.Reason = "review_rejected"
		}
		for j := 0; j < 2; j++ {
			category.Items = append(category.Items, PaperReproductionItem{Number: i*2 + j + 1, Text: strings.Repeat("论文设置🙂", 75), CitationIDs: []string{fmt.Sprintf("answer-q%d-%d-1", i+1, j+1)}})
		}
		checklist.Categories = append(checklist.Categories, category)
	}
	snapshot := string(budgetJSON(t, checklist))
	summary, clipped := reproductionContextSummary(checklist)
	if !clipped || !utf8.ValidString(summary) || len(summary) >= paperHistoryAnswerLimit || !strings.Contains(summary, "不作为当前证据") {
		t.Fatalf("history summary is not a bounded UTF-8 background: clipped=%t bytes=%d", clipped, len(summary))
	}
	for _, category := range checklist.Categories {
		if !strings.Contains(summary, "### "+category.Title) {
			t.Fatalf("history lost category %s", category.Title)
		}
		for _, item := range category.Items {
			clippedText := truncatePaperContext(item.Text, 200)
			if len(clippedText) > 200 || !strings.Contains(summary, fmt.Sprintf("%d. %s", item.Number, clippedText)) {
				t.Fatalf("history lost visible item %d or changed its number", item.Number)
			}
		}
	}
	if !strings.Contains(summary, "[当前材料存在证据缺口]") || !strings.Contains(summary, "[部分项目未通过证据审核]") || strings.Contains(summary, "answer-q") {
		t.Fatal("history gap is missing or historical evidence IDs were carried into the summary")
	}
	if string(budgetJSON(t, checklist)) != snapshot {
		t.Fatal("summarizing history modified the completed reproduction")
	}
	context := boundedPaperConversationContext(PaperConversationContext{Turns: []PaperConversationTurn{{Question: "生成复现清单", Answer: summary}}, Truncated: clipped})
	if len(context.Turns) != 1 || context.Turns[0].Answer != summary || !context.Truncated || !strings.Contains(context.Turns[0].Answer, "12. ") {
		t.Fatal("ordinary history budgeting truncated later reproduction items")
	}
}

func TestReproductionContextSummaryShortItemsRemainComplete(t *testing.T) {
	if summary, clipped := reproductionContextSummary(nil); summary != "" || clipped {
		t.Fatal("nil reproduction invented history")
	}
	checklist := &PaperReproduction{Status: "partial", Categories: []PaperReproductionCategory{{ID: "data", Title: "数据与预处理", Status: "partial", Items: []PaperReproductionItem{{Number: 7, Text: "训练数据使用固定划分。"}}, Gap: &PaperAnswerGap{Reason: "untrusted freeform text"}}}}
	summary, clipped := reproductionContextSummary(checklist)
	if clipped || !strings.Contains(summary, "7. 训练数据使用固定划分。") || strings.Contains(summary, "untrusted freeform text") || !strings.Contains(summary, "[当前材料存在证据缺口]") {
		t.Fatal("short summary renumbered items, invented truncation or copied an uncontrolled gap")
	}
}
