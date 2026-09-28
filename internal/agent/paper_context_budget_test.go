package agent

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func budgetJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func budgetReport(text string) *PaperReport {
	return &PaperReport{Problem: text, Method: text, Experiments: text, Results: text, Limitations: text}
}

func budgetReportFields(report *PaperReport) []string {
	return []string{report.Problem, report.Method, report.Experiments, report.Results, report.Limitations}
}

func budgetProjectedContext(t *testing.T, input map[string]any) PaperConversationContext {
	t.Helper()
	value, ok := input["conversation_context"]
	if !ok {
		t.Fatal("expected retained conversation context")
	}
	var snapshot PaperConversationContext
	if err := json.Unmarshal(budgetJSON(t, value), &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPaperContextBudgetTruncatesFieldsAtUTF8Boundaries(t *testing.T) {
	const marker = "\n[已截断]"
	snapshot := PaperConversationContext{
		Turns:  []PaperConversationTurn{{Question: strings.Repeat("研究🙂", 1000), Answer: strings.Repeat("验证🙂", 1600)}},
		Report: budgetReport(strings.Repeat("论文🙂", 200)),
	}
	original := budgetJSON(t, snapshot)
	bounded := boundedPaperConversationContext(snapshot)
	if !bounded.Truncated {
		t.Fatal("field truncation must be explicitly marked")
	}
	if !bytes.Equal(original, budgetJSON(t, snapshot)) {
		t.Fatal("bounding changed the source snapshot")
	}
	if len(bounded.Turns) != 1 || bounded.Report == nil {
		t.Fatal("ordinary field truncation discarded a complete turn or report")
	}
	values := []struct {
		text  string
		limit int
	}{
		{bounded.Turns[0].Question, 2 << 10},
		{bounded.Turns[0].Answer, 4 << 10},
	}
	for _, field := range budgetReportFields(bounded.Report) {
		values = append(values, struct {
			text  string
			limit int
		}{field, 400})
	}
	for _, value := range values {
		if !utf8.ValidString(value.text) || len(value.text) > value.limit || !strings.HasSuffix(value.text, marker) {
			t.Fatalf("invalid bounded UTF-8 field: bytes=%d limit=%d suffix=%t", len(value.text), value.limit, strings.HasSuffix(value.text, marker))
		}
	}
	if len(budgetJSON(t, bounded)) > 24<<10 {
		t.Fatal("bounded snapshot exceeded 24 KiB")
	}
}

func TestPaperContextBudgetIncludesEscapingAndRetainsNewestCompleteTurn(t *testing.T) {
	turns := []PaperConversationTurn{}
	for _, prefix := range []string{"oldest", "middle", "newest"} {
		turns = append(turns, PaperConversationTurn{
			Question: prefix + strings.Repeat("\\", (2<<10)-len(prefix)),
			Answer:   prefix + strings.Repeat("\\", (4<<10)-len(prefix)),
		})
	}
	snapshot := PaperConversationContext{Turns: turns, Report: budgetReport("当前论文的简短报告")}
	original := budgetJSON(t, snapshot)
	if len(original) <= 24<<10 {
		t.Fatal("fixture must exceed the serialized snapshot budget")
	}
	bounded := boundedPaperConversationContext(snapshot)
	if !bounded.Truncated {
		t.Fatal("serialized-budget truncation must be explicitly marked")
	}
	if len(budgetJSON(t, bounded)) > 24<<10 {
		t.Fatal("JSON escaping bypassed the snapshot limit")
	}
	if len(bounded.Turns) != 1 || bounded.Turns[0] != turns[2] {
		t.Fatalf("expected only the newest intact question/answer pair, got %d turns", len(bounded.Turns))
	}
	if !reflect.DeepEqual(bounded.Report, snapshot.Report) {
		t.Fatal("report was changed before removing excess old turns")
	}
	if !bytes.Equal(original, budgetJSON(t, snapshot)) {
		t.Fatal("snapshot limiting mutated its input")
	}
}

func TestPaperContextBudgetProjectionDropsOldTurnsBeforeReport(t *testing.T) {
	snapshot := PaperConversationContext{
		Turns: []PaperConversationTurn{
			{Question: strings.Repeat("o", 1024), Answer: strings.Repeat("a", 2048)},
			{Question: strings.Repeat("n", 1024), Answer: strings.Repeat("b", 2048)},
		},
		Report: budgetReport(strings.Repeat("r", 400)),
	}
	// HTML escaping expands this mandatory source text sixfold under json.Marshal.
	base := map[string]any{
		"question":          "当前问题",
		"original_question": "当前原始问题",
		"evidence":          []evidencePassage{{ID: "current-source", Quote: strings.Repeat("<", 9900)}},
	}
	originalBase, originalSnapshot := budgetJSON(t, base), budgetJSON(t, snapshot)
	projected, err := paperInputWithContext(base, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(budgetJSON(t, projected)) > 64<<10 {
		t.Fatal("full serialized model input exceeded 64 KiB")
	}
	context := budgetProjectedContext(t, projected)
	if !context.Truncated {
		t.Fatal("stage projection must mark omitted context")
	}
	if len(context.Turns) != 1 || context.Turns[0] != snapshot.Turns[1] {
		t.Fatalf("expected oldest complete turn to be removed, got %d turns", len(context.Turns))
	}
	if !reflect.DeepEqual(context.Report, snapshot.Report) {
		t.Fatal("summary was reduced before old turns")
	}
	for key, value := range base {
		if !reflect.DeepEqual(projected[key], value) {
			t.Fatalf("projection changed mandatory field %q", key)
		}
	}
	if !bytes.Equal(originalBase, budgetJSON(t, base)) || !bytes.Equal(originalSnapshot, budgetJSON(t, snapshot)) {
		t.Fatal("projection mutated the request or checkpoint snapshot")
	}
	// A later stage derives a new projection from the same frozen source.
	second, err := paperInputWithContext(map[string]any{"question": "小输入"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := budgetProjectedContext(t, second); !reflect.DeepEqual(got, snapshot) {
		t.Fatal("earlier projection silently removed context from a later stage")
	}
}

func budgetMandatoryInput(t *testing.T, size int) map[string]any {
	t.Helper()
	input := map[string]any{
		"question":          "must preserve the current question",
		"original_question": "must preserve the original question",
		"evidence":          []evidencePassage{{ID: "immutable", Quote: ""}},
	}
	room := size - len(budgetJSON(t, input))
	if room < 0 {
		t.Fatal("requested fixture is too small")
	}
	input["evidence"] = []evidencePassage{{ID: "immutable", Quote: strings.Repeat("x", room)}}
	if len(budgetJSON(t, input)) != size {
		t.Fatal("fixture has an unexpected serialized size")
	}
	return input
}

func TestPaperContextBudgetProjectionReducesReportOnlyAfterAllTurns(t *testing.T) {
	snapshot := PaperConversationContext{
		Turns: []PaperConversationTurn{
			{Question: strings.Repeat("old", 200), Answer: strings.Repeat("answer", 300)},
			{Question: strings.Repeat("new", 200), Answer: strings.Repeat("answer", 300)},
		},
		Report: budgetReport(strings.Repeat("中文", 66)),
	}
	original := budgetJSON(t, snapshot)
	base := budgetMandatoryInput(t, (64<<10)-1024)
	projected, err := paperInputWithContext(base, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	context := budgetProjectedContext(t, projected)
	if !context.Truncated {
		t.Fatal("shortened report must be explicitly marked")
	}
	if len(context.Turns) != 0 || context.Report == nil {
		t.Fatal("expected all turns removed and a shortened report retained")
	}
	if reflect.DeepEqual(context.Report, snapshot.Report) {
		t.Fatal("oversized report was not shortened")
	}
	for _, field := range budgetReportFields(context.Report) {
		if !utf8.ValidString(field) || len(field) > 400 {
			t.Fatal("report projection broke a UTF-8 or field-size boundary")
		}
	}
	if len(budgetJSON(t, projected)) > 64<<10 || !bytes.Equal(original, budgetJSON(t, snapshot)) {
		t.Fatal("report projection exceeded budget or mutated the frozen snapshot")
	}
}

func TestPaperContextBudgetExactBoundaryOmitsOptionalContextAndRejectsOversizedEvidence(t *testing.T) {
	snapshot := PaperConversationContext{
		Turns:  []PaperConversationTurn{{Question: "历史问题", Answer: "历史回答"}},
		Report: budgetReport("历史报告"),
	}
	base := budgetMandatoryInput(t, 64<<10)
	projected, err := paperInputWithContext(base, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := projected["conversation_context"]; exists || !reflect.DeepEqual(projected, base) {
		t.Fatal("exactly-full mandatory input must remain intact without optional context")
	}
	oversized := budgetMandatoryInput(t, (64<<10)+1)
	original := budgetJSON(t, oversized)
	if _, err = paperInputWithContext(oversized, snapshot); err == nil || paperFailureCode(err) != "context_too_large" {
		t.Fatalf("oversized mandatory evidence must fail explicitly: %v", err)
	}
	if !bytes.Equal(original, budgetJSON(t, oversized)) {
		t.Fatal("oversized mandatory input was truncated")
	}
}

func TestPaperContextBudgetKeepsRecentThreePairsAndPreservesAnExistingMarker(t *testing.T) {
	turns := []PaperConversationTurn{
		{Question: "一", Answer: "第一轮"},
		{Question: "二", Answer: "第二轮"},
		{Question: "三", Answer: "第三轮"},
		{Question: "四", Answer: "第四轮"},
	}
	snapshot := PaperConversationContext{Turns: turns}
	bounded := boundedPaperConversationContext(snapshot)
	if !reflect.DeepEqual(bounded.Turns, turns[1:]) || !bounded.Truncated {
		t.Fatal("expected the latest three complete pairs and a truncation marker")
	}
	if snapshot.Truncated || !reflect.DeepEqual(snapshot.Turns, turns) {
		t.Fatal("pair limiting changed the source snapshot")
	}
	projected, err := paperInputWithContext(map[string]any{"question": "继续说明"}, bounded)
	if err != nil {
		t.Fatal(err)
	}
	if got := budgetProjectedContext(t, projected); !reflect.DeepEqual(got, bounded) {
		t.Fatal("a roomy projection lost the snapshot's truncation marker")
	}
	complete := PaperConversationContext{Turns: turns[1:]}
	if got := boundedPaperConversationContext(complete); got.Truncated || !reflect.DeepEqual(got, complete) {
		t.Fatal("a complete context fitting all limits was altered or marked truncated")
	}
}
