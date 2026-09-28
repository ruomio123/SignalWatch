package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// A returned Save error is different from a process crash: the Before callback
// returns without starting the provider request, and a later terminal write can
// durably restore the unspent call and repair reservation.
type paperReservationFailureStore struct {
	Store
	stage          string
	after, tripped bool
}

func (s *paperReservationFailureStore) Save(ctx context.Context, r Run, cp Checkpoint, progress string, step *Step) error {
	if !s.tripped && step == nil && cp.Phase == "calling" && cp.Paper != nil && cp.Paper.CurrentStage == s.stage {
		s.tripped = true
		if s.after {
			if err := s.Store.Save(ctx, r, cp, progress, step); err != nil {
				return err
			}
		}
		return errors.New("fixture intermittent reservation write failure")
	}
	return s.Store.Save(ctx, r, cp, progress, step)
}

func TestPaperCallReservationSaveFailureDoesNotConsumeCallOrRepair(t *testing.T) {
	for _, stage := range []string{"analyzing_results", "repairing_results"} {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/after=%t", stage, after), func(t *testing.T) {
				f, c, submitted, gateway := repairWorkflowFixture(t, TaskPaperReport, "results")
				store := &paperReservationFailureStore{Store: f.store, stage: stage, after: after}
				f.s.Store = store
				f.s.process(t.Context(), f.claim(t, submitted.ID))
				end, messages := paperOutcome(t, f, c, submitted)
				cp := directCheckpoint(t, f, submitted)
				wantCalls := 3
				if stage == "repairing_results" {
					wantCalls = 4
				}
				if !store.tripped || len(gateway.calls) != wantCalls || cp.Calls != wantCalls || end.State != "failed" || end.FailureCode != "storage_failed" || end.FailureStage != stage || len(messages) != 1 || cp.Phase != "failed" || cp.Paper.TerminalFailure != "storage_failed" {
					t.Fatalf("failed reservation counted or failure stage lost: end=%+v calls=%d checkpoint=%+v", end, len(gateway.calls), cp)
				}
				if end.FailureDetail != nil {
					t.Fatal("original output limit masked reservation storage failure")
				}
				if stage == "repairing_results" {
					if cp.Paper.Repair == nil || cp.Paper.Repair.Attempted || cp.Paper.Repair.State != "failed" || end.RepairSummary == nil || end.RepairSummary.Attempted {
						t.Fatalf("unstarted repair reported as attempted: repair=%+v summary=%+v", cp.Paper.Repair, end.RepairSummary)
					}
				} else if cp.Paper.Repair != nil {
					t.Fatal("unstarted analysis scheduled a repair")
				}
				steps, err := f.store.Steps(t.Context(), f.u.ID, submitted.ID)
				must(t, err)
				last := steps[len(steps)-1]
				if last.Tool != stage || last.FailureCode != "storage_failed" || last.CallID != "" {
					t.Fatalf("incorrect final failure step: %+v", last)
				}
			})
		}
	}
}
