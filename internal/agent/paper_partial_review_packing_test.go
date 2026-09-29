package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"signalwatch/internal/generation"
	"strings"
	"testing"
)

func partialReviewPackingFixture(scenario string) ([]reviewClaim, map[string]Citation) {
	if scenario == "batch_limit" {
		return reviewFixture(7, 6000)
	}
	count := 2
	if scenario == "all_oversized" {
		count = 1
	}
	claims, sources := reviewFixture(count, 100)
	large := sources["source-0"]
	large.Quote = strings.Repeat("x", paperInputLimit)
	sources["source-0"] = large
	return claims, sources
}

func TestPaperPartialReviewPackingRetainsWholeReviewableClaims(t *testing.T) {
	for _, scenario := range []string{"oversized_first", "batch_limit", "all_oversized"} {
		t.Run(scenario, func(t *testing.T) {
			claims, sources := partialReviewPackingFixture(scenario)
			pc := &PaperCheckpoint{}
			plan, omitted, code, err := planPaperReviewAvailable(pc, TaskPaperReport, claims, sources)
			must(t, err)
			wantCode, wantOmitted, wantBatches := "context_too_large", []string{claims[0].ID}, 1
			if scenario == "batch_limit" {
				wantCode, wantOmitted, wantBatches = "budget_exhausted", []string{claims[6].ID}, 6
			} else if scenario == "all_oversized" {
				wantBatches = 0
			}
			if code != wantCode || !reflect.DeepEqual(omitted, wantOmitted) || len(plan) != wantBatches {
				t.Fatalf("incorrect bounded review selection: code=%s omitted=%v batches=%d", code, omitted, len(plan))
			}
			pc.ReviewPlan, pc.ReviewOmitted = plan, omitted
			pc.Issues = []PaperIssue{{Stage: "validating_paper", Code: code}}
			retained, err := retainedPaperReviewClaims(pc, TaskPaperReport, claims)
			must(t, err)
			if len(retained)+len(omitted) != len(claims) {
				t.Fatal("review packing lost an unrecorded claim")
			}
			for _, batch := range plan {
				var input struct {
					Sources []evidencePassage `json:"source_passages"`
				}
				must(t, json.Unmarshal([]byte(batch.Request), &input))
				for _, source := range input.Sources {
					if source.Quote != sources[source.ID].Quote {
						t.Fatal("packing clipped source text to fit a claim")
					}
				}
			}
			// An empty ReviewPlan is omitted from checkpoint JSON. Omitted IDs
			// must still distinguish a frozen all-omitted plan from no plan yet.
			var resumed PaperCheckpoint
			must(t, json.Unmarshal(budgetJSON(t, pc), &resumed))
			got, err := retainedPaperReviewClaims(&resumed, TaskPaperReport, claims)
			must(t, err)
			if !reflect.DeepEqual(got, retained) {
				t.Fatal("checkpoint roundtrip changed retained claims")
			}
		})
	}
}

func TestPaperPartialReviewRejectsInvalidOmissionCheckpoints(t *testing.T) {
	claims, sources := partialReviewPackingFixture("oversized_first")
	plan, omitted, code, err := planPaperReviewAvailable(&PaperCheckpoint{}, TaskPaperReport, claims, sources)
	must(t, err)
	base := PaperCheckpoint{ReviewPlan: plan, ReviewOmitted: omitted, Issues: []PaperIssue{{Stage: "validating_paper", Code: code}}}
	for _, scenario := range []string{"duplicate", "unknown", "retained_also_omitted", "missing_issue", "empty_plan", "changed_claim"} {
		t.Run(scenario, func(t *testing.T) {
			var pc PaperCheckpoint
			must(t, json.Unmarshal(budgetJSON(t, base), &pc))
			current := append([]reviewClaim(nil), claims...)
			switch scenario {
			case "duplicate":
				pc.ReviewOmitted = append(pc.ReviewOmitted, pc.ReviewOmitted[0])
			case "unknown":
				pc.ReviewOmitted[0] = "unknown"
			case "retained_also_omitted":
				pc.ReviewOmitted = append(pc.ReviewOmitted, claims[1].ID)
			case "missing_issue":
				pc.Issues = nil
			case "empty_plan":
				pc.ReviewPlan = nil
			case "changed_claim":
				current[1].Text = "changed factual claim"
			}
			if _, err := retainedPaperReviewClaims(&pc, TaskPaperReport, current); paperFailureCode(err) != "invalid_checkpoint" {
				t.Fatalf("accepted corrupted review omission plan: %v", err)
			}
		})
	}
}

func TestPaperPartialReviewPackingPersistsAndResumesWithoutNewCalls(t *testing.T) {
	for _, scenario := range []string{"oversized_first", "batch_limit", "all_oversized"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, nil)
			gateway := &workflowGateway{limits: generation.ModelLimits{ContextTokens: 262144, MaxOutputTokens: 8192}}
			f.s.Gateway = gateway
			r := f.claim(t, submitReport(t, f, c, "abstract").ID)
			cp := Checkpoint{Phase: "ready"}
			_, _, err := f.s.preparePaper(t.Context(), r, c, &cp, func(context.Context) error { return nil })
			must(t, err)
			claims, sources := partialReviewPackingFixture(scenario)
			verdicts, err := f.s.reviewPaper(t.Context(), r, &cp, func(context.Context) error { return nil }, claims, sources)
			must(t, err)
			if len(verdicts) != len(claims)-len(cp.Paper.ReviewOmitted) || len(cp.Paper.Issues) != 1 || cp.Paper.StageFailures["validating_paper"] != nil {
				t.Fatal("omitted claim was reviewed or packing issue prevented a real first batch")
			}
			calls := len(gateway.calls)
			frozen := directCheckpoint(t, f, r)
			replayed, err := f.s.reviewPaper(t.Context(), r, &frozen, func(context.Context) error { return nil }, claims, nil)
			must(t, err)
			if len(gateway.calls) != calls || !reflect.DeepEqual(verdicts, replayed) || !reflect.DeepEqual(frozen.Paper.ReviewOmitted, cp.Paper.ReviewOmitted) {
				t.Fatal("resuming reselected omitted claims or replayed a review call")
			}
			err = paperPublicationError(cp.Paper, verdicts)
			if scenario == "all_oversized" {
				if calls != 0 || paperFailureCode(err) != "context_too_large" {
					t.Fatal("all-omitted review spent tokens or became an empty successful answer")
				}
			} else {
				must(t, err)
			}
		})
	}
}
