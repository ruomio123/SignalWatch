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

type supplementGateway struct {
	workflowGateway
	t               *testing.T
	f               *fixture
	stages          []string
	query           string
	noQueries       bool
	overStages      map[string]bool
	repairBad       bool
	repairQueries   bool
	denseInitial    bool
	denseSupplement bool
	initialCalls    int
	beforeStart     func(ModelRequest) error
	afterStart      func(string) error
	afterResult     func(string, generation.Result, error) error
}

func (g *supplementGateway) Generate(ctx context.Context, req ModelRequest) (generation.Result, error) {
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
	g.calls = append(g.calls, req)
	g.stages = append(g.stages, stage)
	if cp.Phase != "calling" || cp.Calls != g.initialCalls+len(g.calls) {
		g.t.Fatalf("call was not durably reserved: stage=%s phase=%s count=%d calls=%d", stage, cp.Phase, cp.Calls, len(g.calls))
	}
	if stage == "analyzing_answer_supplement" && (cp.Paper.QA.Supplement == nil || cp.Paper.QA.Supplement.State != "calling" || cp.Paper.QA.Supplement.Input == nil || cp.Paper.QA.Supplement.Input.Request != string(req.Input)) {
		g.t.Fatal("supplement started without frozen request and calling state")
	}
	if g.afterStart != nil {
		if err := g.afterStart(stage); err != nil {
			return generation.Result{}, err
		}
	}
	var input struct {
		Evidence  []Citation      `json:"evidence"`
		Claims    []reviewClaim   `json:"claims"`
		Candidate json.RawMessage `json:"candidate"`
	}
	must(g.t, json.Unmarshal(req.Input, &input))
	var value any
	switch {
	case stage == "normalizing_question":
		value = paperQuestionsOutput{Questions: []paperQuestionItemOutput{{Question: "方法及其图结构如何工作？", Query: "retrieval"}}}
	case strings.HasPrefix(stage, "validating_paper"):
		verdicts := []map[string]any{}
		for _, claim := range input.Claims {
			verdicts = append(verdicts, map[string]any{"id": claim.ID, "supported": true})
		}
		value = map[string]any{"verdicts": verdicts}
	case strings.HasPrefix(stage, "repairing_"):
		var candidate paperAnswerOutput
		if len(input.Candidate) > 0 {
			must(g.t, json.Unmarshal(input.Candidate, &candidate))
		} else {
			candidate = singleQuestionAnswer(fieldOutput{Status: "supported", Claims: []claimOutput{{Text: "重新生成的结论来自论文证据。", Evidence: []evidenceOutput{{ID: input.Evidence[0].ID}}}}})
		}
		if !g.repairBad {
			candidate.Answers[0].Claims = candidate.Answers[0].Claims[:1]
			candidate.Answers[0].Claims[0].Text = "整理后保留有证据支持的方法结论。"
		}
		if g.repairQueries {
			candidate.SupplementalQueries = []PaperSupplementQuery{{QuestionID: "q1", Query: "unexpected different query"}}
		}
		value = candidate
	default:
		if len(input.Evidence) == 0 {
			g.t.Fatal("fixture expected at least one source passage")
		}
		answer := singleQuestionAnswer(fieldOutput{Status: "partial", Claims: []claimOutput{{Text: "论文的方法使用检索。", Evidence: []evidenceOutput{{ID: input.Evidence[0].ID}}}}})
		if stage == "analyzing_answer_supplement" {
			answer.Answers[0].Status = "supported"
			answer.Answers[0].Claims = append(answer.Answers[0].Claims, claimOutput{Text: "补充材料说明了图结构。", Evidence: []evidenceOutput{{ID: input.Evidence[len(input.Evidence)-1].ID}}})
		} else if !g.noQueries {
			query := g.query
			if query == "" {
				query = "graph"
			}
			answer.SupplementalQueries = []PaperSupplementQuery{{QuestionID: "q1", Query: query}}
		}
		if g.overStages[stage] {
			for len(answer.Answers[0].Claims) < 7 {
				answer.Answers[0].Claims = append(answer.Answers[0].Claims, answer.Answers[0].Claims[0])
			}
		}
		if g.denseInitial && stage == "analyzing_answer" || g.denseSupplement && stage == paperSupplementStage {
			answer.Answers[0].Claims = make([]claimOutput, 6)
			for i := range answer.Answers[0].Claims {
				refs := []evidenceOutput{}
				for j := 0; j < 3; j++ {
					refs = append(refs, evidenceOutput{ID: input.Evidence[(i*3+j)%len(input.Evidence)].ID})
				}
				answer.Answers[0].Claims[i] = claimOutput{Text: strings.Repeat("a", 1200), Evidence: refs}
			}
		}
		value = answer
	}
	wantTokens := 4096
	if strings.HasPrefix(stage, "analyzing_") || strings.HasPrefix(stage, "repairing_") {
		wantTokens = 8192
	}
	if req.MaxTokens != wantTokens || len(req.Input) > paperInputLimit {
		g.t.Fatalf("incorrect stage budget: %s tokens=%d bytes=%d", stage, req.MaxTokens, len(req.Input))
	}
	result := generation.Result{Content: budgetJSON(g.t, value), CallID: fmt.Sprintf("supplement-fixture-%d", len(g.calls)), UsageKnown: true, InputTokens: 10, OutputTokens: 20}
	err := req.Validate(result)
	if g.afterResult != nil {
		err = g.afterResult(stage, result, err)
	}
	return result, err
}

func supplementFixture(t *testing.T, mode string) (*fixture, Conversation, Run, *supplementGateway) {
	t.Helper()
	f := newFixture(t)
	c, _ := workflowPaper(t, f, []string{"The retrieval method selects source passages.", "The graph structure connects relevant nodes."})
	g := &supplementGateway{t: t, f: f, overStages: map[string]bool{}}
	f.s.Gateway = g
	r := directSubmit(t, f, c, TaskPaperFollowup, mode)
	return f, c, r, g
}

func TestPaperSupplementCompletesWithOneAdditionalAnalysis(t *testing.T) {
	f, c, r, g := supplementFixture(t, "fulltext")
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, messages := paperOutcome(t, f, c, r)
	cp := directCheckpoint(t, f, r)
	want := []string{"normalizing_question", "analyzing_answer", "analyzing_answer_supplement", "validating_paper"}
	if end.State != "completed" || !reflect.DeepEqual(g.stages, want) || cp.Calls != 4 || len(messages) != 2 {
		t.Fatalf("supplement failed or repeated: end=%+v stages=%v count=%d", end, g.stages, cp.Calls)
	}
	supplement := cp.Paper.QA.Supplement
	if supplement == nil || supplement.State != "completed" || supplement.Added != 1 || len(supplement.Queries) != 1 || supplement.Input == nil || len(supplement.Input.Evidence) != 2 {
		t.Fatalf("lost completed supplementary plan: %+v", supplement)
	}
	var initial paperAnswerOutput
	must(t, json.Unmarshal(cp.Paper.Outputs["analyzing_answer"], &initial))
	if string(cp.Paper.Outputs["analyzing_answer"]) == string(cp.Paper.Outputs["analyzing_answer_supplement"]) || initial.Answers[0].Status != "partial" {
		t.Fatal("supplement overwrote initial analysis")
	}
	var input struct {
		Initial json.RawMessage `json:"initial_answer"`
	}
	must(t, json.Unmarshal(g.calls[2].Input, &input))
	var requested paperAnswerOutput
	must(t, json.Unmarshal(input.Initial, &requested))
	if !reflect.DeepEqual(requested, initial) {
		t.Fatal("supplement did not receive the complete initial answer")
	}
	result := directResult(t, f, r)
	if result.Answer == nil || result.Answer.Status != "complete" || len(result.Answer.Parts[0].Claims) != 2 {
		t.Fatalf("final coverage not published: %+v", result)
	}
	var citations []Citation
	must(t, json.Unmarshal(messages[1].Citations, &citations))
	if len(citations) != 2 || citations[0].Quote != cp.Paper.QA.Initial.Evidence[0].Quote || !strings.Contains(citations[1].Quote, "graph") {
		t.Fatalf("initial or supplementary source text lost: %+v", citations)
	}
	public := string(budgetJSON(t, end)) + string(messages[1].Result)
	for _, internal := range []string{"supplemental_queries", "initial_answer", "supplement-fixture", "normalization_request"} {
		if strings.Contains(public, internal) {
			t.Fatalf("private supplementary data leaked: %s", internal)
		}
	}
}

func TestPaperSupplementBudgetUsesDeadlineAndReservesReviewAndRepair(t *testing.T) {
	now := time.Now()
	for _, repaired := range []bool{false, true} {
		seconds, allowedCalls := 95, 4
		for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
			cp := Checkpoint{Calls: allowedCalls, Paper: &PaperCheckpoint{}}
			if repaired {
				cp.Paper.Repair = &PaperRepair{Attempted: true, State: "completed"}
			}
			deadline := now.Add(time.Duration(seconds)*time.Second + delta)
			want := ""
			if delta < 0 {
				want = "time_budget"
			}
			if got := paperSupplementBudget(context.Background(), Run{Deadline: &deadline}, &cp, now); got != want {
				t.Fatalf("repaired=%t delta=%s got=%q want=%q", repaired, delta, got, want)
			}
		}
		deadline := now.Add(180 * time.Second)
		cp := Checkpoint{Calls: allowedCalls + 1, Paper: &PaperCheckpoint{}}
		if repaired {
			cp.Paper.Repair = &PaperRepair{Attempted: true, State: "completed"}
		}
		if got := paperSupplementBudget(context.Background(), Run{Deadline: &deadline}, &cp, now); got != "call_budget" {
			t.Fatalf("did not reserve remaining model calls: %q", got)
		}
		cp.Calls = allowedCalls
		ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Duration(seconds-1)*time.Second))
		if got := paperSupplementBudget(ctx, Run{Deadline: &deadline}, &cp, now); got != "time_budget" {
			t.Fatalf("ignored the earlier context deadline: %q", got)
		}
		cancel()
	}
}

func TestPaperSupplementSkipsWithoutCallingAndStillReviews(t *testing.T) {
	for _, reason := range []string{"no_queries", "abstract_only", "no_new_evidence", "time_budget", "call_budget"} {
		t.Run(reason, func(t *testing.T) {
			mode := "fulltext"
			if reason == "abstract_only" {
				mode = "abstract"
			}
			f, c, r, g := supplementFixture(t, mode)
			if reason == "no_queries" {
				g.noQueries = true
			}
			if reason == "no_new_evidence" {
				g.query = "retrieval"
			}
			r = f.claim(t, r.ID)
			if reason == "time_budget" {
				deadline := time.Now().Add(120 * time.Second)
				r.Deadline = &deadline
				must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("deadline", deadline).Error)
			}
			if reason == "call_budget" {
				cp := directCheckpoint(t, f, r)
				cp.Calls, g.initialCalls = 4, 4
				must(t, f.store.Save(t.Context(), r, cp, "ready", nil))
				r.Checkpoint = budgetJSON(t, cp)
			}
			f.s.process(t.Context(), r)
			end, messages := paperOutcome(t, f, c, r)
			cp := directCheckpoint(t, f, r)
			if end.State != "completed" || len(g.calls) != 3 || cp.Calls != 3+g.initialCalls || len(messages) != 2 || cp.Paper.QA.Supplement == nil || cp.Paper.QA.Supplement.State != "skipped" || cp.Paper.QA.Supplement.Reason != reason {
				t.Fatalf("skip did not retain reviewed initial answer: end=%+v calls=%v checkpoint=%+v", end, g.stages, cp.Paper.QA)
			}
			if cp.Paper.TerminalFailure != "" || directResult(t, f, r).Answer.Status != "partial" {
				t.Fatal("optional skip became terminal or hid the evidence gap")
			}
		})
	}
}

func TestPaperSupplementRecoversEachAnalysisOnceAndRetainsReviewedDraft(t *testing.T) {
	for _, scenario := range []string{"initial-repair", "supplement-repair", "both-overlimit", "repair-overlimit"} {
		t.Run(scenario, func(t *testing.T) {
			f, c, r, g := supplementFixture(t, "fulltext")
			wantRepairs := []string{"repairing_answer"}
			wantCalls := 5
			g.overStages["analyzing_answer"] = scenario == "initial-repair" || scenario == "both-overlimit"
			g.overStages["analyzing_answer_supplement"] = scenario != "initial-repair"
			if scenario == "supplement-repair" || scenario == "repair-overlimit" {
				wantRepairs = []string{"repairing_answer_supplement"}
			}
			if scenario == "both-overlimit" {
				wantRepairs = []string{"repairing_answer", "repairing_answer_supplement"}
				wantCalls = 6
			}
			g.repairBad = scenario == "repair-overlimit"
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			cp := directCheckpoint(t, f, r)
			repairs := []string{}
			for _, stage := range g.stages {
				if strings.HasPrefix(stage, "repairing_") {
					repairs = append(repairs, stage)
				}
			}
			if end.State != "completed" || !reflect.DeepEqual(repairs, wantRepairs) || cp.Calls != len(g.calls) || cp.Calls != wantCalls || len(messages) != 2 || paperRecoveryUsed(cp.Paper) != len(wantRepairs) {
				t.Fatalf("repair ownership/limit broken: end=%+v stages=%v count=%d", end, g.stages, cp.Calls)
			}
			if scenario == "repair-overlimit" {
				result := directResult(t, f, r)
				if result.Outcome != "partial" || len(result.Issues) != 1 || result.Issues[0].Code != "output_limit_exceeded" || cp.Paper.QA.Supplement.State != "failed" || cp.Paper.Outputs[paperSupplementStage] != nil || len(result.Answer.Parts[0].Claims) != 1 {
					t.Fatal("failed optional analysis did not retain only the reviewed initial draft")
				}
			} else {
				for _, repairStage := range wantRepairs {
					original := "analyzing_" + strings.TrimPrefix(repairStage, "repairing_")
					repair := cp.Paper.Repairs[original]
					if repair == nil || repair.State != "completed" || string(cp.Paper.Outputs[original]) != string(cp.Paper.Outputs[repairStage]) {
						t.Fatal("repair did not promote only its original stage")
					}
				}
			}
		})
	}
}

type supplementCrashStore struct {
	Store
	point   string
	after   bool
	tripped bool
}

func (s *supplementCrashStore) Save(ctx context.Context, r Run, cp Checkpoint, progress string, step *Step) error {
	match := false
	if cp.Paper != nil && cp.Paper.QA != nil && cp.Paper.QA.Supplement != nil {
		supplement := cp.Paper.QA.Supplement
		switch s.point {
		case "pending":
			match = supplement.State == "pending"
		case "ready":
			match = supplement.State == "ready" && supplement.Input != nil
		case "calling":
			match = supplement.State == "calling" && cp.Phase == "calling" && step == nil
		case "result":
			match = step != nil && step.Tool == "analyzing_answer_supplement"
		case "repair-candidate":
			match = step != nil && step.Tool == "analyzing_answer_supplement" && step.FailureCode == "output_limit_exceeded"
		case "repair-calling":
			match = cp.Paper.CurrentStage == "repairing_answer_supplement" && cp.Phase == "calling" && step == nil
		case "repair-result":
			match = step != nil && step.Tool == "repairing_answer_supplement"
		}
	}
	if !s.tripped && match {
		s.tripped = true
		if s.after {
			if err := s.Store.Save(ctx, r, cp, progress, step); err != nil {
				return err
			}
		}
		panic(errRepairCheckpointPause)
	}
	return s.Store.Save(ctx, r, cp, progress, step)
}

func TestPaperSupplementRecoveryNeverReplaysUncertainCalls(t *testing.T) {
	for _, point := range []string{"pending", "ready", "calling", "result", "repair-candidate", "repair-calling", "repair-result"} {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/after=%t", point, after), func(t *testing.T) {
				f, c, r, g := supplementFixture(t, "fulltext")
				withRepair := strings.HasPrefix(point, "repair-")
				g.overStages[paperSupplementStage] = withRepair
				r = f.claim(t, r.ID)
				cp := directCheckpoint(t, f, r)
				crash := &supplementCrashStore{Store: f.store, point: point, after: after}
				f.s.Store = crash
				func() {
					defer func() {
						if recovered := recover(); recovered != errRepairCheckpointPause {
							t.Fatalf("wrong crash: %v", recovered)
						}
					}()
					if err := f.s.processPaper(t.Context(), r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) }); err != nil {
						t.Fatal(err)
					}
				}()
				if !crash.tripped {
					t.Fatal("missed crash barrier")
				}
				saved := directCheckpoint(t, f, r)
				var frozen *PaperAnswerInput
				if saved.Paper.QA.Supplement != nil {
					frozen = saved.Paper.QA.Supplement.Input
				}
				f.s.Store = f.store
				if frozen != nil {
					must(t, f.db.Model(&document.Chunk{}).Where("document_id=?", cp.DocumentID).Update("text", "Changed reader cannot replace frozen evidence.").Error)
				}
				must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
				f.s.process(t.Context(), f.claim(t, r.ID))
				end, messages := paperOutcome(t, f, c, r)
				stored := directCheckpoint(t, f, r)
				unknown := point == "calling" && after || point == "result" && !after || point == "repair-candidate" && !after || point == "repair-calling" && after || point == "repair-result" && !after
				if unknown {
					wantCalls, wantReserved, wantStage := 2, 3, paperSupplementStage
					if point == "result" {
						wantCalls = 3
					}
					if point == "repair-candidate" {
						wantCalls = 3
					}
					if point == "repair-calling" || point == "repair-result" {
						wantCalls, wantReserved, wantStage = 3, 4, "repairing_answer_supplement"
						if point == "repair-result" {
							wantCalls = 4
						}
					}
					if end.State != "unknown" || end.FailureCode != "result_unknown" || end.FailureStage != wantStage || len(g.calls) != wantCalls || len(messages) != 1 || stored.Calls != wantReserved {
						t.Fatalf("uncertain supplement replayed: end=%+v stages=%v count=%d", end, g.stages, stored.Calls)
					}
					return
				}
				wantCalls := 4
				if withRepair {
					wantCalls = 5
				}
				if end.State != "completed" || len(g.calls) != wantCalls || stored.Calls != wantCalls || len(messages) != 2 || stored.Paper.QA.Supplement.State != "completed" {
					t.Fatalf("safe recovery failed: end=%+v stages=%v count=%d", end, g.stages, stored.Calls)
				}
				if frozen != nil && !reflect.DeepEqual(frozen, stored.Paper.QA.Supplement.Input) {
					t.Fatal("resume changed frozen supplementary request or evidence")
				}
			})
		}
	}
}

func TestPaperSupplementUnknownFailuresStopAndTruncationRegenerates(t *testing.T) {
	for _, code := range []string{"timeout", "network_error", "result_unknown", "provider_rejected", "output_truncated"} {
		t.Run(code, func(t *testing.T) {
			f, c, r, g := supplementFixture(t, "fulltext")
			g.afterResult = func(stage string, _ generation.Result, validation error) error {
				if stage == "analyzing_answer_supplement" {
					return &ModelError{Code: code}
				}
				return validation
			}
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			cp := directCheckpoint(t, f, r)
			if code == "output_truncated" {
				if end.State != "completed" || len(messages) != 2 || len(g.calls) != 5 || cp.Calls != 5 || cp.Paper.Repair == nil || cp.Paper.Repair.Kind != "truncated" || cp.Paper.Repair.State != "completed" || cp.Paper.TerminalFailure != "" {
					t.Fatalf("known truncation did not regenerate and review: state=%s code=%s stages=%v", end.State, end.FailureCode, g.stages)
				}
				return
			}
			if end.State == "completed" || end.FailureCode != code || end.FailureStage != "analyzing_answer_supplement" || len(messages) != 1 || len(g.calls) != 3 || cp.Calls != 3 || cp.Paper.Repair != nil {
				t.Fatalf("actual supplementary failure was hidden: end=%+v stages=%v", end, g.stages)
			}
		})
	}
}

func TestPaperSupplementChecksAccessAndCancellationBeforeStarting(t *testing.T) {
	for _, scenario := range []string{"access", "lease", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			f, c, r, g := supplementFixture(t, "fulltext")
			r = f.claim(t, r.ID)
			cp := directCheckpoint(t, f, r)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			check := func(context.Context) error {
				if cp.Paper != nil && cp.Paper.Outputs["analyzing_answer"] != nil {
					switch scenario {
					case "access":
						return ErrNotFound
					case "lease":
						return ErrLease
					default:
						cancel()
						return context.Canceled
					}
				}
				return nil
			}
			err := f.s.processPaper(ctx, r, c, &cp, check)
			if err == nil || len(g.calls) != 2 || cp.Calls != 2 {
				t.Fatalf("supplement began after %s: error=%v stages=%v count=%d", scenario, err, g.stages, cp.Calls)
			}
			if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("wrong cancellation: %v", err)
			}
			_, messages := paperOutcome(t, f, c, r)
			if len(messages) != 1 {
				t.Fatal("published despite access loss or cancellation")
			}
		})
	}
}

func TestPaperSupplementRepairCannotChangeRetrievalDecision(t *testing.T) {
	f, c, r, g := supplementFixture(t, "fulltext")
	g.overStages["analyzing_answer"], g.repairQueries = true, true
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, messages := paperOutcome(t, f, c, r)
	if end.State != "failed" || end.FailureCode != "output_schema_mismatch" || end.FailureStage != "repairing_answer" || len(g.calls) != 3 || len(messages) != 1 || end.FailureDetail == nil || end.FailureDetail.Rule != "repair_queries_unchanged" {
		t.Fatalf("repair changed the retrieval decision: end=%+v stages=%v", end, g.stages)
	}
}

type supplementDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c *supplementDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestPaperSupplementRechecksBudgetAfterAdmissionBeforeCalling(t *testing.T) {
	for _, admission := range []bool{false, true} {
		t.Run(fmt.Sprint(admission), func(t *testing.T) {
			f, c, r, g := supplementFixture(t, "fulltext")
			r = f.claim(t, r.ID)
			cp := directCheckpoint(t, f, r)
			ctx := &supplementDeadlineContext{Context: t.Context(), deadline: time.Now().Add(180 * time.Second)}
			changed := false
			g.beforeStart = func(req ModelRequest) error {
				var input map[string]json.RawMessage
				must(t, json.Unmarshal(req.Input, &input))
				if input["initial_answer"] != nil && !changed {
					changed = true
					ctx.deadline = time.Now().Add(120 * time.Second)
					if admission {
						return &ModelError{Code: "busy", Admission: true}
					}
				}
				return nil
			}
			must(t, f.s.processPaper(ctx, r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) }))
			end, messages := paperOutcome(t, f, c, r)
			stored := directCheckpoint(t, f, r)
			if !changed || end.State != "completed" || len(g.calls) != 3 || stored.Calls != 3 || len(messages) != 2 || stored.Paper.QA.Supplement.State != "skipped" || stored.Paper.QA.Supplement.Reason != "time_budget" || stored.Paper.TerminalFailure != "" {
				t.Fatalf("unstarted optional call was counted or failed the run: end=%+v stages=%v cp=%+v", end, g.stages, stored.Paper.QA.Supplement)
			}
		})
	}
}

func TestPaperSupplementInputBudgetKeepsInitialAnswerForCompleteReview(t *testing.T) {
	f, c, r, g := supplementFixture(t, "fulltext")
	r = f.claim(t, r.ID)
	installEscapedSupplementEvidence(t, f, c, r)
	g.denseInitial = true
	f.s.process(t.Context(), r)
	end, messages := paperOutcome(t, f, c, r)
	cp := directCheckpoint(t, f, r)
	if end.State != "completed" || len(messages) != 2 || len(g.calls) != 4 || cp.Paper.QA.Supplement.State != "skipped" || cp.Paper.QA.Supplement.Reason != "input_budget" || len(cp.Paper.ReviewPlan) != 2 {
		t.Fatalf("oversized supplementary input lost the draft or skipped its reviews: end=%+v stages=%v plan=%+v", end, g.stages, cp.Paper.QA.Supplement)
	}
	for _, batch := range cp.Paper.ReviewPlan {
		if len(batch.Request) > paperInputLimit {
			t.Fatal("review exceeded serialized input limit")
		}
	}
	if len(directResult(t, f, r).Answer.Parts[0].Claims) != 6 {
		t.Fatal("initial facts were cut to fit the supplementary budget")
	}
}

func installEscapedSupplementEvidence(t *testing.T, f *fixture, c Conversation, r Run) {
	t.Helper()
	if gateway, ok := f.s.Gateway.(*supplementGateway); ok {
		// Exercise escaping/64 KiB limits without a smaller model window
		// replacing this fixture's complete source passages first.
		gateway.limits = generation.ModelLimits{ContextTokens: 262144, MaxOutputTokens: 8192}
		gateway.callTimeout = 30 * time.Second
	}
	var pc Checkpoint
	_, _, err := f.s.preparePaper(t.Context(), r, c, &pc, func(context.Context) error { return nil })
	must(t, err)
	chunkText := strings.Repeat("<", 991) + "retrieval"
	chunks := []document.Chunk{{DocumentID: pc.DocumentID, Page: 1, Number: 1, Text: strings.Repeat("<", 1000) + chunkText + strings.Repeat("<", 1000)}}
	for i := 2; i <= 8; i++ {
		chunks = append(chunks, document.Chunk{DocumentID: pc.DocumentID, Page: i, Number: i, Text: chunkText})
	}
	chunks = append(chunks, document.Chunk{DocumentID: pc.DocumentID, Page: 9, Number: 9, Text: strings.Repeat("<", 995) + "graph"})
	must(t, f.db.Where("document_id=?", pc.DocumentID).Delete(&document.Chunk{}).Error)
	must(t, f.db.Create(&chunks).Error)
}

func TestPaperSupplementSixCallsIncludeRepairAndTwoResumableReviews(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint(restart), func(t *testing.T) {
			f, c, r, g := supplementFixture(t, "fulltext")
			r = f.claim(t, r.ID)
			installEscapedSupplementEvidence(t, f, c, r)
			g.overStages["analyzing_answer"], g.denseSupplement = true, true
			var frozenPlan []PaperReviewBatch
			if restart {
				watch := &questionCheckpointStore{Store: f.store, pause: func(cp Checkpoint, _ string) bool {
					return cp.Paper != nil && len(cp.Paper.ReviewPlan) == 2 && cp.Paper.Outputs["validating_paper"] != nil
				}}
				f.s.Store = watch
				cp := directCheckpoint(t, f, r)
				err := f.s.processPaper(t.Context(), r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) })
				if !errors.Is(err, errContextCheckpointPause) || len(g.calls) != 5 {
					t.Fatalf("missed between-review boundary: %v stages=%v", err, g.stages)
				}
				frozenPlan = directCheckpoint(t, f, r).Paper.ReviewPlan
				_, messages := paperOutcome(t, f, c, r)
				if len(messages) != 1 {
					t.Fatal("published before every review batch completed")
				}
				f.s.Store = f.store
				must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
				r = f.claim(t, r.ID)
			}
			f.s.process(t.Context(), r)
			end, messages := paperOutcome(t, f, c, r)
			cp := directCheckpoint(t, f, r)
			want := []string{"normalizing_question", "analyzing_answer", "repairing_answer", paperSupplementStage, "validating_paper", "validating_paper_2"}
			if end.State != "completed" || len(messages) != 2 || cp.Calls != 6 || !reflect.DeepEqual(g.stages, want) || len(cp.Paper.ReviewPlan) != 2 || cp.Paper.QA.Supplement.State != "completed" {
				t.Fatalf("six-call route failed: end=%+v stages=%v count=%d", end, g.stages, cp.Calls)
			}
			if restart && !reflect.DeepEqual(frozenPlan, cp.Paper.ReviewPlan) {
				t.Fatal("restart changed serialized review requests")
			}
			if len(directResult(t, f, r).Answer.Parts[0].Claims) != 6 {
				t.Fatal("review lost final claims")
			}
		})
	}
}

func TestPaperSupplementPackingPreservesCitedPassagesAndBoundsFreshEvidence(t *testing.T) {
	questions := []PaperQuestion{{ID: "q1", Question: "方法的证据？", Query: "method"}}
	r := Run{Question: "原问题不应截断🙂"}
	for _, escaped := range []bool{false, true} {
		t.Run(fmt.Sprint(escaped), func(t *testing.T) {
			count, quote := 24, "完整原文证据🙂"
			if escaped {
				count, quote = 10, strings.Repeat("<", 990)+"中文🙂"
			}
			pc := &PaperCheckpoint{Mode: "fulltext", ConversationContext: PaperConversationContext{Turns: []PaperConversationTurn{
				{Question: strings.Repeat("Q", 2000), Answer: strings.Repeat("A", 4000)},
				{Question: strings.Repeat("R", 2000), Answer: strings.Repeat("B", 4000)},
				{Question: strings.Repeat("S", 2000), Answer: strings.Repeat("C", 4000)},
			}}}
			initial := PaperAnswerInput{Evidence: []Citation{}}
			for i := 0; i < count; i++ {
				initial.Evidence = append(initial.Evidence, Citation{ID: fmt.Sprintf("old-%d", i), Quote: quote, DocumentID: "doc", ContentHash: "hash", Page: i + 1})
			}
			candidate := singleQuestionAnswer(fieldOutput{Status: "partial", Claims: []claimOutput{{Text: "已知方法事实。", Evidence: []evidenceOutput{{ID: "old-0"}}}}})
			candidate.SupplementalQueries = []PaperSupplementQuery{{QuestionID: "q1", Query: "new evidence"}}
			raw := budgetJSON(t, candidate)
			additions := []Citation{initial.Evidence[0]}
			for i := 0; i < 12; i++ {
				additions = append(additions, Citation{ID: fmt.Sprintf("new-%d", i), Quote: quote, DocumentID: "doc", ContentHash: "hash", Page: 30 + i})
			}
			additions = append(additions, additions[1])
			snapshot := string(budgetJSON(t, pc.ConversationContext))
			packed, added, err := packPaperSupplementInput(r, pc, questions, initial, raw, additions)
			must(t, err)
			if added != 8 || len(packed.Evidence) > 32 || len(packed.Request) > paperInputLimit || packed.Evidence[0] != initial.Evidence[0] {
				t.Fatalf("evidence budget or pinned source lost: added=%d total=%d bytes=%d", added, len(packed.Evidence), len(packed.Request))
			}
			var request struct {
				Initial  paperAnswerOutput        `json:"initial_answer"`
				Fresh    []string                 `json:"new_evidence_ids"`
				Evidence []evidencePassage        `json:"evidence"`
				Context  PaperConversationContext `json:"conversation_context"`
			}
			must(t, json.Unmarshal([]byte(packed.Request), &request))
			if !reflect.DeepEqual(request.Initial, candidate) || len(request.Fresh) != 8 || len(request.Evidence) != len(packed.Evidence) {
				t.Fatal("complete initial answer or new-source provenance changed")
			}
			seen := map[string]bool{}
			for i, source := range packed.Evidence {
				if seen[source.ID] || source.Quote != quote || request.Evidence[i].Quote != quote || request.Evidence[i].ID != source.ID {
					t.Fatal("duplicate, clipped or changed source selected")
				}
				seen[source.ID] = true
			}
			if escaped && (len(request.Context.Turns) != 0 || len(packed.Evidence) >= count+8) {
				t.Fatal("packing did not remove background before uncited old passages")
			}
			if string(budgetJSON(t, pc.ConversationContext)) != snapshot {
				t.Fatal("packing mutated history snapshot")
			}
		})
	}
}

func TestPaperClaimSkipsLockedLargeCheckpointAndReturnsCompleteRun(t *testing.T) {
	f, first, older, _ := supplementFixture(t, "fulltext")
	second, _ := workflowPaper(t, f, nil)
	newer := directSubmit(t, f, second, TaskPaperFollowup, "abstract")
	now := time.Now().UTC()
	payload := budgetJSON(t, Checkpoint{Phase: "ready", Paper: &PaperCheckpoint{QA: &PaperQACheckpoint{NormalizationRequest: strings.Repeat("large-checkpoint-", 32768)}}})
	must(t, f.db.Model(&Run{}).Where("id=?", older.ID).Updates(map[string]any{"created_at": now.Add(-2 * time.Second), "checkpoint": payload}).Error)
	must(t, f.db.Model(&Run{}).Where("id=?", newer.ID).Update("created_at", now.Add(-time.Second)).Error)
	tx := f.db.Begin()
	must(t, tx.Error)
	defer tx.Rollback()
	var locked struct{ ID string }
	must(t, tx.Raw("SELECT id FROM agent_runs WHERE id=? FOR UPDATE", older.ID).Scan(&locked).Error)
	if locked.ID != older.ID {
		t.Fatal("did not lock the oldest pending run")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	claimed, err := f.store.Claim(ctx, "competing-new-worker")
	must(t, err)
	if claimed.ID != newer.ID || claimed.Task != TaskPaperFollowup || claimed.Question != newer.Question || claimed.Provider != newer.Provider || claimed.ContextMode != "abstract" || claimed.WorkflowVersion != PaperWorkflowVersion || claimed.Epoch != 1 || claimed.LeaseOwner != "competing-new-worker" || claimed.Deadline == nil {
		t.Fatalf("narrow selection lost full run fields or skipped locking: %+v", claimed)
	}
	must(t, tx.Rollback().Error)
	claimed, err = f.store.Claim(t.Context(), "oldest-worker")
	must(t, err)
	if claimed.ID != older.ID || claimed.ConversationID != first.ID || claimed.ContextMode != "fulltext" || claimed.Question != older.Question || len(claimed.Checkpoint) < 512<<10 || claimed.Deadline == nil || time.Until(*claimed.Deadline) < 170*time.Second {
		t.Fatalf("large checkpoint sort or full reload failed: id=%s bytes=%d context=%s", claimed.ID, len(claimed.Checkpoint), claimed.ContextMode)
	}
	var restored Checkpoint
	must(t, json.Unmarshal(claimed.Checkpoint, &restored))
	if restored.Paper.QA.NormalizationRequest != strings.Repeat("large-checkpoint-", 32768) {
		t.Fatal("claim truncated the persisted request")
	}
}
