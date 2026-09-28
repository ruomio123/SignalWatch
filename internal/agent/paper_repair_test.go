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

	"signalwatch/internal/generation"
)

// This fake checks the durable checkpoint at the exact external-call boundary.
// It never calls a real provider, including error and settlement simulations.
type paperRepairGateway struct {
	workflowGateway
	t               *testing.T
	f               *fixture
	overFields      map[string]bool
	textLimit       bool
	candidateText   string
	mixedReference  bool
	repairBad       bool
	admissions      int
	initialCalls    int
	stages          []string
	beforeResult    func(string, generation.Result) error
	afterValidation func(string, generation.Result, error) error
}

func (g *paperRepairGateway) Generate(ctx context.Context, req ModelRequest) (generation.Result, error) {
	if g.admissions > 0 {
		g.admissions--
		return generation.Result{}, &ModelError{Code: "busy", Admission: true}
	}
	if err := req.Before(ctx); err != nil {
		return generation.Result{}, err
	}
	g.calls = append(g.calls, req)
	cp := directCheckpoint(g.t, g.f, req.Run)
	if cp.Phase != "calling" || cp.Calls != g.initialCalls+len(g.calls) {
		g.t.Fatalf("external request started without durable call occupancy: phase=%s calls=%d actual=%d", cp.Phase, cp.Calls, g.initialCalls+len(g.calls))
	}
	var input struct {
		Field    string        `json:"field"`
		Evidence []Citation    `json:"evidence"`
		Claims   []reviewClaim `json:"claims"`
	}
	if err := json.Unmarshal(req.Input, &input); err != nil {
		return generation.Result{}, err
	}
	repair := cp.Paper.Repair != nil && cp.Paper.Repair.Attempted && cp.Paper.Repair.State != "completed"
	stage := "analyzing_" + input.Field
	var value any
	switch {
	case repair:
		stage = "repairing_" + cp.Paper.Repair.Field
		if !cp.Paper.Repair.Attempted || string(req.Input) != cp.Paper.Repair.Request {
			g.t.Fatal("repair did not use its durable request")
		}
		var candidate fieldOutput
		if cp.Paper.Repair.Field == "answer" {
			var answer paperAnswerOutput
			must(g.t, json.Unmarshal(cp.Paper.Repair.Candidate, &answer))
			candidate = fieldOutput{Status: answer.Answers[0].Status, Claims: answer.Answers[0].Claims}
		} else {
			must(g.t, json.Unmarshal(cp.Paper.Repair.Candidate, &candidate))
		}
		claim := candidate.Claims[0]
		claim.Text = "整理后的结论仍受本轮原文支持。"
		value = fieldOutput{Status: "supported", Claims: []claimOutput{claim}}
		if g.repairBad {
			value = candidate
		}
	case strings.Contains(req.System, "Resolve pronouns"):
		stage = "normalizing_question"
		value = paperQuestionsOutput{Questions: []paperQuestionItemOutput{{Question: "论文使用什么方法？", Query: "retrieval method experiment"}}}
	case strings.Contains(req.System, "Check EVERY"):
		stage = "validating_paper"
		verdicts := []map[string]any{}
		for _, c := range input.Claims {
			verdicts = append(verdicts, map[string]any{"id": c.ID, "supported": true})
		}
		value = map[string]any{"verdicts": verdicts}
	default:
		count := 1
		if g.overFields[input.Field] && !g.textLimit {
			count = paperFieldPolicyFor(input.Field).MaxClaims + 1
		}
		claims := make([]claimOutput, count)
		for i := range claims {
			claims[i] = claimOutput{Text: fmt.Sprintf("论文的第%d条结论。", i+1), Evidence: []evidenceOutput{{ID: input.Evidence[0].ID}}}
		}
		if g.overFields[input.Field] && g.textLimit {
			claims[0].Text = strings.Repeat("中", 401)
		}
		if g.overFields[input.Field] && g.candidateText != "" {
			for i := range claims {
				claims[i].Text = g.candidateText
			}
		}
		if g.overFields[input.Field] && g.mixedReference {
			claims[len(claims)-1].Evidence[0].ID = "forged-reference"
		}
		value = fieldOutput{Status: "supported", Claims: claims}
	}
	if input.Field == "answer" {
		value = singleQuestionAnswer(value.(fieldOutput))
	}
	g.stages = append(g.stages, stage)
	wantTokens := 4096
	if strings.HasPrefix(stage, "analyzing_") || strings.HasPrefix(stage, "repairing_") {
		wantTokens = 8192
	}
	if req.MaxTokens != wantTokens {
		g.t.Fatalf("%s token budget=%d want=%d", stage, req.MaxTokens, wantTokens)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return generation.Result{}, err
	}
	result := generation.Result{CallID: fmt.Sprintf("repair-fixture-%d", len(g.calls)), Content: raw, UsageKnown: true, InputTokens: 10, OutputTokens: 20}
	if g.beforeResult != nil {
		if err := g.beforeResult(stage, result); err != nil {
			return result, err
		}
	}
	err = req.Validate(result)
	if g.afterValidation != nil {
		err = g.afterValidation(stage, result, err)
	}
	return result, err
}

func repairWorkflowFixture(t *testing.T, task string, overFields ...string) (*fixture, Conversation, Run, *paperRepairGateway) {
	t.Helper()
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	g := &paperRepairGateway{t: t, f: f, overFields: map[string]bool{}}
	for _, field := range overFields {
		g.overFields[field] = true
	}
	f.s.Gateway = g
	var r Run
	if task == TaskPaperReport {
		r = submitReport(t, f, c, "abstract")
	} else {
		r = directSubmit(t, f, c, "", "abstract")
	}
	return f, c, r, g
}

func TestPaperRepairOneSuccessKeepsOtherFieldsAndEvidence(t *testing.T) {
	for _, task := range []string{TaskPaperReport, TaskPaperFollowup} {
		for _, textLimit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/text=%t", task, textLimit), func(t *testing.T) {
				field, wantCalls := "results", 7
				if task == TaskPaperFollowup {
					field, wantCalls = "answer", 4
				}
				f, c, r, g := repairWorkflowFixture(t, task, field)
				g.textLimit = textLimit
				f.s.process(t.Context(), f.claim(t, r.ID))
				end, messages := paperOutcome(t, f, c, r)
				cp := directCheckpoint(t, f, r)
				if end.State != "completed" || len(g.calls) != wantCalls || cp.Calls != wantCalls || len(messages) != 2 || cp.Paper.Repair == nil || !cp.Paper.Repair.Attempted || cp.Paper.Repair.State != "completed" {
					t.Fatalf("repair failed: state=%s failure=%s calls=%d cp=%+v", end.State, end.FailureCode, len(g.calls), cp)
				}
				if !strings.Contains(messages[1].Content, "整理后的结论") || strings.Contains(messages[1].Content, strings.Repeat("中", 20)) {
					t.Fatal("published candidate instead of repaired output")
				}
				if string(cp.Paper.Outputs["analyzing_"+field]) != string(cp.Paper.Outputs["repairing_"+field]) {
					t.Fatal("successful repair was not atomically promoted")
				}
				steps, err := f.store.Steps(t.Context(), f.u.ID, r.ID)
				must(t, err)
				failures, repairs := 0, 0
				for _, step := range steps {
					if step.Tool == "analyzing_"+field && step.FailureCode == "output_limit_exceeded" {
						failures++
					}
					if step.Tool == "repairing_"+field && step.FailureCode == "" {
						repairs++
					}
				}
				if failures != 1 || repairs != 1 {
					t.Fatalf("lost original failed call or repair record: %+v", steps)
				}
				public := string(budgetJSON(t, end))
				for _, internal := range []string{"candidate", "source_passages", "target_limits", "repair-fixture-"} {
					if strings.Contains(public, internal) {
						t.Fatalf("internal repair data leaked: %s", public)
					}
				}
				var citations []Citation
				must(t, json.Unmarshal(messages[1].Citations, &citations))
				if len(citations) == 0 || !strings.Contains(citations[0].Quote, "retrieval") {
					t.Fatal("repair lost server-bound source evidence")
				}
			})
		}
	}
}

func TestPaperRepairOnlyOneAttemptAcrossWholeTask(t *testing.T) {
	for _, scenario := range []string{"repair still exceeds", "later field exceeds", "mixed reference"} {
		t.Run(scenario, func(t *testing.T) {
			f, c, r, g := repairWorkflowFixture(t, TaskPaperReport, "results")
			wantCalls, wantCode, wantRepairs := 5, "output_limit_exceeded", 1
			wantStage := "repairing_results"
			switch scenario {
			case "repair still exceeds":
				g.repairBad = true
			case "later field exceeds":
				g.overFields["limitations"] = true
				wantCalls = 6
				wantStage = "analyzing_limitations"
			case "mixed reference":
				g.mixedReference = true
				wantCalls, wantCode, wantRepairs = 4, "evidence_id_unknown", 0
				wantStage = "analyzing_results"
			}
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			cp := directCheckpoint(t, f, r)
			repairs := 0
			for _, stage := range g.stages {
				if strings.HasPrefix(stage, "repairing_") {
					repairs++
				}
			}
			if end.State != "failed" || end.FailureCode != wantCode || end.FailureStage != wantStage || len(g.calls) != wantCalls || cp.Calls != wantCalls || repairs != wantRepairs || len(messages) != 1 {
				t.Fatalf("unbounded repair or partial publication: end=%+v calls=%d repairs=%d", end, len(g.calls), repairs)
			}
			if end.FailureDetail == nil || end.FailureDetail.Code != wantCode {
				t.Fatal("final output failure lost its diagnostic")
			}
			if scenario == "later field exceeds" && (end.FailureDetail.Count == nil || *end.FailureDetail.Count != 7 || end.FailureDetail.Limit == nil || *end.FailureDetail.Limit != 6) {
				t.Fatal("earlier results limit hid the later limitations failure")
			}
		})
	}
}

func TestPaperRepairNeverOverridesActualCallFailure(t *testing.T) {
	for _, stage := range []string{"analyzing_results", "repairing_results"} {
		for _, code := range []string{"timeout", "network_error", "result_unknown", "provider_rejected", "output_truncated", "storage_failed"} {
			t.Run(stage+"/"+code, func(t *testing.T) {
				f, c, r, g := repairWorkflowFixture(t, TaskPaperReport, "results")
				// Settlement failure can arrive after a complete response has already
				// produced a repairable validation error. The final outcome wins.
				g.afterValidation = func(at string, _ generation.Result, validation error) error {
					if at == stage {
						return &ModelError{Code: code}
					}
					return validation
				}
				f.s.process(t.Context(), f.claim(t, r.ID))
				end, messages := paperOutcome(t, f, c, r)
				cp := directCheckpoint(t, f, r)
				wantCalls := 4
				if strings.HasPrefix(stage, "repairing_") {
					wantCalls = 5
				}
				if end.FailureCode != code || end.FailureStage != stage || len(g.calls) != wantCalls || cp.Calls != wantCalls || len(messages) != 1 || end.FailureDetail != nil {
					t.Fatalf("original over-limit hid actual failure: end=%+v calls=%d", end, len(g.calls))
				}
			})
		}
	}
}

type paperRepairCrashStore struct {
	Store
	point   string
	after   bool
	tripped bool
}

var errRepairCheckpointPause = errors.New("simulated repair checkpoint interruption")

func (s *paperRepairCrashStore) Save(ctx context.Context, r Run, cp Checkpoint, progress string, step *Step) error {
	match := false
	if cp.Paper != nil && cp.Paper.Repair != nil {
		switch s.point {
		case "candidate":
			match = step != nil && step.Tool == "analyzing_results" && step.FailureCode == "output_limit_exceeded"
		case "start":
			match = cp.Phase == "calling" && cp.Paper.Repair.Attempted && step == nil
		case "result":
			match = step != nil && step.Tool == "repairing_results" && cp.Paper.Repair.State == "completed"
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

func TestPaperRepairCheckpointRecoveryDoesNotReplayExternalCalls(t *testing.T) {
	for _, point := range []string{"candidate", "start", "result"} {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/after=%t", point, after), func(t *testing.T) {
				f, c, r, g := repairWorkflowFixture(t, TaskPaperReport, "results")
				r = f.claim(t, r.ID)
				cp := directCheckpoint(t, f, r)
				crash := &paperRepairCrashStore{Store: f.store, point: point, after: after}
				f.s.Store = crash
				interrupted := false
				func() {
					defer func() {
						if recovered := recover(); recovered != nil {
							if recovered != errRepairCheckpointPause {
								panic(recovered)
							}
							interrupted = true
						}
					}()
					if err := f.s.processPaper(t.Context(), r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) }); err != nil {
						t.Fatal(err)
					}
				}()
				if !interrupted || !crash.tripped {
					t.Fatalf("missed %s checkpoint", point)
				}
				saved := directCheckpoint(t, f, r)
				var request string
				if saved.Paper.Repair != nil {
					request = saved.Paper.Repair.Request
				}
				f.s.Store = f.store
				must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
				f.s.process(t.Context(), f.claim(t, r.ID))
				end, messages := paperOutcome(t, f, c, r)
				stored := directCheckpoint(t, f, r)
				unknown := point == "candidate" && !after || point == "start" && after || point == "result" && !after
				if unknown {
					wantCalls := 4
					wantStage := "analyzing_results"
					if point != "candidate" {
						wantStage = "repairing_results"
					}
					if point == "result" {
						wantCalls = 5
					}
					if end.State != "unknown" || end.FailureCode != "result_unknown" || end.FailureStage != wantStage || end.FailureDetail != nil || len(g.calls) != wantCalls || len(messages) != 1 {
						t.Fatalf("uncertain call replayed: state=%s code=%s calls=%d", end.State, end.FailureCode, len(g.calls))
					}
					return
				}
				if end.State != "completed" || len(g.calls) != 7 || stored.Calls != 7 || len(messages) != 2 {
					t.Fatalf("durable repair not resumed: state=%s code=%s calls=%d", end.State, end.FailureCode, len(g.calls))
				}
				if request != "" && stored.Paper.Repair.Request != request {
					t.Fatal("repair request changed after restart")
				}
				for _, field := range []string{"problem", "method", "experiments"} {
					if !reflect.DeepEqual(saved.Paper.Outputs["analyzing_"+field], stored.Paper.Outputs["analyzing_"+field]) {
						t.Fatal("successful earlier field regenerated")
					}
				}
			})
		}
	}
}

func TestPaperCallBudgetCountsFailuresBeforeCallsAndExcludesAdmission(t *testing.T) {
	f, c, r, g := repairWorkflowFixture(t, TaskPaperFollowup, "answer")
	g.admissions = 1
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, _ := paperOutcome(t, f, c, r)
	if end.State != "completed" || len(g.calls) != 4 || directCheckpoint(t, f, r).Calls != 4 {
		t.Fatalf("admission consumed a call or repair failure was not counted: %+v", end)
	}
}

func TestPaperRepairRequestContainsOnlyCandidateEvidenceAndExactLimits(t *testing.T) {
	candidate := policyClaims(9, "中文候选结论")
	for i := range candidate.Claims {
		candidate.Claims[i].Evidence[0].ID = "used"
	}
	raw, err := json.Marshal(candidate)
	must(t, err)
	pc := &PaperCheckpoint{Mode: "fulltext", Context: PaperContext{Title: "本轮论文"}, SourceVersion: "v1", ContentHash: "current-hash", Sections: json.RawMessage(`[{"heading":"实验"}]`)}
	sources := []Citation{{ID: "unused", Quote: "must not reach repair"}, {ID: "used", Quote: "Original source <中文> 🙂", Page: 7, ContentHash: "current-hash"}}
	original, err := boundedPaperInput(paperInput(pc, "results", sources))
	must(t, err)
	request, err := buildPaperRepairRequest(Run{Task: TaskPaperReport, Question: "原始问题"}, pc, "analyzing_results", raw, original)
	must(t, err)
	if len(request) > paperInputLimit {
		t.Fatal("oversized serialized repair request")
	}
	var got struct {
		Candidate   fieldOutput      `json:"candidate"`
		Field       string           `json:"field"`
		Question    string           `json:"original_question"`
		Limits      paperFieldPolicy `json:"target_limits"`
		Evidence    []Citation       `json:"evidence"`
		ContextMode string           `json:"context_mode"`
		ContentHash string           `json:"content_hash"`
	}
	must(t, json.Unmarshal([]byte(request), &got))
	if !reflect.DeepEqual(got.Candidate, candidate) || got.Field != "results" || got.Question != "原始问题" || got.Limits != paperFieldPolicyFor("results") || got.ContextMode != "fulltext" || got.ContentHash != "current-hash" || len(got.Evidence) != 1 || got.Evidence[0].ID != "used" || got.Evidence[0].Quote != sources[1].Quote || strings.Contains(request, sources[0].Quote) {
		t.Fatalf("invalid repair context: %+v", got)
	}
}

func TestPaperRepairOversizedSerializedRequestPreservesOriginalFailure(t *testing.T) {
	f, c, r, g := repairWorkflowFixture(t, TaskPaperReport, "results")
	r = f.claim(t, r.ID)
	cp := directCheckpoint(t, f, r)
	_, _, err := f.s.preparePaper(t.Context(), r, c, &cp, func(context.Context) error { return nil })
	must(t, err)
	// Each input and the full response fit separately. JSON HTML escaping makes
	// their combined repair exceed 64 KiB; neither source nor claims may be cut.
	evidence := []Citation{{ID: "large", Quote: strings.Repeat("<", 7800)}}
	g.candidateText = strings.Repeat("<", 450) + "中文"
	input := paperInput(cp.Paper, "results", evidence)
	original, err := boundedPaperInput(input)
	must(t, err)
	if len(original) > paperInputLimit {
		t.Fatal("test original call exceeds budget")
	}
	_, err = f.s.paperCall(t.Context(), r, &cp, func(context.Context) error { return nil }, "analyzing_results", fieldPromptFor("results", true), input, func(raw []byte) error { _, e := decodeFieldFor(raw, evidence, "results", true); return e })
	if paperFailureCode(err) != "output_limit_exceeded" || len(g.calls) != 1 || cp.Calls != 1 || cp.Paper.Repair == nil || cp.Paper.Repair.Attempted || cp.Paper.Repair.State != "budget_exceeded" || cp.Paper.Failure == nil || cp.Paper.Failure.Count == nil || *cp.Paper.Failure.Count != 9 {
		t.Fatalf("oversized repair lost original failure or called model: err=%v cp=%+v calls=%d", err, cp, len(g.calls))
	}
	var candidate fieldOutput
	must(t, json.Unmarshal(cp.Paper.Repair.Candidate, &candidate))
	if len(candidate.Claims) != 9 || candidate.Claims[0].Text != g.candidateText {
		t.Fatal("candidate was cut to fit repair")
	}
	_, messages := paperOutcome(t, f, c, r)
	if len(messages) != 1 {
		t.Fatal("oversized candidate partially published")
	}
}

type paperRepairTerminalStore struct {
	Store
	tripped bool
}

func (s *paperRepairTerminalStore) Save(ctx context.Context, r Run, cp Checkpoint, progress string, step *Step) error {
	if err := s.Store.Save(ctx, r, cp, progress, step); err != nil {
		return err
	}
	if !s.tripped && cp.Phase == "failed" && cp.Paper != nil && cp.Paper.TerminalFailure != "" {
		s.tripped = true
		panic(errRepairCheckpointPause)
	}
	return nil
}

func TestPaperRepairTerminalFailureRecoveryDoesNotReplayPaidCall(t *testing.T) {
	for _, scenario := range []string{"repair fails", "candidate invalid", "credential changed"} {
		t.Run(scenario, func(t *testing.T) {
			f, c, r, g := repairWorkflowFixture(t, TaskPaperReport, "results")
			wantCode, wantCalls := "output_limit_exceeded", 5
			g.repairBad = true
			if scenario == "candidate invalid" {
				g.mixedReference = true
				wantCode, wantCalls = "evidence_id_unknown", 4
			}
			if scenario == "credential changed" {
				wantCode, wantCalls = "access_or_configuration_changed", 4
				g.afterValidation = func(stage string, _ generation.Result, err error) error {
					if stage == "analyzing_results" {
						g.version = 2
					}
					return err
				}
			}
			r = f.claim(t, r.ID)
			cp := directCheckpoint(t, f, r)
			crash := &paperRepairTerminalStore{Store: f.store}
			f.s.Store = crash
			func() {
				defer func() {
					if recovered := recover(); recovered != nil && recovered != errRepairCheckpointPause {
						panic(recovered)
					}
				}()
				_ = f.s.processPaper(t.Context(), r, c, &cp, func(ctx context.Context) error {
					if scenario == "credential changed" && g.version == 2 {
						return ErrConflict
					}
					return f.store.Check(ctx, r)
				})
			}()
			if !crash.tripped {
				t.Fatal("failed call was not durably recorded")
			}
			saved := directCheckpoint(t, f, r)
			if saved.Phase != "failed" || saved.Calls != wantCalls || saved.Paper.TerminalFailure != wantCode {
				t.Fatalf("wrong failed checkpoint: %+v", saved)
			}
			if scenario == "credential changed" {
				if saved.Paper.Repair != nil || saved.Paper.Failure != nil || saved.Paper.CurrentStage != "analyzing_results" {
					t.Fatal("permission failure retained a stale limit or pending repair")
				}
				steps, err := f.store.Steps(t.Context(), f.u.ID, r.ID)
				must(t, err)
				if len(steps) != 4 || steps[3].Tool != "analyzing_results" || steps[3].FailureCode != "output_limit_exceeded" {
					t.Fatalf("original paid failed call was lost: %+v", steps)
				}
				// Access can return after a restart; the known failed paid call must
				// remain terminal rather than being regenerated with restored access.
				g.version = 1
			}
			f.s.Store = f.store
			must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			if end.State != "failed" || end.FailureCode != wantCode || len(g.calls) != wantCalls || len(messages) != 1 {
				t.Fatalf("definite failed call was replayed: %+v calls=%d", end, len(g.calls))
			}
			if scenario == "credential changed" && (end.FailureStage != "analyzing_results" || end.FailureDetail != nil) {
				t.Fatal("restored access hid the actual failure stage or restored stale limit details")
			}
		})
	}
}

func TestPaperRepairChecksCancellationAndAccessBeforeStarting(t *testing.T) {
	for _, scenario := range []string{"cancelled", "access", "lease"} {
		t.Run(scenario, func(t *testing.T) {
			f, c, r, g := repairWorkflowFixture(t, TaskPaperReport, "results")
			r = f.claim(t, r.ID)
			cp := directCheckpoint(t, f, r)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			g.afterValidation = func(stage string, _ generation.Result, err error) error {
				if stage == "analyzing_results" && scenario == "cancelled" {
					cancel()
				}
				return err
			}
			check := func(context.Context) error {
				if cp.Paper != nil && cp.Paper.Repair != nil {
					if scenario == "access" {
						return ErrNotFound
					}
					if scenario == "lease" {
						return ErrLease
					}
				}
				return nil
			}
			err := f.s.processPaper(ctx, r, c, &cp, check)
			if err == nil || len(g.calls) != 4 || (cp.Paper.Repair != nil && cp.Paper.Repair.Attempted) {
				t.Fatalf("repair started without access/cancellation check: %v calls=%d", err, len(g.calls))
			}
			if scenario != "cancelled" && paperFailureCode(err) != "access_or_configuration_changed" {
				t.Fatalf("original limit replaced access error: %v", err)
			}
		})
	}
}

func TestPaperRepairFinalCallCapsIncludeOriginalFailureAndRepair(t *testing.T) {
	for _, task := range []string{TaskPaperReport, TaskPaperFollowup} {
		for _, room := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/room=%d", task, room), func(t *testing.T) {
				field, cap := "results", 30
				if task == TaskPaperFollowup {
					field, cap = "answer", 6
				}
				f, c, r, g := repairWorkflowFixture(t, task, field)
				r = f.claim(t, r.ID)
				cp := directCheckpoint(t, f, r)
				_, _, err := f.s.preparePaper(t.Context(), r, c, &cp, func(context.Context) error { return nil })
				must(t, err)
				cp.Calls, g.initialCalls = cap-room, cap-room
				must(t, f.store.Save(t.Context(), r, cp, "ready", nil))
				evidence := []Citation{{ID: "source", Quote: "paper source"}}
				questions := []PaperQuestion{{ID: "q1", Question: "论文使用什么方法？", Query: "retrieval method experiment"}}
				if task == TaskPaperFollowup {
					cp.Paper.QA = &PaperQACheckpoint{RetrieverVersion: paperRetrieverVersion, Questions: questions}
				}
				validate := func(raw []byte) error {
					if task == TaskPaperFollowup {
						_, e := decodePaperAnswer(raw, evidence, questions)
						return e
					}
					_, e := decodeFieldFor(raw, evidence, field, task == TaskPaperReport)
					return e
				}
				input := paperInput(cp.Paper, field, evidence)
				if task == TaskPaperFollowup {
					input["questions"] = questions
				}
				_, err = f.s.paperCall(t.Context(), r, &cp, func(context.Context) error { return nil }, "analyzing_"+field, fieldPromptFor(field, task == TaskPaperReport), input, validate)
				if room == 2 {
					must(t, err)
					if cp.Paper.Repair == nil || !cp.Paper.Repair.Attempted || cp.Paper.Repair.State != "completed" {
						t.Fatal("last allowed repair did not finish")
					}
				} else if paperFailureCode(err) != "budget_exhausted" {
					t.Fatalf("wrong final budget failure: %v", err)
				}
				if len(g.calls) != room || cp.Calls != cap {
					t.Fatalf("wrong cap accounting: external=%d checkpoint=%d cap=%d", len(g.calls), cp.Calls, cap)
				}
				// Even after a final-slot repair succeeds, review is mandatory and
				// cannot begin without another occupied call slot.
				_, err = f.s.paperCall(t.Context(), r, &cp, func(context.Context) error { return nil }, "validating_paper", verdictPrompt, map[string]any{"claims": []reviewClaim{}}, func([]byte) error { return nil })
				if paperFailureCode(err) != "budget_exhausted" || len(g.calls) != room || cp.Calls != cap {
					t.Fatal("mandatory review exceeded final call cap")
				}
				_, messages := paperOutcome(t, f, c, r)
				if len(messages) != 1 {
					t.Fatal("unreviewed final-slot repair published")
				}
			})
		}
	}
}

func TestPaperRepairCompleteResponseLimitNeverStartsRepair(t *testing.T) {
	f, c, r, g := repairWorkflowFixture(t, TaskPaperReport, "results")
	g.candidateText = strings.Repeat("中", 2000)
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, messages := paperOutcome(t, f, c, r)
	cp := directCheckpoint(t, f, r)
	if end.FailureCode != "output_limit_exceeded" || cp.Paper.Repair != nil || len(g.calls) != 4 || cp.Calls != 4 || len(messages) != 1 || end.FailureDetail == nil || end.FailureDetail.Path != "$" || end.FailureDetail.Limit == nil || *end.FailureDetail.Limit != paperResponseLimit {
		t.Fatalf("oversized complete response triggered repair: %+v", end)
	}
}
