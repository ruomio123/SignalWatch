package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"signalwatch/internal/generation"
	"sort"
	"strings"
	"testing"
	"time"
)

type recoveryV14Store struct {
	Store
	t           *testing.T
	snapshots   []Checkpoint
	steps       []Step
	beforeSave  func(Checkpoint, *Step)
	afterSave   func(Checkpoint, *Step)
	finishState string
	finishCode  string
}

func (s *recoveryV14Store) Save(_ context.Context, _ Run, cp Checkpoint, _ string, step *Step) error {
	raw, err := json.Marshal(cp)
	if err != nil {
		s.t.Fatalf("checkpoint is not persistable JSON: %v", err)
	}
	var snapshot Checkpoint
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		s.t.Fatal(err)
	}
	if s.beforeSave != nil {
		s.beforeSave(snapshot, step)
	}
	s.snapshots = append(s.snapshots, snapshot)
	if step != nil {
		s.steps = append(s.steps, *step)
	}
	if s.afterSave != nil {
		s.afterSave(snapshot, step)
	}
	return nil
}

func (s *recoveryV14Store) Finish(_ context.Context, _ Run, state, code string, _ *Message) error {
	s.finishState, s.finishCode = state, code
	return nil
}

func (s *recoveryV14Store) Renew(context.Context, Run) error { return nil }

type recoveryV14Reply struct {
	content             string
	failure             string
	validateBeforeError bool
}

type recoveryV14Gateway struct {
	Gateway
	t       *testing.T
	store   *recoveryV14Store
	replies []recoveryV14Reply
	calls   []ModelRequest
	before  func(context.Context, ModelRequest) context.Context
}

func (g *recoveryV14Gateway) Generate(ctx context.Context, request ModelRequest) (generation.Result, error) {
	if g.before != nil {
		ctx = g.before(ctx, request)
	}
	if err := request.Before(ctx); err != nil {
		return generation.Result{}, err
	}
	if len(g.replies) <= len(g.calls) {
		g.t.Fatal("unexpected additional model call")
	}
	reply := g.replies[len(g.calls)]
	g.calls = append(g.calls, request)
	if len(g.store.snapshots) == 0 {
		g.t.Fatal("model called before a durable reservation")
	}
	saved := g.store.snapshots[len(g.store.snapshots)-1]
	if saved.Phase != "calling" || saved.Calls != len(g.calls) {
		g.t.Fatalf("model call missing reservation: phase=%s calls=%d actual=%d", saved.Phase, saved.Calls, len(g.calls))
	}
	if strings.HasPrefix(saved.Paper.CurrentStage, "repairing_") {
		repair := saved.Paper.Repair
		if repair == nil || !repair.Attempted || repair.State != "calling" || repair.Request != string(request.Input) || repair.OriginalRequest == "" {
			g.t.Fatal("recovery was not durably reserved with frozen inputs")
		}
	}
	result := generation.Result{CallID: fmt.Sprintf("recovery-v14-%d", len(g.calls)), Content: []byte(reply.content), UsageKnown: true, InputTokens: 11, OutputTokens: 12}
	var validation error
	if reply.failure == "" || reply.validateBeforeError {
		validation = request.Validate(result)
	}
	if reply.failure != "" {
		return result, &ModelError{Code: reply.failure}
	}
	return result, validation
}

func recoveryV14Fixture(t *testing.T, task string, replies ...recoveryV14Reply) (*Service, Run, *Checkpoint, *recoveryV14Gateway, *recoveryV14Store) {
	t.Helper()
	store := &recoveryV14Store{t: t}
	gateway := &recoveryV14Gateway{t: t, store: store, replies: replies}
	cp := &Checkpoint{Phase: "ready", Paper: &PaperCheckpoint{
		Limits: generation.DefaultModelLimits(), CallTimeout: 10 * time.Second,
		Context: PaperContext{Title: "Recovery fixture"}, Mode: "fulltext",
		Outputs: map[string]json.RawMessage{}, QA: &PaperQACheckpoint{Supplement: &PaperSupplementCheckpoint{State: "ready"}},
	}}
	return &Service{Dependencies: Dependencies{Store: store, Gateway: gateway}}, Run{ID: "recovery-v14", Task: task, Question: "请说明论文内容"}, cp, gateway, store
}

func recoveryV14Inputs(t *testing.T, stage string) (string, map[string]any, paperStageValidation, string) {
	t.Helper()
	contract := paperContract(stage)
	evidence := []Citation{{ID: "source", Quote: "The paper describes its method."}}
	questions := []PaperQuestion{{ID: "q1", Question: "方法是什么？", Query: "method"}}
	claims := []reviewClaim{{ID: "problem-1", Text: "论文介绍了方法。", Evidence: []evidenceOutput{{ID: "source"}}}}
	task := TaskPaperReport
	var output any
	switch contract.Kind {
	case "report":
		output = fieldOutput{Status: "insufficient_evidence", Claims: []claimOutput{}}
	case "extraction":
		output = map[string]any{"problem": []any{}, "method": []any{}, "experiments": []any{}, "results": []any{}, "limitations": []any{}}
	case "review":
		output = map[string]any{"verdicts": []any{map[string]any{"id": claims[0].ID, "supported": true}}}
	case "question":
		if stage == "planning_reproduction" {
			task = TaskPaperReproduction
			categories := []map[string]string{}
			for _, category := range paperReproductionCategories() {
				categories = append(categories, map[string]string{"category_id": category.ID, "query": "method"})
			}
			output = map[string]any{"categories": categories}
		} else {
			task = TaskPaperFollowup
			output = paperQuestionsOutput{Questions: []paperQuestionItemOutput{{Question: "方法是什么？", Query: "method"}}}
		}
	case "answer":
		task = TaskPaperFollowup
		if contract.Field == "reproduction" {
			task = TaskPaperReproduction
			questions = []PaperQuestion{}
			for i, category := range paperReproductionCategories() {
				questions = append(questions, PaperQuestion{ID: fmt.Sprintf("q%d", i+1), Question: category.Question, Query: "method"})
			}
		}
		answer := paperAnswerOutput{Answers: []paperAnswerPartOutput{}, SupplementalQueries: []PaperSupplementQuery{}}
		for _, question := range questions {
			answer.Answers = append(answer.Answers, paperAnswerPartOutput{QuestionID: question.ID, Status: "insufficient_evidence", Claims: []claimOutput{}})
		}
		output = answer
	default:
		t.Fatalf("unsupported fixture contract: %s", stage)
	}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"evidence": evidence, "questions": questions, "claims": claims, "source_passages": evidence}
	validation := paperStageValidation{Evidence: evidence, Questions: questions, Claims: claims}
	if err := contract.Validate(raw, validation); err != nil {
		t.Fatalf("invalid fixture for %s: %v", stage, err)
	}
	return task, input, validation, string(raw)
}

func recoveryV14Check(context.Context) error { return nil }

func TestPaperRecoveryV14SingleCompleteFenceEnvelope(t *testing.T) {
	valid := `{"status":"insufficient_evidence","claims":[]}`
	for _, envelope := range []string{"```json\n" + valid + "\n```", " \n```\r\n" + valid + "\r\n``` \n"} {
		service, run, cp, gateway, store := recoveryV14Fixture(t, TaskPaperReport, recoveryV14Reply{content: envelope})
		validate := func(raw []byte) error {
			return paperContract("analyzing_problem").Validate(raw, paperStageValidation{})
		}
		raw, err := service.paperCall(t.Context(), run, cp, recoveryV14Check, "analyzing_problem", "Analyze", map[string]any{}, validate)
		if err != nil || string(raw) != valid || len(gateway.calls) != 1 || paperRecoveryUsed(cp.Paper) != 0 || string(cp.Paper.Outputs["analyzing_problem"]) != valid {
			t.Fatalf("fence normalization called recovery or failed: raw=%s err=%v calls=%d", raw, err, len(gateway.calls))
		}
		if got := string(store.snapshots[len(store.snapshots)-1].Paper.Outputs["analyzing_problem"]); got != valid {
			t.Fatalf("noncanonical output persisted: %s", got)
		}
	}
	for _, invalid := range []string{
		"Here is JSON:\n```json\n" + valid + "\n```", "```json\n" + valid + "\n```\nprose",
		"```json\n{} {}\n```", "```json\n{\"status\":\n```", "```json\n[]\n```",
		"```json\n{}\n```\n```json\n{}\n```", "```javascript\n{}\n```",
	} {
		if got := string(normalizePaperJSON([]byte(invalid))); got != invalid {
			t.Fatalf("unsafe fence extraction: %q became %q", invalid, got)
		}
	}
}

func TestPaperRecoveryV14FormatRecoveryAcrossEveryOriginalStage(t *testing.T) {
	stages := []string{}
	for stage, contract := range paperStageContracts {
		if contract.OriginalStage == "" {
			stages = append(stages, stage)
		}
	}
	sort.Strings(stages)
	for _, stage := range stages {
		for _, invalidKind := range []string{"syntax", "missing", "type"} {
			t.Run(stage+"/"+invalidKind, func(t *testing.T) {
				task, input, validation, valid := recoveryV14Inputs(t, stage)
				invalid := `{"unterminated":`
				if invalidKind == "missing" {
					invalid = `{}`
				}
				if invalidKind == "type" {
					var value map[string]any
					_ = json.Unmarshal([]byte(valid), &value)
					value[paperContract(stage).Schema.Required[0]] = 7
					raw, _ := json.Marshal(value)
					invalid = string(raw)
				}
				service, run, cp, gateway, store := recoveryV14Fixture(t, task, recoveryV14Reply{content: invalid}, recoveryV14Reply{content: "```json\n" + valid + "\n```"})
				cp.Paper.QA.Questions = validation.Questions
				validate := func(raw []byte) error { return paperContract(stage).Validate(raw, validation) }
				raw, err := service.paperCall(t.Context(), run, cp, recoveryV14Check, stage, "Analyze", input, validate)
				if err != nil || string(raw) != valid || len(gateway.calls) != 2 || cp.Calls != 2 || paperRecoveryUsed(cp.Paper) != 1 {
					t.Fatalf("recovery failed: raw=%s err=%v calls=%d", raw, err, len(gateway.calls))
				}
				repair := cp.Paper.Repairs[stage]
				if repair == nil || repair.Kind != "format" || repair.State != "completed" || repair.RawCandidate != invalid || len(repair.Candidate) != 0 || !repair.Attempted || repair.Failure == nil {
					t.Fatalf("candidate/error snapshot was not retained: %+v", repair)
				}
				if string(cp.Paper.Outputs[stage]) != valid || string(cp.Paper.Outputs[paperContract(stage).RepairStage]) != valid {
					t.Fatal("canonical recovery output not promoted to the original stage")
				}
				persisted := store.snapshots[len(store.snapshots)-1].Paper.Repairs[stage]
				if persisted.RawCandidate != invalid || persisted.OriginalRequest != string(gateway.calls[0].Input) {
					t.Fatal("raw candidate or original request changed during persistence")
				}
				if len(store.steps) != 2 || store.steps[0].FailureCode == "" || store.steps[1].FailureCode != "" {
					t.Fatal("original failed call or successful recovery audit step lost")
				}
			})
		}
	}
}

func TestPaperRecoveryV14TruncationAndUnknownCallPrecedence(t *testing.T) {
	stage := "analyzing_problem"
	task, input, validation, valid := recoveryV14Inputs(t, stage)
	for _, code := range []string{"output_truncated", "timeout", "transport_failed", "result_unknown", "storage_failed", "provider_rejected"} {
		for _, validateBeforeError := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/validated=%t", code, validateBeforeError), func(t *testing.T) {
				service, run, cp, gateway, _ := recoveryV14Fixture(t, task,
					recoveryV14Reply{content: `{"status":`, failure: code, validateBeforeError: validateBeforeError}, recoveryV14Reply{content: valid})
				validate := func(raw []byte) error { return paperContract(stage).Validate(raw, validation) }
				_, err := service.paperCall(t.Context(), run, cp, recoveryV14Check, stage, "Analyze", input, validate)
				if code == "output_truncated" {
					if err != nil || len(gateway.calls) != 2 || cp.Paper.Repairs[stage].Kind != "truncated" {
						t.Fatalf("known truncated result not regenerated: err=%v calls=%d", err, len(gateway.calls))
					}
					var request struct {
						Target paperFieldPolicy `json:"target_limits"`
					}
					if json.Unmarshal(gateway.calls[1].Input, &request) != nil || request.Target.MaxClaims != 3 || request.Target.MaxTextBytes != 600 {
						t.Fatal("truncation recovery did not request compact output")
					}
				} else if err == nil || paperFailureCode(err) != code || len(gateway.calls) != 1 || len(cp.Paper.Repairs) != 0 || cp.Phase != "failed" {
					t.Fatalf("actual provider/settlement failure was overridden or replayed: err=%v calls=%d phase=%s", err, len(gateway.calls), cp.Phase)
				}
			})
		}
	}
}

func TestPaperRecoveryV14TaskCapAndStageReplay(t *testing.T) {
	valid := `{"status":"insufficient_evidence","claims":[]}`
	var replies []recoveryV14Reply
	for i := 0; i < 3; i++ {
		replies = append(replies, recoveryV14Reply{content: `{}`}, recoveryV14Reply{content: valid})
	}
	replies = append(replies, recoveryV14Reply{content: `{}`})
	service, run, cp, gateway, store := recoveryV14Fixture(t, TaskPaperReport, replies...)
	for i, field := range paperFields[:4] {
		stage := "analyzing_" + field
		validate := func(raw []byte) error { return paperContract(stage).Validate(raw, paperStageValidation{}) }
		_, err := service.paperCall(t.Context(), run, cp, recoveryV14Check, stage, "Analyze", map[string]any{}, validate)
		if (i < 3 && err != nil) || (i == 3 && !paperStageIncomplete(err)) {
			t.Fatalf("unexpected task cap result at %s: %v", stage, err)
		}
	}
	if paperRecoveryUsed(cp.Paper) != 3 || len(gateway.calls) != 7 || len(cp.Paper.Issues) != 1 {
		t.Fatalf("task recovery cap not enforced: repairs=%d calls=%d issues=%+v", paperRecoveryUsed(cp.Paper), len(gateway.calls), cp.Paper.Issues)
	}
	restored := store.snapshots[len(store.snapshots)-1]
	before := len(gateway.calls)
	_, err := service.paperCall(t.Context(), run, &restored, recoveryV14Check, "analyzing_results", "Analyze", map[string]any{}, func([]byte) error { t.Fatal("failed stage must not revalidate a nonexistent output"); return nil })
	if !paperStageIncomplete(err) || len(gateway.calls) != before {
		t.Fatal("durable failed stage replayed the model call")
	}
}

func TestPaperRecoveryV14OneAttemptPerStageAndInvalidEvidenceNotRepairable(t *testing.T) {
	for _, invalidEvidence := range []bool{false, true} {
		t.Run(fmt.Sprint(invalidEvidence), func(t *testing.T) {
			stage := "analyzing_problem"
			invalid := `{}`
			if invalidEvidence {
				invalid = `{"status":"supported","claims":[{"text":"该结论来自不存在的证据。","evidence":[{"id":"forged"}]}]}`
			}
			service, run, cp, gateway, store := recoveryV14Fixture(t, TaskPaperReport, recoveryV14Reply{content: invalid}, recoveryV14Reply{content: invalid})
			validate := func(raw []byte) error { return paperContract(stage).Validate(raw, paperStageValidation{}) }
			_, err := service.paperCall(t.Context(), run, cp, recoveryV14Check, stage, "Analyze", map[string]any{}, validate)
			wantCalls := 2
			if invalidEvidence {
				wantCalls = 1
			}
			if !paperStageIncomplete(err) || len(gateway.calls) != wantCalls || cp.Paper.Outputs[stage] != nil || cp.Paper.StageFailures[stage] == nil {
				t.Fatalf("unsafe recovery outcome: err=%v calls=%d failures=%v", err, len(gateway.calls), cp.Paper.StageFailures)
			}
			restored := store.snapshots[len(store.snapshots)-1]
			_, replayErr := service.paperCall(t.Context(), run, &restored, recoveryV14Check, stage, "Analyze", map[string]any{}, validate)
			if !paperStageIncomplete(replayErr) || len(gateway.calls) != wantCalls || !reflect.DeepEqual(restored.Paper.StageFailures, cp.Paper.StageFailures) {
				t.Fatal("failed recovery retried or changed durable failure")
			}
		})
	}
}

type recoveryV14Crash struct{}

func recoveryV14Interrupt(t *testing.T, action func()) {
	t.Helper()
	defer func() {
		if _, ok := recover().(recoveryV14Crash); !ok {
			t.Fatal("expected the simulated process interruption")
		}
	}()
	action()
}

func TestPaperRecoveryV14CrashWindowsPreserveExactlyOnceCalls(t *testing.T) {
	for _, window := range []string{"before_pending", "after_pending", "before_calling", "after_calling", "before_result", "after_result"} {
		t.Run(window, func(t *testing.T) {
			stage := "analyzing_problem"
			task, input, validation, valid := recoveryV14Inputs(t, stage)
			service, run, cp, gateway, store := recoveryV14Fixture(t, task, recoveryV14Reply{content: `{"broken":`}, recoveryV14Reply{content: valid})
			validate := func(raw []byte) error { return paperContract(stage).Validate(raw, validation) }
			checkpointEvent := strings.TrimPrefix(strings.TrimPrefix(window, "before_"), "after_")
			hook := func(saved Checkpoint, _ *Step) {
				if saved.Paper.Repair == nil {
					return
				}
				repair := saved.Paper.Repair
				matches := (checkpointEvent == "pending" && saved.Phase == "ready" && repair.State == "pending") ||
					(checkpointEvent == "calling" && saved.Phase == "calling" && repair.State == "calling") ||
					(checkpointEvent == "result" && saved.Phase == "ready" && repair.State == "completed")
				if matches {
					panic(recoveryV14Crash{})
				}
			}
			if strings.HasPrefix(window, "before_") {
				store.beforeSave = hook
			} else {
				store.afterSave = hook
			}
			recoveryV14Interrupt(t, func() {
				_, _ = service.paperCall(t.Context(), run, cp, recoveryV14Check, stage, "Frozen original prompt", input, validate)
			})
			store.beforeSave, store.afterSave = nil, nil
			restored := store.snapshots[len(store.snapshots)-1]
			paidCalls := len(gateway.calls)
			if restored.Phase == "calling" {
				// This is the real process entry guard, which must finish unknown
				// before consulting documents, configuration or another model call.
				run.Checkpoint, _ = json.Marshal(restored)
				service.process(t.Context(), run)
				if store.finishState != "unknown" || store.finishCode != "result_unknown" || len(gateway.calls) != paidCalls {
					t.Fatalf("uncertain call replayed: state=%s code=%s calls=%d want=%d", store.finishState, store.finishCode, len(gateway.calls), paidCalls)
				}
				return
			}
			var frozenPrompt, frozenRequest string
			if restored.Paper.Repair != nil {
				frozenPrompt, frozenRequest = restored.Paper.Repair.Prompt, restored.Paper.Repair.Request
			}
			raw, err := service.paperCall(t.Context(), run, &restored, recoveryV14Check, stage, "Changed prompt must not replace saved recovery", map[string]any{"changed": true}, validate)
			if err != nil || string(raw) != valid || len(gateway.calls) != 2 {
				t.Fatalf("restart lost progress or replayed original call: err=%v calls=%d raw=%s", err, len(gateway.calls), raw)
			}
			if paidCalls == 1 && (string(gateway.calls[1].Input) != frozenRequest || gateway.calls[1].System != paperPolicy+"\n"+frozenPrompt) {
				t.Fatal("pending recovery did not reuse frozen request and prompt")
			}
		})
	}
}

func TestPaperRecoveryV14RechecksReviewBudgetOnPendingResumeAndAdmission(t *testing.T) {
	for _, timing := range []string{"pending_resume", "after_admission"} {
		t.Run(timing, func(t *testing.T) {
			stage := "analyzing_problem"
			task, input, validation, valid := recoveryV14Inputs(t, stage)
			service, run, cp, gateway, store := recoveryV14Fixture(t, task, recoveryV14Reply{content: `{}`}, recoveryV14Reply{content: valid})
			validate := func(raw []byte) error { return paperContract(stage).Validate(raw, validation) }
			ctx := t.Context()
			if timing == "pending_resume" {
				store.afterSave = func(saved Checkpoint, _ *Step) {
					if saved.Phase == "ready" && saved.Paper.Repair != nil && saved.Paper.Repair.State == "pending" {
						panic(recoveryV14Crash{})
					}
				}
				recoveryV14Interrupt(t, func() { _, _ = service.paperCall(ctx, run, cp, recoveryV14Check, stage, "Analyze", input, validate) })
				store.afterSave = nil
				restored := store.snapshots[len(store.snapshots)-1]
				cp = &restored
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
			} else {
				gateway.before = func(ctx context.Context, _ ModelRequest) context.Context {
					if len(gateway.calls) == 1 {
						short, cancel := context.WithTimeout(ctx, 5*time.Second)
						t.Cleanup(cancel)
						return short
					}
					return ctx
				}
			}
			_, err := service.paperCall(ctx, run, cp, recoveryV14Check, stage, "Analyze", input, validate)
			repair := cp.Paper.Repairs[stage]
			if !paperStageIncomplete(err) || len(gateway.calls) != 1 || cp.Calls != 1 || paperRecoveryUsed(cp.Paper) != 0 || repair == nil || repair.Attempted || repair.State != "budget_exceeded" || cp.Paper.StageFailures[stage] == nil {
				t.Fatalf("optional recovery consumed review budget: err=%v calls=%d occupied=%d repair=%+v", err, len(gateway.calls), cp.Calls, repair)
			}
		})
	}
}

func TestPaperRecoveryV14ReplacementUndergoesEvidenceAndReviewCoverageValidation(t *testing.T) {
	for _, stage := range []string{"analyzing_problem", "validating_paper"} {
		t.Run(stage, func(t *testing.T) {
			task, input, validation, _ := recoveryV14Inputs(t, stage)
			invalid := `{"status":"supported","claims":[{"text":"凭空生成的结论。","evidence":[{"id":"forged"}]}]}`
			if stage == "validating_paper" {
				invalid = `{"verdicts":[{"id":"forged-claim","supported":true}]}`
			}
			service, run, cp, gateway, _ := recoveryV14Fixture(t, task, recoveryV14Reply{content: `{}`}, recoveryV14Reply{content: invalid})
			_, err := service.paperCall(t.Context(), run, cp, recoveryV14Check, stage, "Analyze", input, func(raw []byte) error { return paperContract(stage).Validate(raw, validation) })
			if !paperStageIncomplete(err) || len(gateway.calls) != 2 || cp.Paper.Outputs[stage] != nil || cp.Paper.Repairs[stage].State != "failed" {
				t.Fatalf("replacement bypassed full validation: err=%v calls=%d repair=%+v", err, len(gateway.calls), cp.Paper.Repairs[stage])
			}
		})
	}
}
