package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Keep these predecessor versions explicit when workflow semantics change.
var paperPredecessorVersions = []string{"paper-fixed-v7", "paper-fixed-v8", "paper-fixed-v9"}

func TestPaperPredecessorIdempotencyPreservesFailedAndCompletedRuns(t *testing.T) {
	for _, version := range paperPredecessorVersions {
		for _, task := range []string{TaskPaperReport, TaskPaperFollowup} {
			for _, state := range []string{"failed", "completed"} {
				t.Run(version+"/"+task+"/"+state, func(t *testing.T) {
					f := newFixture(t)
					c, _ := workflowPaper(t, f, nil)
					g := &workflowGateway{}
					f.s.Gateway = g
					question := "旧版问题"
					if task == TaskPaperReport {
						question = PaperGoal
					}
					old := seedPaperContextRun(t, f, c, contextSeed{task: task, state: state, hash: contextPaperHash(t, f, c), question: question, answer: "原有回答", workflow: version})
					key := "predecessor-idempotency-key"
					// Frozen wire order independent of the current submissionHash.
					legacy := fmt.Sprintf(`{"task":%q,"question":%q,"provider":"glm","model":"glm-4.7-flash","idempotency_key":%q,"context_mode":"abstract","workflow_version":%q}`, task, question, key, version)
					digest := sha256.Sum256([]byte(legacy))
					wantHash := hex.EncodeToString(digest[:])
					must(t, f.db.Model(&Run{}).Where("id=?", old.ID).Updates(map[string]any{"input_hash": wantHash, "idempotency_key": key}).Error)
					input := SubmitInput{Task: task, Question: question, Provider: "glm", Model: "glm-4.7-flash", ContextMode: "abstract", IdempotencyKey: key}
					again, err := f.s.Submit(t.Context(), f.u.ID, c.ID, input)
					must(t, err)
					if again.ID != old.ID || again.State != state || again.WorkflowVersion != version || again.InputHash != wantHash || len(g.calls) != 0 {
						t.Fatalf("old request was restarted or reinterpreted: %+v", again)
					}
					input.ContextMode = "fulltext"
					if _, err := f.s.Submit(t.Context(), f.u.ID, c.ID, input); !errors.Is(err, ErrConflict) {
						t.Fatalf("accepted changed old input: %v", err)
					}
					var count int64
					must(t, f.db.Model(&Run{}).Where("conversation_id=?", c.ID).Count(&count).Error)
					if count != 1 || len(g.calls) != 0 {
						t.Fatal("retry spawned a new run or model call")
					}
				})
			}
		}
	}
}

func TestPaperPredecessorRecoveryNeverReplaysCalls(t *testing.T) {
	for _, version := range paperPredecessorVersions {
		for _, scenario := range []string{"ready", "calling", "calling-expired"} {
			t.Run(version+"/"+scenario, func(t *testing.T) {
				f := newFixture(t)
				c, _ := workflowPaper(t, f, nil)
				g := &workflowGateway{}
				f.s.Gateway = g
				r := directSubmit(t, f, c, "", "abstract")
				phase, want := "ready", "workflow_changed"
				if strings.HasPrefix(scenario, "calling") {
					phase, want = "calling", "result_unknown"
				}
				cp, err := json.Marshal(Checkpoint{Phase: phase, Calls: 1, Paper: &PaperCheckpoint{Outputs: map[string]json.RawMessage{"analyzing_answer": json.RawMessage(`{"status":"not_stated","claims":[]}`)}}})
				must(t, err)
				updates := map[string]any{"workflow_version": version, "checkpoint": cp}
				if scenario == "calling-expired" {
					updates["deadline"] = time.Now().Add(-time.Minute)
				}
				must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Updates(updates).Error)
				f.s.process(t.Context(), f.claim(t, r.ID))
				end, messages := paperOutcome(t, f, c, r)
				if end.FailureCode != want || len(g.calls) != 0 || len(messages) != 1 || (phase == "calling" && end.State != "unknown") {
					t.Fatalf("old call replayed: state=%s code=%s calls=%d", end.State, end.FailureCode, len(g.calls))
				}
			})
		}
	}
}

func TestPaperPredecessorCompletedReportsAndAnswersRemainReadable(t *testing.T) {
	for _, version := range paperPredecessorVersions {
		t.Run(version, func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, nil)
			hash := contextPaperHash(t, f, c)
			report := contextReport("历史报告")
			seedPaperContextRun(t, f, c, contextSeed{task: TaskPaperReport, hash: hash, question: PaperGoal, answer: "已有报告", report: report, workflow: version})
			seedPaperContextRun(t, f, c, contextSeed{hash: hash, question: "已有问题", answer: "已有回答", workflow: version})
			history, err := f.store.PaperHistory(t.Context(), c.ID, hash, "")
			must(t, err)
			want := []PaperConversationTurn{{Question: "已有问题", Answer: "已有回答"}}
			if !reflect.DeepEqual(history.Report, report) || !reflect.DeepEqual(history.Turns, want) {
				t.Fatalf("lost predecessor history: %+v", history)
			}
			messages, _, err := f.store.Messages(t.Context(), f.u.ID, c.ID, 0)
			must(t, err)
			if len(messages) != 4 {
				t.Fatalf("predecessor messages hidden: %+v", messages)
			}
		})
	}
}
