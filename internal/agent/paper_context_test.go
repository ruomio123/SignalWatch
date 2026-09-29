package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"signalwatch/internal/generation"
	"strings"
	"testing"
	"time"
)

type contextSeed struct {
	task, state, hash, question, answer, workflow, mode string
	omitUser, omitAssistant                             bool
	messageConversation                                 string
	report                                              *PaperReport
}

func seedPaperContextRun(t *testing.T, f *fixture, c Conversation, seed contextSeed) Run {
	t.Helper()
	if seed.task == "" {
		seed.task = TaskPaperFollowup
	}
	if seed.state == "" {
		seed.state = "completed"
	}
	if seed.workflow == "" {
		seed.workflow = PaperWorkflowVersion
	}
	if seed.mode == "" {
		seed.mode = "abstract"
	}
	if seed.messageConversation == "" {
		seed.messageConversation = c.ID
	}
	now := time.Now().UTC()
	r := Run{ID: rand.Text(), ConversationID: c.ID, UserID: f.u.ID, Task: seed.task, State: seed.state, WorkflowVersion: seed.workflow, Question: seed.question, Provider: "glm", Model: "glm-4.7-flash", Generation: "agent-fixture", Version: 1, IdempotencyKey: rand.Text(), InputHash: strings.Repeat("0", 64), ContextMode: seed.mode, Checkpoint: json.RawMessage(`{"phase":"ready"}`), CreatedAt: now, UpdatedAt: now}
	must(t, f.db.Create(&r).Error)
	result, err := json.Marshal(PaperResult{PaperHash: seed.hash, WorkflowVersion: seed.workflow, ContextMode: seed.mode, Report: seed.report})
	must(t, err)
	messages := []Message{}
	if !seed.omitUser {
		messages = append(messages, Message{ConversationID: seed.messageConversation, RunID: r.ID, Role: "user", Content: seed.question, Citations: json.RawMessage(`[]`), CreatedAt: now})
	}
	if !seed.omitAssistant {
		messages = append(messages, Message{ConversationID: seed.messageConversation, RunID: r.ID, Role: "assistant", Content: seed.answer, Result: result, Citations: json.RawMessage(`[]`), CreatedAt: now})
	}
	if len(messages) > 0 {
		must(t, f.db.Create(&messages).Error)
	}
	return r
}

func contextPaperHash(t *testing.T, f *fixture, c Conversation) string {
	t.Helper()
	p, err := f.s.Papers.Get(t.Context(), f.u.ID, *c.PaperID)
	must(t, err)
	return paperSnapshotHash(p)
}

func contextReport(label string) *PaperReport {
	return &PaperReport{Problem: label + "问题", Method: label + "方法", Experiments: label + "实验", Results: label + "结果", Limitations: label + "局限"}
}

func TestPaperConversationHistoryPairsOnlyEligibleRuns(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	hash := contextPaperHash(t, f, c)
	want := []PaperConversationTurn{{Question: "第一个问题", Answer: "第一个回答"}, {Question: "第二个问题", Answer: "第二个回答"}, {Question: "第三个问题", Answer: "第三个回答"}}
	for _, turn := range want {
		seedPaperContextRun(t, f, c, contextSeed{hash: hash, question: turn.Question, answer: turn.Answer, workflow: "paper-fixed-v6"})
	}
	for _, state := range []string{"failed", "cancelled", "unknown", "pending", "running"} {
		seedPaperContextRun(t, f, c, contextSeed{hash: hash, state: state, question: state + "问题", answer: state + "回答"})
	}
	for _, seed := range []contextSeed{
		{hash: hash, question: "只有问题", omitAssistant: true},
		{hash: hash, answer: "只有回答", omitUser: true},
		{hash: hash, question: "   ", answer: "问题为空"},
		{hash: hash, question: "回答为空", answer: "   "},
		{hash: "another-snapshot", question: "旧论文问题", answer: "旧论文回答"},
		{hash: hash, task: TaskPaperReport, question: "生成报告", answer: "报告不属于QA轮次"},
	} {
		seedPaperContextRun(t, f, c, seed)
	}
	other, err := f.s.CreateConversation(t.Context(), f.u.ID, "paper", c.PaperID)
	must(t, err)
	seedPaperContextRun(t, f, other, contextSeed{hash: hash, question: "另一个会话", answer: "不应进入当前会话"})
	// Even inconsistent message rows must not defeat the run's conversation
	// ownership check; both sides of a pair must belong to the same run/view.
	seedPaperContextRun(t, f, other, contextSeed{hash: hash, question: "错位问题", answer: "错位回答", messageConversation: c.ID})
	excluded := seedPaperContextRun(t, f, c, contextSeed{hash: hash, question: "当前轮问题", answer: "排除当前轮"})
	got, err := f.store.PaperHistory(t.Context(), c.ID, hash, excluded.ID)
	must(t, err)
	if !reflect.DeepEqual(got.Turns, want) || got.Report != nil {
		t.Fatalf("history included incomplete, unrelated, or uncommitted turns: %+v", got)
	}
}

func TestPaperConversationHistoryFiltersBeforeTheLatestThreeLimit(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	hash := contextPaperHash(t, f, c)
	for i := 1; i <= 5; i++ {
		seedPaperContextRun(t, f, c, contextSeed{hash: hash, question: fmt.Sprintf("question-%d", i), answer: fmt.Sprintf("answer-%d", i)})
	}
	// More than the old broad History limit of twenty messages, all newer
	// than the valid rows, must not crowd the eligible pairs out of the query.
	for i := 0; i < 30; i++ {
		seedPaperContextRun(t, f, c, contextSeed{hash: "stale", question: "stale question", answer: "stale answer"})
		seedPaperContextRun(t, f, c, contextSeed{hash: hash, question: "incomplete question", omitAssistant: true})
	}
	got, err := f.store.PaperHistory(t.Context(), c.ID, hash, "")
	must(t, err)
	want := []PaperConversationTurn{{Question: "question-3", Answer: "answer-3"}, {Question: "question-4", Answer: "answer-4"}, {Question: "question-5", Answer: "answer-5"}}
	if !reflect.DeepEqual(got.Turns, want) || !got.Truncated {
		t.Fatalf("filtering or latest-three order is wrong: %+v", got.Turns)
	}
}

func TestPaperConversationReportSkipsStaleAndInvalidNewerReports(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	hash := contextPaperHash(t, f, c)
	seedPaperContextRun(t, f, c, contextSeed{task: TaskPaperReport, hash: hash, question: "旧报告", answer: "旧报告", report: contextReport("older"), workflow: "paper-fixed-v6"})
	want := contextReport("latest-valid")
	seedPaperContextRun(t, f, c, contextSeed{task: TaskPaperReport, hash: hash, question: "有效报告", answer: "有效报告", report: want, workflow: "paper-fixed-v6"})
	// Force the report query across more than two internal pages. The JSON
	// column is valid JSON, but these payloads are not compatible reports.
	for i := 0; i < 41; i++ {
		seedPaperContextRun(t, f, c, contextSeed{task: TaskPaperReport, hash: hash, question: "无效报告", answer: "無效报告", report: contextReport("invalid"), workflow: "unsupported-version"})
	}
	seedPaperContextRun(t, f, c, contextSeed{task: TaskPaperReport, hash: "stale", question: "过期报告", answer: "过期报告", report: contextReport("stale")})
	seedPaperContextRun(t, f, c, contextSeed{task: TaskPaperReport, state: "failed", hash: hash, question: "失败报告", answer: "失败报告", report: contextReport("failed")})
	excluded := seedPaperContextRun(t, f, c, contextSeed{task: TaskPaperReport, hash: hash, question: "排除本轮", answer: "排除本轮", report: contextReport("excluded")})
	got, err := f.store.PaperHistory(t.Context(), c.ID, hash, excluded.ID)
	must(t, err)
	if len(got.Turns) != 0 || !reflect.DeepEqual(got.Report, want) {
		t.Fatalf("newer stale/invalid reports hid the valid v6 summary: %+v", got)
	}
}

var errContextCheckpointPause = errors.New("fixture process stopped after durable context checkpoint")

type paperContextStore struct {
	Store
	reads, legacyReads int
	pauseAtCalls       int
	pause              bool
}

func (s *paperContextStore) PaperHistory(ctx context.Context, conversation, hash, exclude string) (PaperConversationContext, error) {
	s.reads++
	return s.Store.PaperHistory(ctx, conversation, hash, exclude)
}

func (s *paperContextStore) History(ctx context.Context, conversation string) ([]Message, error) {
	s.legacyReads++
	return s.Store.History(ctx, conversation)
}

func (s *paperContextStore) Save(ctx context.Context, r Run, cp Checkpoint, progress string, step *Step) error {
	if err := s.Store.Save(ctx, r, cp, progress, step); err != nil {
		return err
	}
	if s.pause && cp.Paper != nil && cp.Paper.ContextCaptured && cp.Phase == "ready" && cp.Calls == s.pauseAtCalls {
		s.pause = false
		return errContextCheckpointPause
	}
	return nil
}

type contextInspectGateway struct {
	*workflowGateway
	inspect func(ModelRequest)
}

func (g *contextInspectGateway) Generate(ctx context.Context, req ModelRequest) (generation.Result, error) {
	// Observe before the gateway invokes req.Before: the context must already
	// be durable before the model-call checkpoint or any external request.
	if g.inspect != nil {
		g.inspect(req)
	}
	return g.workflowGateway.Generate(ctx, req)
}

func TestPaperConversationSnapshotSurvivesRecoveryAndHistoryChanges(t *testing.T) {
	for _, scenario := range []string{"captured", "normalized", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, nil)
			hash := contextPaperHash(t, f, c)
			var previous Run
			if scenario != "empty" {
				previous = seedPaperContextRun(t, f, c, contextSeed{hash: hash, question: "保留的问题", answer: "保留的回答"})
				seedPaperContextRun(t, f, c, contextSeed{task: TaskPaperReport, hash: hash, question: "原报告", answer: "原报告", report: contextReport("原始")})
			}
			watch := &paperContextStore{Store: f.store, pause: true}
			if scenario == "normalized" {
				watch.pauseAtCalls = 1
			}
			f.s.Store = watch
			g := &contextInspectGateway{workflowGateway: &workflowGateway{}}
			f.s.Gateway = g
			r := f.claim(t, directSubmit(t, f, c, "", "abstract").ID)
			contexts := []PaperConversationContext{}
			g.inspect = func(req ModelRequest) {
				var input map[string]json.RawMessage
				must(t, json.Unmarshal(req.Input, &input))
				raw, exists := input["conversation_context"]
				if strings.Contains(req.System, "Resolve pronouns") || strings.Contains(string(req.Input), `"field":"answer"`) {
					if !exists {
						t.Fatal("normalize/answer request omitted its captured context")
					}
					var value PaperConversationContext
					must(t, json.Unmarshal(raw, &value))
					stored := directCheckpoint(t, f, r)
					if stored.Paper == nil || !stored.Paper.ContextCaptured || !reflect.DeepEqual(value, stored.Paper.ConversationContext) {
						t.Fatalf("model request began before its context snapshot was saved: request=%+v checkpoint=%+v", value, stored)
					}
					contexts = append(contexts, value)
				}
			}
			var cp Checkpoint
			must(t, json.Unmarshal(r.Checkpoint, &cp))
			err := f.s.processPaper(t.Context(), r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) })
			if !errors.Is(err, errContextCheckpointPause) {
				t.Fatalf("did not stop at the intended persisted checkpoint: %v", err)
			}
			captured := directCheckpoint(t, f, r)
			if captured.Paper == nil || !captured.Paper.ContextCaptured || watch.reads != 1 || watch.legacyReads != 0 || len(g.calls) != watch.pauseAtCalls {
				t.Fatalf("capture barrier failed: reads=%d broad=%d calls=%d checkpoint=%+v", watch.reads, watch.legacyReads, len(g.calls), captured)
			}
			if scenario == "empty" && (len(captured.Paper.ConversationContext.Turns) != 0 || captured.Paper.ConversationContext.Report != nil) {
				t.Fatal("empty context was not captured as an empty snapshot")
			}
			if previous.ID != "" {
				must(t, f.db.Model(&Message{}).Where("run_id=? AND role='assistant'", previous.ID).Update("content", "历史已被改变").Error)
			}
			seedPaperContextRun(t, f, c, contextSeed{hash: hash, question: "后来新增问题", answer: "后来新增回答"})
			seedPaperContextRun(t, f, c, contextSeed{task: TaskPaperReport, hash: hash, question: "后来报告", answer: "后来报告", report: contextReport("后来")})
			must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, _ := paperOutcome(t, f, c, r)
			if end.State != "completed" || len(g.calls) != 3 || len(contexts) != 2 || watch.reads != 1 || watch.legacyReads != 0 {
				t.Fatalf("recovery reread history or replayed normalization: state=%s calls=%d inputs=%d reads=%d broad=%d", end.State, len(g.calls), len(contexts), watch.reads, watch.legacyReads)
			}
			for _, value := range contexts {
				if !reflect.DeepEqual(value, captured.Paper.ConversationContext) {
					t.Fatalf("normalize and answer used different context snapshots: captured=%+v got=%+v", captured.Paper.ConversationContext, value)
				}
			}
			if !reflect.DeepEqual(directCheckpoint(t, f, r).Paper.ConversationContext, captured.Paper.ConversationContext) {
				t.Fatal("recovery rewrote the persisted conversation context")
			}
		})
	}
}

func TestPaperConversationHistoricalCitationsCannotBecomeSourceEvidence(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	hash := contextPaperHash(t, f, c)
	seedPaperContextRun(t, f, c, contextSeed{hash: hash, question: "历史问题", answer: "历史断言 [history-only-reference]"})
	seenContext := false
	g := &workflowGateway{}
	g.hook = func(req ModelRequest) error {
		var input struct {
			Field    string                   `json:"field"`
			Context  PaperConversationContext `json:"conversation_context"`
			Evidence []Citation               `json:"evidence"`
		}
		must(t, json.Unmarshal(req.Input, &input))
		if input.Field != "answer" {
			return nil
		}
		seenContext = len(input.Context.Turns) == 1 && strings.Contains(input.Context.Turns[0].Answer, "history-only-reference")
		for _, evidence := range input.Evidence {
			if evidence.ID == "history-only-reference" || strings.Contains(evidence.Quote, "历史断言") {
				t.Fatal("a historical assistant answer was promoted into source evidence")
			}
		}
		raw, err := json.Marshal(singleQuestionAnswer(fieldOutput{Status: "supported", Claims: []claimOutput{{Text: "这个结论来自历史对话。", Evidence: []evidenceOutput{{ID: "history-only-reference"}}}}}))
		must(t, err)
		return req.Validate(generation.Result{Content: raw})
	}
	f.s.Gateway = g
	r := directSubmit(t, f, c, "", "abstract")
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, _ := paperOutcome(t, f, c, r)
	var published int64
	must(t, f.db.Model(&Message{}).Where("run_id=? AND role='assistant'", r.ID).Count(&published).Error)
	if !seenContext || end.State == "completed" || end.FailureCode != "evidence_id_unknown" || published != 0 || len(g.calls) != 2 {
		t.Fatalf("historical citation escaped validation: context=%t state=%s failure=%s messages=%d calls=%d", seenContext, end.State, end.FailureCode, published, len(g.calls))
	}
}

func TestPaperConversationOutputWithoutCapturedContextIsNotReinterpreted(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	g := &workflowGateway{}
	f.s.Gateway = g
	r := f.claim(t, directSubmit(t, f, c, "", "abstract").ID)
	var cp Checkpoint
	must(t, json.Unmarshal(r.Checkpoint, &cp))
	_, _, err := f.s.preparePaper(t.Context(), r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) })
	must(t, err)
	cp.Paper.ContextCaptured = false
	cp.Paper.Outputs["normalizing_question"] = json.RawMessage(`{"question":"已有规范化问题","query":"retrieval"}`)
	cp.Calls = 1
	must(t, f.store.Save(t.Context(), r, cp, "normalizing_question", nil))
	must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
	watch := &paperContextStore{Store: f.store}
	f.s.Store = watch
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, _ := paperOutcome(t, f, c, r)
	if end.FailureCode != "invalid_checkpoint" || len(g.calls) != 0 || watch.reads != 0 {
		t.Fatalf("partial old output acquired a new history snapshot: state=%s failure=%s calls=%d reads=%d", end.State, end.FailureCode, len(g.calls), watch.reads)
	}
}

func TestPaperConversationV6RetryKeepsItsOriginalIdentity(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	g := &workflowGateway{}
	f.s.Gateway = g
	old := seedPaperContextRun(t, f, c, contextSeed{hash: contextPaperHash(t, f, c), question: "旧版问题", answer: "旧版回答", workflow: "paper-fixed-v6"})
	// Frozen v6 wire representation, independent of submissionHash in v7.
	legacy := `{"task":"paper_followup","question":"旧版问题","provider":"glm","model":"glm-4.7-flash","idempotency_key":"legacy-v6-context-key","context_mode":"abstract","workflow_version":"paper-fixed-v6"}`
	digest := sha256.Sum256([]byte(legacy))
	wantHash := hex.EncodeToString(digest[:])
	must(t, f.db.Model(&Run{}).Where("id=?", old.ID).Updates(map[string]any{"input_hash": wantHash, "idempotency_key": "legacy-v6-context-key"}).Error)
	input := SubmitInput{Question: "旧版问题", Provider: "glm", Model: "glm-4.7-flash", ContextMode: "abstract", IdempotencyKey: "legacy-v6-context-key"}
	again, err := f.s.Submit(t.Context(), f.u.ID, c.ID, input)
	must(t, err)
	if again.ID != old.ID || again.WorkflowVersion != "paper-fixed-v6" || again.InputHash != wantHash {
		t.Fatalf("v6 replay was rebuilt using v7 semantics: %+v", again)
	}
	input.Question = "不同的问题"
	if _, err := f.s.Submit(t.Context(), f.u.ID, c.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("v6 key accepted a different question: %v", err)
	}
	stored, err := f.store.RunByID(t.Context(), f.u.ID, old.ID)
	must(t, err)
	if stored.InputHash != wantHash || len(g.calls) != 0 {
		t.Fatal("old retry changed the stored hash or called a model")
	}
}

func TestPaperConversationV6RecoveryPreservesUnknownCallPrecedence(t *testing.T) {
	for _, scenario := range []string{"ready", "calling", "calling-expired"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, nil)
			g := &workflowGateway{}
			f.s.Gateway = g
			r := directSubmit(t, f, c, "", "abstract")
			phase, want := "ready", "workflow_changed"
			if strings.HasPrefix(scenario, "calling") {
				phase, want = "calling", "result_unknown"
			}
			cp, err := json.Marshal(Checkpoint{Phase: phase})
			must(t, err)
			updates := map[string]any{"workflow_version": "paper-fixed-v6", "checkpoint": cp}
			if scenario == "calling-expired" {
				updates["deadline"] = time.Now().Add(-time.Minute)
			}
			must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Updates(updates).Error)
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			if end.FailureCode != want || len(g.calls) != 0 || len(messages) != 1 || (phase == "calling" && end.State != "unknown") {
				t.Fatalf("v6 checkpoint was reinterpreted: state=%s failure=%s calls=%d", end.State, end.FailureCode, len(g.calls))
			}
		})
	}
}

func TestPaperPartialReportHistoryRetainsCompletionMarker(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	hash := contextPaperHash(t, f, c)
	report := contextReport("审核内容")
	r := seedPaperContextRun(t, f, c, contextSeed{task: TaskPaperReport, hash: hash, question: PaperGoal, answer: "本轮部分完成", report: report})
	raw, err := json.Marshal(PaperResult{Outcome: "partial", Report: report, PaperHash: hash, WorkflowVersion: PaperWorkflowVersion, ContextMode: "abstract", Issues: []PaperIssue{{Stage: "extracting_batch_1", Code: "output_invalid_json"}}})
	must(t, err)
	must(t, f.db.Model(&Message{}).Where("run_id=? AND role='assistant'", r.ID).Update("result", raw).Error)
	history, err := f.store.PaperHistory(t.Context(), c.ID, hash, "")
	must(t, err)
	if history.ReportOutcome != "partial" || history.Report == nil {
		t.Fatal("partial report history marker lost")
	}
	projected := boundedPaperConversationContext(history)
	if projected.ReportOutcome != "partial" || projected.Report == nil {
		t.Fatal("context projection lost partial marker")
	}
	request, err := paperInputWithContext(map[string]any{"question": "继续说明"}, projected)
	must(t, err)
	body, err := json.Marshal(request)
	must(t, err)
	if !strings.Contains(string(body), `"report_outcome":"partial"`) {
		t.Fatal("model history treats unfinished report as complete")
	}
}
