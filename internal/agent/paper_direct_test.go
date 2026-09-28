package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"signalwatch/internal/document"
	"signalwatch/internal/paper"
	"signalwatch/internal/platform/httpx"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Keep real document persistence while observing which operation the QA path
// chooses. An explicit retry uses Prepare; ordinary QA must only use Ensure.
type directDocuments struct {
	document.Store
	ensure       func(context.Context, document.Source) (document.Document, error)
	get          func(context.Context, string) (document.Document, error)
	ensureCalls  int
	prepareCalls int
}

func (s *directDocuments) Ensure(ctx context.Context, src document.Source) (document.Document, error) {
	s.ensureCalls++
	if s.ensure != nil {
		return s.ensure(ctx, src)
	}
	return s.Store.Ensure(ctx, src)
}

func (s *directDocuments) Prepare(ctx context.Context, src document.Source) (document.Document, error) {
	s.prepareCalls++
	return s.Store.Prepare(ctx, src)
}

func (s *directDocuments) Get(ctx context.Context, id string) (document.Document, error) {
	if s.get != nil {
		return s.get(ctx, id)
	}
	return s.Store.Get(ctx, id)
}

func directInput(task, mode string) SubmitInput {
	return SubmitInput{Task: task, Question: "论文使用什么方法？", Provider: "glm", Model: "glm-4.7-flash", ContextMode: mode, IdempotencyKey: rand.Text()}
}

func directSubmit(t *testing.T, f *fixture, c Conversation, task, mode string) Run {
	t.Helper()
	r, err := f.s.Submit(t.Context(), f.u.ID, c.ID, directInput(task, mode))
	must(t, err)
	return r
}

func directCheckpoint(t *testing.T, f *fixture, r Run) Checkpoint {
	t.Helper()
	stored, err := f.store.RunByID(t.Context(), f.u.ID, r.ID)
	must(t, err)
	var cp Checkpoint
	must(t, json.Unmarshal(stored.Checkpoint, &cp))
	return cp
}

func directResult(t *testing.T, f *fixture, r Run) PaperResult {
	t.Helper()
	var message Message
	must(t, f.db.Where("run_id=? AND role='assistant'", r.ID).Take(&message).Error)
	var result PaperResult
	must(t, json.Unmarshal(message.Result, &result))
	return result
}

func directReady(t *testing.T, f *fixture, d document.Document) {
	t.Helper()
	must(t, f.db.Model(&document.Document{}).Where("id=?", d.ID).Updates(map[string]any{
		"state": "ready", "text_complete": true, "page_count": 1,
		"content_hash": "direct-ready-hash", "source_version": "1706.03762v1", "failure_code": "",
	}).Error)
	chunks := document.Split(d.ID, []string{"The retrieval method uses a held out dataset to evaluate the experiment."})
	must(t, f.db.CreateInBatches(chunks, 50).Error)
}

func directThreeStages(t *testing.T, f *fixture, r Run) {
	t.Helper()
	steps, err := f.store.Steps(t.Context(), f.u.ID, r.ID)
	must(t, err)
	want := []string{"normalizing_question", "analyzing_answer", "validating_paper"}
	if len(steps) != len(want) {
		t.Fatalf("QA used %d stages: %+v", len(steps), steps)
	}
	for i, stage := range want {
		if steps[i].Tool != stage || steps[i].Kind != "model" || steps[i].FailureCode != "" {
			t.Fatalf("QA stage %d = %+v, want %s", i, steps[i], stage)
		}
	}
}

func TestDirectPaperQuestionsNeedNoReportAndUseThreeCalls(t *testing.T) {
	for _, task := range []string{"", TaskPaperFollowup} {
		for _, mode := range []string{"abstract", "fulltext"} {
			t.Run(fmt.Sprintf("task=%s/mode=%s", task, mode), func(t *testing.T) {
				f := newFixture(t)
				c, _ := workflowPaper(t, f, []string{"The retrieval method evaluates an experiment on a held out dataset."})
				g := &workflowGateway{}
				f.s.Gateway = g
				if _, err := f.store.LatestPaperReport(t.Context(), c.ID); !errors.Is(err, ErrNotFound) {
					t.Fatalf("fixture unexpectedly has a report: %v", err)
				}
				r := directSubmit(t, f, c, task, mode)
				if r.Task != TaskPaperFollowup || r.WorkflowVersion != PaperWorkflowVersion {
					t.Fatalf("incorrect direct QA normalization: %+v", r)
				}
				f.s.process(t.Context(), f.claim(t, r.ID))
				end, messages := paperOutcome(t, f, c, r)
				if end.State != "completed" || len(g.calls) != 3 || len(messages) != 2 {
					t.Fatalf("state=%s failure=%s calls=%d messages=%d", end.State, end.FailureCode, len(g.calls), len(messages))
				}
				result := directResult(t, f, r)
				if result.Report != nil || result.ContextMode != mode || result.Fields["answer"].Status != "supported" {
					t.Fatalf("unexpected direct QA result: %+v", result)
				}
				directThreeStages(t, f, r)
			})
		}
	}
}

func TestDirectPaperQuestionHTTPRejectsForeignConversationAndRevokedPaper(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	other := newFixture(t)
	for _, scenario := range []string{"foreign_user", "revoked_paper"} {
		t.Run(scenario, func(t *testing.T) {
			uid := other.u.ID
			if scenario == "revoked_paper" {
				uid = f.u.ID
				must(t, f.db.Where("paper_id=?", *c.PaperID).Delete(&paper.SubscriptionPaper{}).Error)
			}
			router := gin.New()
			router.Use(func(ctx *gin.Context) {
				httpx.SetCurrentUserID(ctx, httpx.UserID(uid))
				ctx.Next()
			})
			router.POST("/api/v2/agent/conversations/:id/messages", Handler{Service: f.s}.Handle)
			body := `{"question":"解释这篇论文","provider":"glm","model":"glm-4.7-flash","idempotency_key":"unauthorized-direct-question"}`
			request := httptest.NewRequest("POST", "/api/v2/agent/conversations/"+c.ID+"/messages", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var failure map[string]any
			decodeAgentHTTPJSON(t, response, 404, &failure)
			var runs, messages int64
			must(t, f.db.Model(&Run{}).Where("conversation_id=?", c.ID).Count(&runs).Error)
			must(t, f.db.Model(&Message{}).Where("conversation_id=?", c.ID).Count(&messages).Error)
			stored, err := f.store.Conversation(t.Context(), f.u.ID, c.ID)
			must(t, err)
			if runs != 0 || messages != 0 || stored.ActiveRunID != nil || f.gateway.calls.Load() != 0 {
				t.Fatalf("rejected request wrote work: runs=%d messages=%d active=%v calls=%d", runs, messages, stored.ActiveRunID, f.gateway.calls.Load())
			}
		})
	}
}

func TestDirectPaperQAUsesReadyFulltextAfterAnAbstractReport(t *testing.T) {
	f := newFixture(t)
	c, d := workflowPaper(t, f, nil)
	g := &workflowGateway{}
	f.s.Gateway = g
	report := submitReport(t, f, c, "abstract")
	f.s.process(t.Context(), f.claim(t, report.ID))
	if result := directResult(t, f, report); result.ContextMode != "abstract" || result.Report == nil {
		t.Fatalf("fixture report was not abstract: %+v", result)
	}
	directReady(t, f, d)
	r := directSubmit(t, f, c, TaskPaperFollowup, "fulltext")
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, messages := paperOutcome(t, f, c, r)
	result := directResult(t, f, r)
	if end.State != "completed" || result.ContextMode != "fulltext" || result.DocumentID != d.ID || len(g.calls) != 9 || len(messages) != 4 {
		t.Fatalf("abstract report pinned a new question: state=%s result=%+v calls=%d messages=%d", end.State, result, len(g.calls), len(messages))
	}
	if old := directResult(t, f, report); old.ContextMode != "abstract" {
		t.Fatal("upgrading a new QA changed the previous report")
	}
	directThreeStages(t, f, r)
}

func TestDirectPaperPendingPreparationPersistsDeadlineBeforeEnsure(t *testing.T) {
	f := newFixture(t)
	c, d := workflowPaper(t, f, nil)
	f.s.paperPreparationTimeout = 150 * time.Millisecond
	g := &workflowGateway{}
	f.s.Gateway = g
	r := directSubmit(t, f, c, TaskPaperFollowup, "fulltext")
	base := f.s.Documents
	watched := &directDocuments{Store: base}
	var savedDeadline time.Time
	watched.ensure = func(ctx context.Context, src document.Source) (document.Document, error) {
		cp := directCheckpoint(t, f, r)
		if cp.Paper == nil || cp.Paper.PreparationDeadline == nil {
			t.Fatal("document Ensure ran before the preparation deadline was persisted")
		}
		savedDeadline = *cp.Paper.PreparationDeadline
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(savedDeadline) {
			t.Fatalf("Ensure deadline=%v, stored=%v", deadline, savedDeadline)
		}
		return base.Ensure(ctx, src)
	}
	f.s.Documents = watched
	started := time.Now()
	f.s.process(t.Context(), f.claim(t, r.ID))
	if time.Since(started) > 3*time.Second {
		t.Fatal("short preparation budget unexpectedly waited for the production timeout")
	}
	end, _ := paperOutcome(t, f, c, r)
	cp := directCheckpoint(t, f, r)
	if end.State != "completed" || end.EffectiveContextMode != "abstract" || end.FallbackReason != "document_timeout" || len(g.calls) != 3 {
		t.Fatalf("pending preparation did not become a bounded abstract answer: %+v calls=%d", end, len(g.calls))
	}
	if savedDeadline.IsZero() || cp.Paper.PreparationDeadline == nil || !cp.Paper.PreparationDeadline.Equal(savedDeadline) || cp.DocumentID != "" {
		t.Fatalf("preparation deadline or abstract checkpoint changed: %+v", cp)
	}
	if watched.ensureCalls != 1 || watched.prepareCalls != 0 {
		t.Fatalf("ordinary QA restarted preparation: Ensure=%d Prepare=%d", watched.ensureCalls, watched.prepareCalls)
	}
	doc, err := base.Get(t.Context(), d.ID)
	must(t, err)
	if doc.State != "pending" {
		t.Fatalf("QA timeout changed the independent document task: %+v", doc)
	}
}

func TestDirectPaperSlowEnsureUsesPreparationDeadline(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	f.s.paperPreparationTimeout = 100 * time.Millisecond
	g := &workflowGateway{}
	f.s.Gateway = g
	watched := &directDocuments{Store: f.s.Documents}
	watched.ensure = func(ctx context.Context, _ document.Source) (document.Document, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("Ensure has no bounded context")
		}
		<-ctx.Done()
		return document.Document{}, ctx.Err()
	}
	f.s.Documents = watched
	r := directSubmit(t, f, c, "", "fulltext")
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, _ := paperOutcome(t, f, c, r)
	if end.State != "completed" || end.FallbackReason != "document_timeout" || end.EffectiveContextMode != "abstract" || len(g.calls) != 3 {
		t.Fatalf("preparation-local deadline did not safely fall back: %+v calls=%d", end, len(g.calls))
	}
}

func TestDirectPaperPollingReadUsesTheSamePreparationDeadline(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	// The production poll interval is one second. Only this polling-boundary
	// check needs to span it; no test waits for the twenty-second user budget.
	f.s.paperPreparationTimeout = 1200 * time.Millisecond
	g := &workflowGateway{}
	f.s.Gateway = g
	r := directSubmit(t, f, c, "", "fulltext")
	watched := &directDocuments{Store: f.s.Documents}
	reads := 0
	watched.get = func(ctx context.Context, _ string) (document.Document, error) {
		reads++
		cp := directCheckpoint(t, f, r)
		deadline, ok := ctx.Deadline()
		if cp.Paper == nil || cp.Paper.PreparationDeadline == nil || !ok || !deadline.Equal(*cp.Paper.PreparationDeadline) {
			t.Fatalf("document polling escaped its persisted deadline: context=%v checkpoint=%+v", deadline, cp)
		}
		<-ctx.Done()
		return document.Document{}, ctx.Err()
	}
	f.s.Documents = watched
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, _ := paperOutcome(t, f, c, r)
	if reads != 1 || end.State != "completed" || end.EffectiveContextMode != "abstract" || end.FallbackReason != "document_timeout" || len(g.calls) != 3 {
		t.Fatalf("polling deadline did not bound the read: reads=%d state=%s failure=%s calls=%d", reads, end.State, end.FailureCode, len(g.calls))
	}
}

type directFailingSaveStore struct{ Store }

func (s directFailingSaveStore) Save(context.Context, Run, Checkpoint, string, *Step) error {
	return errors.New("fixture checkpoint persistence unavailable")
}

func TestDirectPaperPreparationDoesNotStartBeforeCheckpointSave(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	g := &workflowGateway{}
	f.s.Gateway = g
	r := f.claim(t, directSubmit(t, f, c, "", "fulltext").ID)
	watched := &directDocuments{Store: f.s.Documents}
	f.s.Documents = watched
	f.s.Store = directFailingSaveStore{f.store}
	f.s.process(t.Context(), r)
	end, messages := paperOutcome(t, f, c, r)
	if end.State == "completed" || len(g.calls) != 0 || len(messages) != 1 || watched.ensureCalls != 0 || watched.prepareCalls != 0 {
		t.Fatalf("failed checkpoint save started new work: state=%s calls=%d Ensure=%d Prepare=%d", end.State, len(g.calls), watched.ensureCalls, watched.prepareCalls)
	}
}

func TestDirectPaperExpiredPreparationIsNotRenewedAfterRecovery(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	f.s.Gateway = &workflowGateway{}
	r := f.claim(t, directSubmit(t, f, c, "", "fulltext").ID)
	p, err := f.s.Papers.Get(t.Context(), f.u.ID, *c.PaperID)
	must(t, err)
	expired := time.Now().UTC().Add(-time.Minute)
	hash := sha256.Sum256([]byte(p.Title + "\n" + p.Abstract))
	cp := Checkpoint{Phase: "ready", Paper: &PaperCheckpoint{Context: contextOf(p), PaperHash: paperSnapshotHash(p), Outputs: map[string]json.RawMessage{}, PreparationDeadline: &expired}, Evidence: []Citation{{ID: "abstract", DocumentID: "abstract:" + hex.EncodeToString(hash[:]), ContentHash: hex.EncodeToString(hash[:]), Quote: p.Title + "\n" + p.Abstract, URL: p.ArXivURL}}}
	must(t, f.store.Save(t.Context(), r, cp, "preparing_document", nil))
	must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
	next := f.claim(t, r.ID)
	if next.Epoch <= r.Epoch {
		t.Fatal("recovery did not acquire a new lease epoch")
	}
	must(t, json.Unmarshal(next.Checkpoint, &cp))
	watched := &directDocuments{Store: f.s.Documents}
	f.s.Documents = watched
	_, evidence, err := f.s.preparePaper(t.Context(), next, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, next) })
	must(t, err)
	if cp.Paper.Mode != "abstract" || cp.Paper.FallbackReason != "document_timeout" || !cp.Paper.PreparationDeadline.Equal(expired) || len(evidence) == 0 {
		t.Fatalf("recovery restarted an expired preparation budget: %+v", cp)
	}
	if watched.ensureCalls != 0 || watched.prepareCalls != 0 {
		t.Fatalf("expired preparation re-entered the document store: %+v", watched)
	}
	stored := directCheckpoint(t, f, next)
	if stored.Paper.PreparationDeadline == nil || !stored.Paper.PreparationDeadline.Equal(expired) || stored.Paper.Mode != "abstract" {
		t.Fatalf("recovered fallback was not persisted: %+v", stored)
	}
	must(t, f.store.Cancel(t.Context(), f.u.ID, r.ID))
}

func TestDirectPaperAbstractCheckpointStaysFixedButNewRunCanUpgrade(t *testing.T) {
	f := newFixture(t)
	c, d := workflowPaper(t, f, nil)
	f.s.paperPreparationTimeout = 100 * time.Millisecond
	g := &workflowGateway{}
	f.s.Gateway = g
	r := f.claim(t, directSubmit(t, f, c, "", "fulltext").ID)
	var cp Checkpoint
	must(t, json.Unmarshal(r.Checkpoint, &cp))
	_, _, err := f.s.preparePaper(t.Context(), r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) })
	must(t, err)
	if cp.Paper.Mode != "abstract" {
		t.Fatal("fixture did not fix the current run to abstract")
	}
	directReady(t, f, d)
	watched := &directDocuments{Store: f.s.Documents}
	f.s.Documents = watched
	must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
	f.s.process(t.Context(), f.claim(t, r.ID))
	if result := directResult(t, f, r); result.ContextMode != "abstract" || result.DocumentID != "" {
		t.Fatalf("recovery changed the mode of an existing run: %+v", result)
	}
	if watched.ensureCalls != 0 || watched.prepareCalls != 0 {
		t.Fatal("fixed abstract checkpoint re-entered document preparation")
	}
	next := directSubmit(t, f, c, TaskPaperFollowup, "fulltext")
	f.s.process(t.Context(), f.claim(t, next.ID))
	if result := directResult(t, f, next); result.ContextMode != "fulltext" || result.DocumentID != d.ID {
		t.Fatalf("new run did not use newly available fulltext: %+v", result)
	}
	if len(g.calls) != 6 || watched.ensureCalls != 1 || watched.prepareCalls != 0 {
		t.Fatalf("calls=%d Ensure=%d Prepare=%d", len(g.calls), watched.ensureCalls, watched.prepareCalls)
	}
}

func TestDirectPaperFailedDocumentIsNotAutomaticallyRetried(t *testing.T) {
	f := newFixture(t)
	c, d := workflowPaper(t, f, nil)
	must(t, f.db.Model(&document.Document{}).Where("id=?", d.ID).Updates(map[string]any{"state": "failed", "failure_code": "ocr_required", "epoch": 7}).Error)
	g := &workflowGateway{}
	f.s.Gateway = g
	watched := &directDocuments{Store: f.s.Documents}
	f.s.Documents = watched
	r := directSubmit(t, f, c, "", "fulltext")
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, _ := paperOutcome(t, f, c, r)
	stored, err := watched.Store.Get(t.Context(), d.ID)
	must(t, err)
	if end.State != "completed" || end.EffectiveContextMode != "abstract" || end.FallbackReason != "ocr_required" || len(g.calls) != 3 {
		t.Fatalf("failed document did not use available abstract: %+v", end)
	}
	if watched.ensureCalls != 1 || watched.prepareCalls != 0 || stored.State != "failed" || stored.FailureCode != "ocr_required" || stored.Epoch != 7 {
		t.Fatalf("QA implicitly retried a failed extraction: Ensure=%d Prepare=%d document=%+v", watched.ensureCalls, watched.prepareCalls, stored)
	}
}

func TestDirectPaperPreparationDoesNotHideCancellationLeaseOrAccessLoss(t *testing.T) {
	for _, failure := range []string{"cancel", "parent_deadline", "lease", "permission", "conflict", "storage"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, nil)
			g := &workflowGateway{}
			f.s.Gateway = g
			f.s.paperPreparationTimeout = time.Second
			r := f.claim(t, directSubmit(t, f, c, "", "fulltext").ID)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if failure == "parent_deadline" {
				deadline := time.Now().UTC().Add(100 * time.Millisecond)
				r.Deadline = &deadline
				must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("deadline", deadline).Error)
			}
			base := f.s.Documents
			watched := &directDocuments{Store: base}
			watched.ensure = func(preparation context.Context, src document.Source) (document.Document, error) {
				switch failure {
				case "cancel":
					cancel()
					return document.Document{}, preparation.Err()
				case "parent_deadline":
					<-preparation.Done()
					return document.Document{}, preparation.Err()
				case "lease":
					must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
				case "permission":
					must(t, f.db.Where("paper_id=?", *c.PaperID).Delete(&paper.SubscriptionPaper{}).Error)
				case "conflict":
					return document.Document{}, ErrConflict
				case "storage":
					return document.Document{}, errors.New("fixture document read failed")
				}
				return base.Ensure(preparation, src)
			}
			f.s.Documents = watched
			f.s.process(ctx, r)
			end, err := f.store.RunByID(t.Context(), f.u.ID, r.ID)
			must(t, err)
			cp := directCheckpoint(t, f, r)
			messages, _, err := f.store.Messages(t.Context(), f.u.ID, c.ID, 0)
			must(t, err)
			if len(g.calls) != 0 || len(messages) != 1 || end.State == "completed" || (cp.Paper != nil && cp.Paper.Mode == "abstract") {
				t.Fatalf("%s became an abstract model request: state=%s failure=%s calls=%d cp=%+v", failure, end.State, end.FailureCode, len(g.calls), cp)
			}
		})
	}
}

func TestDirectPaperV5IdempotencyUsesTheOriginalSerializedInput(t *testing.T) {
	for _, task := range []string{TaskPaperFollowup, TaskPaperReport} {
		t.Run(task, func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, nil)
			g := &workflowGateway{}
			f.s.Gateway = g
			question := "论文使用什么方法？"
			if task == TaskPaperReport {
				question = "帮助用户快速了解当前论文"
			}
			// This is the actual v5 JSON field order and omission behavior, not
			// a call to the new version's submissionHash or a new struct marshal.
			legacyJSON := `{"task":"paper_followup","question":"论文使用什么方法？","provider":"glm","model":"glm-4.7-flash","idempotency_key":"legacy-v5-request-key","context_mode":"fulltext","workflow_version":"paper-fixed-v5"}`
			if task == TaskPaperReport {
				legacyJSON = `{"task":"paper_report","question":"帮助用户快速了解当前论文","provider":"glm","model":"glm-4.7-flash","idempotency_key":"legacy-v5-request-key","context_mode":"fulltext","workflow_version":"paper-fixed-v5"}`
			}
			hash := sha256.Sum256([]byte(legacyJSON))
			legacyHash := hex.EncodeToString(hash[:])
			now := time.Now().UTC()
			old := Run{ID: rand.Text(), ConversationID: c.ID, UserID: f.u.ID, Task: task, WorkflowVersion: "paper-fixed-v5", Question: question, Provider: "glm", Model: "glm-4.7-flash", Generation: "agent-fixture", Version: 1, IdempotencyKey: "legacy-v5-request-key", InputHash: legacyHash, ContextMode: "fulltext", State: "completed", Checkpoint: json.RawMessage(`{"phase":"ready"}`), CreatedAt: now, UpdatedAt: now}
			must(t, f.db.Create(&old).Error)
			messages := []Message{{ConversationID: c.ID, RunID: old.ID, Role: "user", Content: question, Citations: json.RawMessage(`[]`), CreatedAt: now}, {ConversationID: c.ID, RunID: old.ID, Role: "assistant", Content: "Saved v5 answer", Citations: json.RawMessage(`[]`), CreatedAt: now}}
			must(t, f.db.Create(&messages).Error)
			input := SubmitInput{Task: task, Question: question, Provider: "glm", Model: "glm-4.7-flash", IdempotencyKey: old.IdempotencyKey}
			for _, implicit := range []bool{false, true} {
				replay := input
				if implicit && task == TaskPaperFollowup {
					replay.Task = ""
				}
				if implicit && task == TaskPaperReport {
					replay.Question = ""
				}
				again, err := f.s.Submit(t.Context(), f.u.ID, c.ID, replay)
				must(t, err)
				if again.ID != old.ID || again.WorkflowVersion != "paper-fixed-v5" || again.InputHash != legacyHash {
					t.Fatalf("compatible v5 retry produced a different run: %+v", again)
				}
			}
			for _, change := range []string{"task", "provider", "model", "credential", "mode", "question"} {
				if task == TaskPaperReport && change == "question" {
					continue // A report's question is intentionally canonicalized.
				}
				changed := input
				switch change {
				case "task":
					if task == TaskPaperReport {
						changed.Task = TaskPaperFollowup
					} else {
						changed.Task = TaskPaperReport
					}
				case "provider":
					changed.Provider = "different"
				case "model":
					changed.Model = "different"
				case "credential":
					changed.CredentialID = "different"
				case "mode":
					changed.ContextMode = "abstract"
				case "question":
					changed.Question = "论文实验是什么？"
				}
				if _, err := f.s.Submit(t.Context(), f.u.ID, c.ID, changed); !errors.Is(err, ErrConflict) {
					t.Fatalf("v5 payload change %s was not rejected: %v", change, err)
				}
			}
			stored, err := f.store.RunByID(t.Context(), f.u.ID, old.ID)
			must(t, err)
			var runs, count int64
			must(t, f.db.Model(&Run{}).Where("conversation_id=?", c.ID).Count(&runs).Error)
			must(t, f.db.Model(&Message{}).Where("conversation_id=?", c.ID).Count(&count).Error)
			if stored.InputHash != legacyHash || stored.WorkflowVersion != "paper-fixed-v5" || runs != 1 || count != 2 || len(g.calls) != 0 {
				t.Fatalf("legacy retry rewrote history: run=%+v runs=%d messages=%d calls=%d", stored, runs, count, len(g.calls))
			}
		})
	}
}

func TestDirectPaperV5CallingRecoveryRemainsUnknownBeforeVersionAndDeadline(t *testing.T) {
	for _, phase := range []string{"calling", "ready"} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("phase=%s/expired=%t", phase, expired), func(t *testing.T) {
				f := newFixture(t)
				c, _ := workflowPaper(t, f, nil)
				g := &workflowGateway{}
				f.s.Gateway = g
				r := directSubmit(t, f, c, "", "abstract")
				cp, err := json.Marshal(Checkpoint{Phase: phase})
				must(t, err)
				updates := map[string]any{"workflow_version": "paper-fixed-v5", "checkpoint": cp}
				if expired {
					updates["deadline"] = time.Now().Add(-time.Minute)
				}
				must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Updates(updates).Error)
				f.s.process(t.Context(), f.claim(t, r.ID))
				end, messages := paperOutcome(t, f, c, r)
				want := "workflow_changed"
				if expired {
					want = "budget_exhausted"
				}
				if phase == "calling" {
					want = "result_unknown"
				}
				if end.FailureCode != want || len(g.calls) != 0 || len(messages) != 1 || (phase == "calling" && end.State != "unknown") {
					t.Fatalf("old checkpoint recovery precedence changed: %+v calls=%d", end, len(g.calls))
				}
			})
		}
	}
}
