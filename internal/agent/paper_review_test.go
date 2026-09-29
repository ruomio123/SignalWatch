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

func reviewFixture(count, bytes int) ([]reviewClaim, map[string]Citation) {
	claims := []reviewClaim{}
	sources := map[string]Citation{}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("source-%d", i)
		sources[id] = Citation{ID: id, Quote: strings.Repeat("<", bytes)}
		claims = append(claims, reviewClaim{ID: fmt.Sprintf("results-%d", i+1), Text: "实验结果。", Evidence: []evidenceOutput{{ID: id}, {ID: "shared"}}})
	}
	sources["shared"] = Citation{ID: "shared", Quote: "共同条件。"}
	return claims, sources
}

func TestPaperReviewSerializedBudgetAndDedup(t *testing.T) {
	claims, sources := reviewFixture(8, 2000)
	pc := &PaperCheckpoint{Context: PaperContext{Title: "论文🙂<&"}, Mode: "fulltext"}
	plan, err := planPaperReview(pc, TaskPaperReport, claims, sources)
	must(t, err)
	if len(plan) != 2 {
		t.Fatalf("want 2 batches, got %d", len(plan))
	}
	must(t, validatePaperReviewPlan(plan, TaskPaperReport, claims))
	got := []reviewClaim{}
	for _, batch := range plan {
		if len(batch.Request) > 64<<10 {
			t.Fatal("serialized input too large")
		}
		var request struct {
			Claims  []reviewClaim     `json:"claims"`
			Sources []evidencePassage `json:"source_passages"`
		}
		must(t, json.Unmarshal([]byte(batch.Request), &request))
		seen := map[string]bool{}
		for _, source := range request.Sources {
			if seen[source.ID] || source.Quote != sources[source.ID].Quote {
				t.Fatal("duplicate or altered evidence")
			}
			seen[source.ID] = true
		}
		if len(request.Sources) != len(request.Claims)+1 {
			t.Fatal("did not deduplicate shared evidence")
		}
		got = append(got, request.Claims...)
	}
	if !reflect.DeepEqual(got, claims) {
		t.Fatal("claims lost, reordered, or changed")
	}
	plan[1].ClaimIDs[0] = "wrong"
	if validatePaperReviewPlan(plan, TaskPaperReport, claims) == nil {
		t.Fatal("corrupt plan accepted")
	}
}

func TestPaperReviewChecksAllBatchesBeforeCalling(t *testing.T) {
	for _, tc := range []struct {
		task         string
		count, bytes int
		want         int
	}{
		{TaskPaperReport, 6, 6000, 6}, {TaskPaperReport, 7, 6000, 0},
		{TaskPaperFollowup, 2, 6000, 2}, {TaskPaperFollowup, 3, 6000, 0},
		{TaskPaperReport, 1, 12000, 0},
	} {
		t.Run(fmt.Sprintf("%s-%d-%d", tc.task, tc.count, tc.bytes), func(t *testing.T) {
			claims, sources := reviewFixture(tc.count, tc.bytes)
			plan, err := planPaperReview(&PaperCheckpoint{}, tc.task, claims, sources)
			if tc.want == 0 {
				if err == nil || plan != nil {
					t.Fatal("invalid full plan accepted")
				}
				return
			}
			must(t, err)
			if len(plan) != tc.want {
				t.Fatalf("got %d", len(plan))
			}
		})
	}
}

type reviewCrashStore struct {
	Store
	stage   string
	after   bool
	tripped bool
}

func (s *reviewCrashStore) Save(ctx context.Context, r Run, cp Checkpoint, progress string, step *Step) error {
	if !s.tripped && step != nil && step.Tool == s.stage {
		s.tripped = true
		if s.after {
			if err := s.Store.Save(ctx, r, cp, progress, step); err != nil {
				return err
			}
		}
		return errors.New("simulated checkpoint write interruption")
	}
	return s.Store.Save(ctx, r, cp, progress, step)
}

func TestPaperReviewRecoveryReusesRequestsAndCompletedBatches(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, nil)
			gateway := &workflowGateway{limits: generation.ModelLimits{ContextTokens: 262144, MaxOutputTokens: 8192}}
			f.s.Gateway = gateway
			r := f.claim(t, submitReport(t, f, c, "abstract").ID)
			cp := Checkpoint{Phase: "ready"}
			_, _, err := f.s.preparePaper(t.Context(), r, c, &cp, func(context.Context) error { return nil })
			must(t, err)
			claims, sources := reviewFixture(8, 2000)
			f.s.Store = &reviewCrashStore{Store: f.store, stage: "validating_paper", after: after}
			_, err = f.s.reviewPaper(t.Context(), r, &cp, func(context.Context) error { return nil }, claims, sources)
			if err == nil || len(gateway.calls) != 1 {
				t.Fatal("did not interrupt expected batch")
			}
			stored, err := f.store.RunByID(t.Context(), f.u.ID, r.ID)
			must(t, err)
			must(t, json.Unmarshal(stored.Checkpoint, &cp))
			plan := budgetJSON(t, cp.Paper.ReviewPlan)
			must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
			recovered := f.claim(t, r.ID)
			f.s.Store = f.store
			if !after {
				f.s.process(t.Context(), recovered)
				end, messages := paperOutcome(t, f, c, r)
				if end.FailureCode != "result_unknown" || len(gateway.calls) != 1 || len(messages) != 1 {
					t.Fatal("uncertain batch was replayed")
				}
				return
			}
			verdicts, err := f.s.reviewPaper(t.Context(), recovered, &cp, func(context.Context) error { return nil }, claims, nil)
			must(t, err)
			if len(gateway.calls) != 2 || len(verdicts) != 8 || string(budgetJSON(t, cp.Paper.ReviewPlan)) != string(plan) {
				t.Fatal("review replayed or changed plan")
			}
			if string(gateway.calls[1].Input) != cp.Paper.ReviewPlan[1].Request {
				t.Fatal("saved request not reused exactly")
			}
			end, messages := paperOutcome(t, f, c, r)
			if end.ReviewProgress == nil || end.ReviewProgress.Completed != 2 || end.ReviewProgress.Total != 2 || len(messages) != 1 {
				t.Fatal("unsafe publication or wrong progress")
			}
			public := budgetJSON(t, end)
			if strings.Contains(string(public), "source_passages") || strings.Contains(string(public), "claim_ids") {
				t.Fatal("internal review leaked")
			}
		})
	}
}

type fiveResultsGateway struct{ workflowGateway }

func (g *fiveResultsGateway) Generate(ctx context.Context, req ModelRequest) (generation.Result, error) {
	var input struct {
		Field    string           `json:"field"`
		Evidence []evidenceOutput `json:"evidence"`
	}
	if err := json.Unmarshal(req.Input, &input); err != nil {
		return generation.Result{}, err
	}
	if input.Field != "results" {
		return g.workflowGateway.Generate(ctx, req)
	}
	if err := req.Before(ctx); err != nil {
		return generation.Result{}, err
	}
	g.calls = append(g.calls, req)
	claims := []claimOutput{}
	for i := 0; i < 5; i++ {
		claims = append(claims, claimOutput{Text: fmt.Sprintf("实验结果第%d条。", i+1), Evidence: []evidenceOutput{{ID: input.Evidence[0].ID}}})
	}
	raw, _ := json.Marshal(fieldOutput{Status: "supported", Claims: claims})
	result := generation.Result{Content: raw, CallID: "five-results"}
	return result, req.Validate(result)
}
func TestPaperFiveResultsCompleteWithStageTokenBudgets(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	g := &fiveResultsGateway{}
	f.s.Gateway = g
	r := submitReport(t, f, c, "abstract")
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, messages := paperOutcome(t, f, c, r)
	if end.State != "completed" || len(g.calls) != 6 || len(messages) != 2 {
		t.Fatalf("state=%s calls=%d", end.State, len(g.calls))
	}
	for i, req := range g.calls {
		want := 8192
		if i == 5 {
			want = 4096
		}
		if req.MaxTokens != want {
			t.Fatalf("call %d tokens=%d", i, req.MaxTokens)
		}
	}
	if !strings.Contains(messages[1].Content, "实验结果第5条") {
		t.Fatal("fifth result lost")
	}
}

func TestPaperReviewIncompleteLaterBatchRetainsPriorVerdicts(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	f.s.Gateway = &workflowGateway{limits: generation.ModelLimits{ContextTokens: 262144, MaxOutputTokens: 8192}}
	r := f.claim(t, submitReport(t, f, c, "abstract").ID)
	cp := Checkpoint{Phase: "ready"}
	_, _, err := f.s.preparePaper(t.Context(), r, c, &cp, func(context.Context) error { return nil })
	must(t, err)
	claims, sources := reviewFixture(8, 2000)
	calls := 0
	g := &workflowGateway{hook: func(req ModelRequest) error {
		calls++
		if calls == 2 {
			return req.Validate(generation.Result{Content: []byte(`{"verdicts":[]}`)})
		}
		return nil
	}}
	f.s.Gateway = g
	verdicts, err := f.s.reviewPaper(t.Context(), r, &cp, func(context.Context) error { return nil }, claims, sources)
	must(t, err)
	_, messages := paperOutcome(t, f, c, r)
	if len(messages) != 1 || len(cp.Paper.Outputs) != 1 || len(verdicts) != len(cp.Paper.ReviewPlan[0].ClaimIDs) || cp.Paper.StageFailures["validating_paper_2"] == nil {
		t.Fatal("review published prematurely, lost successful verdicts, or accepted incomplete batch")
	}
	for _, id := range cp.Paper.ReviewPlan[1].ClaimIDs {
		if _, reviewed := verdicts[id]; reviewed {
			t.Fatal("incomplete batch was treated as successfully reviewed")
		}
	}
}
