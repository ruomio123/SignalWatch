package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"signalwatch/internal/document"
	"signalwatch/internal/generation"
)

var reproductionWorkflowKeywords = []string{"datasetalpha", "modelbeta", "traingamma", "evaldelta", "computeepsilon", "resourceszeta"}

func reproductionRunDiagnostic(r Run, calls int) string {
	return fmt.Sprintf("state=%s code=%s stage=%s calls=%d", r.State, r.FailureCode, r.FailureStage, calls)
}

type reproductionWorkflowGateway struct {
	workflowGateway
	t                   *testing.T
	f                   *fixture
	stages              []string
	supplement, dense   bool
	longItems, empty    bool
	partial, reject     string
	over                map[string]bool
	failStage, failCode string
	beforeStart         func(ModelRequest) error
}

func (g *reproductionWorkflowGateway) Generate(ctx context.Context, req ModelRequest) (generation.Result, error) {
	if g.beforeStart != nil {
		if err := g.beforeStart(req); err != nil {
			return generation.Result{}, err
		}
	}
	if err := req.Before(ctx); err != nil {
		return generation.Result{}, err
	}
	cp := directCheckpoint(g.t, g.f, req.Run)
	stage := cp.Paper.CurrentStage
	g.calls, g.stages = append(g.calls, req), append(g.stages, stage)
	if cp.Phase != "calling" || cp.Calls != len(g.calls) {
		g.t.Fatalf("reproduction call was not reserved: stage=%s calls=%d actual=%d phase=%s", stage, cp.Calls, len(g.calls), cp.Phase)
	}
	if stage == paperReproductionSupplementStage && (cp.Paper.QA.Supplement.State != "calling" || cp.Paper.QA.Supplement.Input.Request != string(req.Input)) {
		g.t.Fatal("supplement started without its frozen request and calling state")
	}
	var input struct {
		Evidence  []Citation      `json:"evidence"`
		Claims    []reviewClaim   `json:"claims"`
		Candidate json.RawMessage `json:"candidate"`
	}
	must(g.t, json.Unmarshal(req.Input, &input))
	var value any
	switch {
	case stage == "planning_reproduction":
		plan := reproductionTestPlan()
		for i := range plan.Categories {
			plan.Categories[i].Query = reproductionWorkflowKeywords[i]
		}
		value = plan
	case strings.HasPrefix(stage, "validating_paper"):
		verdicts := []map[string]any{}
		for _, claim := range input.Claims {
			verdicts = append(verdicts, map[string]any{"id": claim.ID, "supported": g.reject != "all" && !strings.HasPrefix(claim.ID, g.reject+"-")})
		}
		value = map[string]any{"verdicts": verdicts}
	case strings.HasPrefix(stage, "repairing_"):
		var candidate paperAnswerOutput
		must(g.t, json.Unmarshal(input.Candidate, &candidate))
		for i := range candidate.Answers {
			if len(candidate.Answers[i].Claims) > 0 {
				candidate.Answers[i].Claims = candidate.Answers[i].Claims[:1]
				candidate.Answers[i].Claims[0].Text = "整理后保留论文给出的复现设置。"
			}
		}
		value = candidate
	default:
		wire := paperAnswerOutput{Answers: []paperAnswerPartOutput{}, SupplementalQueries: []PaperSupplementQuery{}}
		tables := []Citation{}
		for _, source := range input.Evidence {
			if strings.HasPrefix(source.ID, "h-table-") {
				tables = append(tables, source)
			}
		}
		for i, keyword := range reproductionWorkflowKeywords {
			part := paperAnswerPartOutput{QuestionID: fmt.Sprintf("q%d", i+1), Status: "supported", Claims: []claimOutput{}}
			if g.empty || len(input.Evidence) == 0 {
				part.Status = "insufficient_evidence"
			} else {
				ref := input.Evidence[0].ID
				for _, source := range input.Evidence {
					if strings.Contains(source.Quote, keyword) {
						ref = source.ID
						break
					}
				}
				count := 1
				if g.longItems || (g.dense && stage == paperReproductionSupplementStage) {
					count = 2
				}
				for j := 0; j < count; j++ {
					claim := claimOutput{Text: fmt.Sprintf("论文给出了第%d类复现设置。", i+1), Evidence: []evidenceOutput{{ID: ref}}}
					if g.longItems {
						claim.Text = strings.Repeat("中", 400)
					}
					if g.dense && stage == paperReproductionSupplementStage {
						if len(tables) < 9 {
							g.t.Fatalf("dense fixture lost complete structural sources: %d", len(tables))
						}
						group := []int{0, 0, 1, 2, 2, 0, 1, 1, 2, 0, 0, 1}[i*2+j]
						claim.Text, claim.Evidence = strings.Repeat("<", 500)+"中", []evidenceOutput{}
						for k := 0; k < 3; k++ {
							claim.Evidence = append(claim.Evidence, evidenceOutput{ID: tables[group*3+k].ID})
						}
					}
					part.Claims = append(part.Claims, claim)
				}
				if g.partial == part.QuestionID || (g.supplement && i == 5 && stage == "analyzing_reproduction") {
					part.Status = "partial"
				}
			}
			wire.Answers = append(wire.Answers, part)
		}
		if g.supplement && stage == "analyzing_reproduction" {
			wire.SupplementalQueries = []PaperSupplementQuery{{QuestionID: "q6", Query: "supplementomega"}}
		}
		if g.over[stage] {
			count := 0
			for _, part := range wire.Answers {
				count += len(part.Claims)
			}
			for count < 13 {
				wire.Answers[0].Claims = append(wire.Answers[0].Claims, wire.Answers[0].Claims[0])
				count++
			}
		}
		value = wire
	}
	tokens := 4096
	if strings.HasPrefix(stage, "analyzing_") || strings.HasPrefix(stage, "repairing_") {
		tokens = 8192
	}
	if req.MaxTokens != tokens || len(req.Input) > paperInputLimit {
		g.t.Fatalf("reproduction stage budget drifted: %s tokens=%d bytes=%d", stage, req.MaxTokens, len(req.Input))
	}
	result := generation.Result{Content: budgetJSON(g.t, value), CallID: fmt.Sprintf("reproduction-fixture-%d", len(g.calls)), UsageKnown: true, InputTokens: 10, OutputTokens: 20}
	err := req.Validate(result)
	if stage == g.failStage {
		return result, &ModelError{Code: g.failCode}
	}
	return result, err
}

func reproductionWorkflowFixture(t *testing.T) (*fixture, Conversation, Run, *reproductionWorkflowGateway) {
	t.Helper()
	f := newFixture(t)
	pages := []string{}
	for _, keyword := range reproductionWorkflowKeywords {
		pages = append(pages, keyword+" states the original paper configuration.")
	}
	pages = append(pages, "supplementomega states the additional released resource.")
	c, _ := workflowPaper(t, f, pages)
	g := &reproductionWorkflowGateway{t: t, f: f, over: map[string]bool{}}
	f.s.Gateway = g
	r := directSubmit(t, f, c, TaskPaperReproduction, "fulltext")
	return f, c, r, g
}

func installReproductionDenseStructures(t *testing.T, f *fixture) {
	t.Helper()
	elements := []document.StructuredElement{}
	for i := 0; i < 9; i++ {
		element := structuredAgentTable(i+1, reproductionWorkflowKeywords[i%6])
		element.Table.Notes = []string{strings.Repeat("<", 1050)}
		element.Quote = document.StructuredQuote(element)
		must(t, document.ValidateStructuredElement(element))
		elements = append(elements, element)
	}
	f.s.Structured = &structuredAgentProvider{elements: elements}
}

func TestPaperReproductionWorkflowNormalThreeCallsAndAllSixQueries(t *testing.T) {
	for _, scenario := range []string{"complete", "partial", "missing", "reject-one", "reject-all"} {
		t.Run(scenario, func(t *testing.T) {
			f, c, r, g := reproductionWorkflowFixture(t)
			want := "complete"
			switch scenario {
			case "partial":
				g.partial, want = "q6", "partial"
			case "missing":
				g.empty, want = true, "insufficient"
			case "reject-one":
				g.reject, want = "q3", "partial"
			case "reject-all":
				g.reject, want = "all", "insufficient"
			}
			claimed := f.claim(t, r.ID)
			if r.Question != PaperReproductionGoal || claimed.Deadline == nil || time.Until(*claimed.Deadline) < 299*time.Second {
				t.Fatal("fixed reproduction task goal or 300-second budget missing")
			}
			f.s.process(t.Context(), claimed)
			end, messages := paperOutcome(t, f, c, r)
			cp := directCheckpoint(t, f, r)
			if end.State != "completed" || len(messages) != 2 || cp.Calls != 3 || !reflect.DeepEqual(g.stages, []string{"planning_reproduction", "analyzing_reproduction", "validating_paper"}) {
				t.Fatalf("unexpected reproduction execution: %s stages=%v", reproductionRunDiagnostic(end, len(g.calls)), g.stages)
			}
			result := directResult(t, f, r)
			if result.Reproduction == nil || result.Answer != nil || result.Report != nil || result.Reproduction.Status != want || len(result.Reproduction.Categories) != 6 {
				t.Fatalf("wrong independent reproduction result: %+v", result)
			}
			for _, keyword := range reproductionWorkflowKeywords {
				found := false
				for _, source := range cp.Paper.QA.Initial.Evidence {
					found = found || strings.Contains(source.Quote, keyword)
				}
				if !found {
					t.Fatalf("lost retrieval category query %s", keyword)
				}
			}
			if scenario == "reject-one" && (result.Reproduction.Categories[2].Gap == nil || result.Reproduction.Categories[2].Gap.Reason != "review_rejected" || len(result.Reproduction.Categories[2].Items) != 0) {
				t.Fatal("rejected category was published or lost its gap")
			}
			public := string(budgetJSON(t, end)) + string(messages[1].Result)
			if strings.Contains(public, "supplemental_queries") || strings.Contains(public, "datasetalpha") || strings.Contains(public, "normalization_request") {
				t.Fatal("private reproduction planning leaked")
			}
		})
	}
}

func TestPaperReproductionBudgetReservesFourReviewsAndOneRepair(t *testing.T) {
	if runDuration(Run{Task: TaskPaperReproduction}) != 300*time.Second || paperCallLimit(TaskPaperReproduction) != 8 || paperReviewLimit(TaskPaperReproduction) != 4 {
		t.Fatal("reproduction fixed budgets changed")
	}
	now := time.Now()
	for _, repaired := range []bool{false, true} {
		seconds, calls := 185, 2
		if repaired {
			seconds, calls = 155, 3
		}
		for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
			deadline := now.Add(time.Duration(seconds)*time.Second + delta)
			cp := Checkpoint{Calls: calls, Paper: &PaperCheckpoint{}}
			if repaired {
				cp.Paper.Repair = &PaperRepair{Attempted: true, State: "completed"}
			}
			want := ""
			if delta < 0 {
				want = "time_budget"
			}
			if got := paperSupplementBudget(context.Background(), Run{Task: TaskPaperReproduction, Deadline: &deadline}, &cp, now); got != want {
				t.Fatalf("reserve repaired=%t delta=%s got=%s", repaired, delta, got)
			}
			cp.Calls++
			if got := paperSupplementBudget(context.Background(), Run{Task: TaskPaperReproduction, Deadline: &deadline}, &cp, now); got != "call_budget" {
				t.Fatal("reproduction did not reserve remaining calls")
			}
		}
	}
}

func TestPaperReproductionSupplementAndOneSharedRepair(t *testing.T) {
	for _, scenario := range []string{"supplement", "initial-repair", "supplement-repair", "both-overflow"} {
		t.Run(scenario, func(t *testing.T) {
			f, c, r, g := reproductionWorkflowFixture(t)
			g.supplement = scenario != "initial-repair"
			wantCalls, wantState := 4, "completed"
			if scenario == "initial-repair" || scenario == "both-overflow" {
				g.over["analyzing_reproduction"] = true
			}
			if scenario == "supplement-repair" || scenario == "both-overflow" {
				g.over[paperReproductionSupplementStage] = true
			}
			if scenario == "supplement-repair" {
				wantCalls = 5
			}
			if scenario == "both-overflow" {
				wantState = "failed"
			}
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			cp := directCheckpoint(t, f, r)
			if end.State != wantState || cp.Calls != wantCalls || len(g.calls) != wantCalls {
				t.Fatalf("shared repair contract failed: %s stages=%v", reproductionRunDiagnostic(end, len(g.calls)), g.stages)
			}
			if scenario != "supplement" && (cp.Paper.Repair == nil || !cp.Paper.Repair.Attempted || cp.Paper.Repair.State != "completed") {
				t.Fatal("reproduction repair was not durably spent")
			}
			if wantState == "failed" {
				if len(messages) != 1 || end.FailureCode != "output_limit_exceeded" || end.FailureStage != paperReproductionSupplementStage {
					t.Fatal("second overflow did not terminate at its actual stage")
				}
			} else {
				if len(messages) != 2 || directResult(t, f, r).Reproduction.Status != "complete" {
					t.Fatal("complete reviewed reproduction was not published")
				}
				if g.supplement && (cp.Paper.QA.Supplement.State != "completed" || cp.Paper.QA.Supplement.Added != 1 || cp.Paper.Outputs[paperReproductionSupplementStage] == nil) {
					t.Fatal("supplement was omitted or reentered")
				}
			}
		})
	}
}

func TestPaperReproductionRechecksSupplementTimeBeforeReservingCall(t *testing.T) {
	for _, admission := range []bool{false, true} {
		t.Run(fmt.Sprint(admission), func(t *testing.T) {
			f, c, r, g := reproductionWorkflowFixture(t)
			g.supplement = true
			ctx := &supplementDeadlineContext{Context: t.Context(), deadline: time.Now().Add(300 * time.Second)}
			changed := false
			g.beforeStart = func(req ModelRequest) error {
				if req.Schema == paperReproductionSupplementSchemas.full && !changed {
					changed = true
					ctx.deadline = time.Now().Add(180 * time.Second)
					if admission {
						return &ModelError{Code: "busy", Admission: true}
					}
				}
				return nil
			}
			r = f.claim(t, r.ID)
			cp := directCheckpoint(t, f, r)
			must(t, f.s.processPaper(ctx, r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) }))
			end, messages := paperOutcome(t, f, c, r)
			saved := directCheckpoint(t, f, r)
			if !changed || end.State != "completed" || len(messages) != 2 || saved.Calls != 3 || len(g.calls) != 3 || saved.Paper.QA.Supplement.State != "skipped" || saved.Paper.QA.Supplement.Reason != "time_budget" || directResult(t, f, r).Reproduction.Status != "partial" {
				t.Fatalf("unstarted supplement consumed budget or failed the draft: %s stages=%v", reproductionRunDiagnostic(end, len(g.calls)), g.stages)
			}
		})
	}
}

func TestPaperReproductionCompletesEightCallsWithFourWholeReviewBatches(t *testing.T) {
	f, c, r, g := reproductionWorkflowFixture(t)
	installReproductionDenseStructures(t, f)
	g.supplement, g.dense, g.over["analyzing_reproduction"] = true, true, true
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, messages := paperOutcome(t, f, c, r)
	cp := directCheckpoint(t, f, r)
	if end.State != "completed" || len(messages) != 2 || len(g.calls) != 8 || cp.Calls != 8 || len(cp.Paper.ReviewPlan) != 4 {
		t.Fatalf("maximum planned reproduction path failed: %s stages=%v reviews=%d", reproductionRunDiagnostic(end, len(g.calls)), g.stages, len(cp.Paper.ReviewPlan))
	}
	seen := map[string]bool{}
	for _, batch := range cp.Paper.ReviewPlan {
		if len(batch.Request) > paperInputLimit {
			t.Fatal("review exceeded 64 KiB")
		}
		for _, id := range batch.ClaimIDs {
			if seen[id] {
				t.Fatal("reviewed an item twice")
			}
			seen[id] = true
		}
	}
	if len(seen) != 12 {
		t.Fatalf("review did not cover all 12 items: %d", len(seen))
	}
}

func TestPaperReproductionRecoveryReusesAllFrozenStageInputs(t *testing.T) {
	for _, barrier := range []string{"plan-request", "planned", "retrieved", "repair-pending", "repair-completed", "supplement-pending", "supplement-ready", "supplement-completed", "review-plan", "review-between"} {
		t.Run(barrier, func(t *testing.T) {
			f, c, r, g := reproductionWorkflowFixture(t)
			g.supplement = true
			if strings.HasPrefix(barrier, "repair-") {
				g.over["analyzing_reproduction"] = true
			}
			if barrier == "review-between" {
				g.dense = true
				installReproductionDenseStructures(t, f)
			}
			watch := &questionCheckpointStore{Store: f.store, pause: func(cp Checkpoint, _ string) bool {
				if cp.Paper == nil || cp.Paper.QA == nil || cp.Phase != "ready" {
					return false
				}
				pc, qa := cp.Paper, cp.Paper.QA
				switch barrier {
				case "plan-request":
					return qa.NormalizationRequest != ""
				case "planned":
					return pc.Outputs["planning_reproduction"] != nil
				case "retrieved":
					return qa.Initial != nil
				case "repair-pending":
					return pc.Repair != nil && pc.Repair.State == "pending"
				case "repair-completed":
					return pc.Repair != nil && pc.Repair.State == "completed"
				case "supplement-pending":
					return qa.Supplement != nil && qa.Supplement.State == "pending"
				case "supplement-ready":
					return qa.Supplement != nil && qa.Supplement.State == "ready"
				case "supplement-completed":
					return qa.Supplement != nil && qa.Supplement.State == "completed"
				case "review-between":
					return len(pc.ReviewPlan) > 1 && pc.Outputs[paperReviewStage(0)] != nil
				default:
					return pc.ReviewPlan != nil
				}
			}}
			f.s.Store = watch
			r = f.claim(t, r.ID)
			cp := directCheckpoint(t, f, r)
			err := f.s.processPaper(t.Context(), r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) })
			if !errors.Is(err, errContextCheckpointPause) || !watch.paused {
				t.Fatalf("missed recovery barrier %s: %v", barrier, err)
			}
			frozen := directCheckpoint(t, f, r)
			seedPaperContextRun(t, f, c, contextSeed{hash: contextPaperHash(t, f, c), question: "恢复后问题", answer: "不得混入已冻结请求"})
			must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, _ := paperOutcome(t, f, c, r)
			done := directCheckpoint(t, f, r)
			want := 4
			if strings.HasPrefix(barrier, "repair-") {
				want = 5
			}
			if barrier == "review-between" {
				want = 7
			}
			if end.State != "completed" || len(g.calls) != want || done.Calls != want || watch.reads != 1 {
				t.Fatalf("resume replayed calls or reread history: %s stages=%v reads=%d", reproductionRunDiagnostic(end, len(g.calls)), g.stages, watch.reads)
			}
			if done.Paper.QA.NormalizationRequest != frozen.Paper.QA.NormalizationRequest || !reflect.DeepEqual(done.Paper.ConversationContext, frozen.Paper.ConversationContext) {
				t.Fatal("resume changed the frozen context or planner request")
			}
			if frozen.Paper.QA.Initial != nil && !reflect.DeepEqual(done.Paper.QA.Initial, frozen.Paper.QA.Initial) {
				t.Fatal("resume changed initial evidence or request")
			}
			if frozen.Paper.QA.Supplement != nil && frozen.Paper.QA.Supplement.Input != nil && !reflect.DeepEqual(done.Paper.QA.Supplement.Input, frozen.Paper.QA.Supplement.Input) {
				t.Fatal("resume changed supplementary evidence or request")
			}
			if frozen.Paper.ReviewPlan != nil && !reflect.DeepEqual(done.Paper.ReviewPlan, frozen.Paper.ReviewPlan) {
				t.Fatal("resume rebuilt the review plan")
			}
		})
	}
}

type reproductionCrashStore struct {
	Store
	phase, stage string
	paused       bool
}

func (s *reproductionCrashStore) Save(ctx context.Context, r Run, cp Checkpoint, progress string, step *Step) error {
	if err := s.Store.Save(ctx, r, cp, progress, step); err != nil {
		return err
	}
	if !s.paused && cp.Paper != nil && cp.Phase == s.phase && cp.Paper.CurrentStage == s.stage {
		s.paused = true
		// A returned Save error is a known storage failure. A process crash must
		// bypass that handler and leave the durably saved calling state intact.
		panic(errContextCheckpointPause)
	}
	return nil
}

func TestPaperReproductionCallingAndKnownFailureNeverReplay(t *testing.T) {
	for _, scenario := range []string{"analyzing_reproduction", "repairing_reproduction", paperReproductionSupplementStage, "known-failure"} {
		t.Run(scenario, func(t *testing.T) {
			f, c, r, g := reproductionWorkflowFixture(t)
			g.supplement = true
			if scenario == "repairing_reproduction" {
				g.over["analyzing_reproduction"] = true
			}
			if scenario == "known-failure" {
				g.failStage, g.failCode = paperReproductionSupplementStage, "provider_rejected"
			}
			watch := &reproductionCrashStore{Store: f.store, phase: "calling", stage: scenario}
			if scenario == "known-failure" {
				watch.phase, watch.stage = "failed", paperReproductionSupplementStage
			}
			f.s.Store = watch
			r = f.claim(t, r.ID)
			cp := directCheckpoint(t, f, r)
			func() {
				defer func() {
					if recovered := recover(); recovered != nil && recovered != errContextCheckpointPause {
						panic(recovered)
					}
				}()
				_ = f.s.processPaper(t.Context(), r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) })
			}()
			if !watch.paused {
				t.Fatal("missed reserved or failed call checkpoint")
			}
			before := len(g.calls)
			saved := directCheckpoint(t, f, r)
			reserved := before + 1
			if scenario == "known-failure" {
				reserved = before
			}
			if saved.Phase != watch.phase || saved.Paper.CurrentStage != watch.stage || saved.Calls != reserved {
				t.Fatalf("crash did not preserve the reserved snapshot: phase=%s stage=%s calls=%d", saved.Phase, saved.Paper.CurrentStage, saved.Calls)
			}
			must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			code, state, stage := "result_unknown", "unknown", scenario
			if scenario == "known-failure" {
				code, state, stage = "provider_rejected", "failed", paperReproductionSupplementStage
			}
			if end.State != state || end.FailureCode != code || end.FailureStage != stage || len(g.calls) != before || len(messages) != 1 || directCheckpoint(t, f, r).Calls != saved.Calls {
				t.Fatalf("paid/uncertain call was replayed or draft published: %s stages=%v", reproductionRunDiagnostic(end, len(g.calls)), g.stages)
			}
		})
	}
}

func TestPaperReproductionHistoryKeepsTwelveIndicesForOrdinaryFollowup(t *testing.T) {
	f, c, r, g := reproductionWorkflowFixture(t)
	g.longItems = true
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, _ := paperOutcome(t, f, c, r)
	if end.State != "completed" {
		t.Fatalf("long checklist failed: %s", reproductionRunDiagnostic(end, len(g.calls)))
	}
	history, err := f.store.PaperHistory(t.Context(), c.ID, contextPaperHash(t, f, c), "")
	must(t, err)
	if len(history.Turns) != 1 || !history.Truncated || len(history.Turns[0].Answer) > paperHistoryAnswerLimit {
		t.Fatal("history did not use bounded reproduction summary")
	}
	for i := 1; i <= 12; i++ {
		if !strings.Contains(history.Turns[0].Answer, fmt.Sprintf("%d. ", i)) {
			t.Fatalf("history lost item %d", i)
		}
	}
	qa := &workflowGateway{}
	f.s.Gateway = qa
	input := directInput(TaskPaperFollowup, "fulltext")
	input.Question = "解释复现清单第12项的依据"
	next, err := f.s.Submit(t.Context(), f.u.ID, c.ID, input)
	must(t, err)
	f.s.process(t.Context(), f.claim(t, next.ID))
	done, _ := paperOutcome(t, f, c, next)
	if done.State != "completed" || len(qa.calls) != 3 || !strings.Contains(string(qa.calls[0].Input), "12. ") {
		t.Fatalf("ordinary followup lost the reproduction discussion: %s", reproductionRunDiagnostic(done, len(qa.calls)))
	}
}

func TestPaperReproductionTaskCannotReuseOldV12QuestionKey(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	input := directInput(TaskPaperFollowup, "abstract")
	old := seedPaperContextRun(t, f, c, contextSeed{hash: contextPaperHash(t, f, c), question: input.Question, answer: "已有问答", workflow: "paper-fixed-v12"})
	must(t, f.db.Model(&Run{}).Where("id=?", old.ID).Updates(map[string]any{"input_hash": submissionHash(input, "paper-fixed-v12"), "idempotency_key": input.IdempotencyKey}).Error)
	again, err := f.s.Submit(t.Context(), f.u.ID, c.ID, input)
	must(t, err)
	if again.ID != old.ID || again.WorkflowVersion != "paper-fixed-v12" {
		t.Fatal("old key was reinterpreted")
	}
	input.Task = TaskPaperReproduction
	if _, err := f.s.Submit(t.Context(), f.u.ID, c.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("reproduction reused a prior task's idempotency key: %v", err)
	}
	if f.gateway.calls.Load() != 0 {
		t.Fatal("idempotency retry called a model")
	}
}
