package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"signalwatch/internal/document"
	"signalwatch/internal/generation"
	"signalwatch/internal/paper"
	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/subscription"
	"strings"
	"testing"
	"time"
)

type workflowGateway struct {
	version    uint64
	candidates bool
	calls      []ModelRequest
	hook       func(ModelRequest) error
	reject     bool
	malformed  bool
	batches    []string
}

func (g *workflowGateway) Selection(context.Context, uint64, string, string, string) (Selection, error) {
	version := g.version
	if version == 0 {
		version = 1
	}
	return Selection{"agent-fixture", version}, nil
}
func (g *workflowGateway) Generate(ctx context.Context, req ModelRequest) (generation.Result, error) {
	if err := req.Before(ctx); err != nil {
		return generation.Result{}, err
	}
	g.calls = append(g.calls, req)
	if g.hook != nil {
		if err := g.hook(req); err != nil {
			return generation.Result{}, err
		}
	}
	var input struct {
		Field    string        `json:"field"`
		Evidence []Citation    `json:"evidence"`
		Claims   []reviewClaim `json:"claims"`
	}
	if err := json.Unmarshal(req.Input, &input); err != nil {
		return generation.Result{}, err
	}
	var value any
	switch {
	case strings.Contains(req.System, "Resolve pronouns"):
		value = map[string]string{"question": "论文使用什么方法？", "query": "retrieval method experiment"}
	case strings.Contains(req.System, "Read ALL supplied"):
		refs := map[string][]evidenceOutput{}
		for _, field := range paperFields {
			refs[field] = []evidenceOutput{}
			if g.candidates && len(input.Evidence) > 0 {
				e := input.Evidence[0]
				refs[field] = []evidenceOutput{{ID: e.ID}}
			}
		}
		for _, e := range input.Evidence {
			g.batches = append(g.batches, e.ID)
		}
		value = refs
	case strings.Contains(req.System, "Check EVERY"):
		results := []map[string]any{}
		for _, c := range input.Claims {
			results = append(results, map[string]any{"id": c.ID, "supported": !g.reject})
		}
		value = map[string]any{"verdicts": results}
	default:
		if len(input.Evidence) > 0 {
			id := input.Evidence[0].ID
			value = fieldOutput{Status: "supported", Claims: []claimOutput{{Text: "论文研究检索方法。", Evidence: []evidenceOutput{{ID: id}}}}}
		} else {
			value = fieldOutput{Status: "not_stated", Claims: []claimOutput{}}
		}
	}
	raw, _ := json.Marshal(value)
	if g.malformed && strings.Contains(req.System, "Check EVERY") {
		raw = []byte(`{"verdicts":[],"new_claim":"invented"}`)
	}
	result := generation.Result{CallID: fmt.Sprintf("test-%d", len(g.calls)), Content: raw}
	if err := req.Validate(result); err != nil {
		return result, err
	}
	return result, nil
}
func workflowPaper(t *testing.T, f *fixture, texts []string) (Conversation, document.Document) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	p := paper.Paper{SourceID: f.src.ID, ArXivID: "wf-" + rand.Text(), Title: "A retrieval method", Abstract: "The paper studies retrieval methods and experiments with held out datasets.", AuthorsJSON: json.RawMessage(`["Researcher"]`), CategoriesJSON: json.RawMessage(`["cs.AI"]`), PDFURL: "https://arxiv.org/pdf/1706.03762v1", ArXivURL: "https://arxiv.org/abs/1706.03762v1", PublishedAt: now, ArXivUpdatedAt: now, FirstSeenAt: now, CreatedAt: now, UpdatedAt: now}
	must(t, f.db.Create(&p).Error)
	t.Cleanup(func() {
		f.db.Where("paper_id=?", p.ID).Delete(&Conversation{})
		f.db.Where("paper_id=?", p.ID).Delete(&paper.SubscriptionPaper{})
		f.db.Where("paper_id=?", p.ID).Delete(&document.Document{})
		f.db.Delete(&p)
	})
	sub, err := f.s.Subscriptions.(*subscription.Service).Create(context.Background(), f.u.ID, subscription.CreateInput{SourceID: f.src.ID, Name: "report", Rules: subscription.RulesInput{Category: "cs.AI"}})
	must(t, err)
	must(t, f.db.Create(&paper.SubscriptionPaper{SubscriptionID: sub.ID, PaperID: p.ID, MatchedKeywordsJSON: json.RawMessage(`[]`), MatchedAt: now}).Error)
	doc, err := f.s.Documents.Ensure(context.Background(), document.Source{PaperID: p.ID, ArXivID: p.ArXivID, PDFURL: p.PDFURL, UpdatedAt: p.ArXivUpdatedAt})
	must(t, err)
	if texts != nil {
		must(t, f.db.Model(&document.Document{}).Where("id=?", doc.ID).Updates(map[string]any{"state": "ready", "text_complete": true, "page_count": len(texts), "content_hash": "fixture-content-hash", "source_version": "1706.03762v1"}).Error)
		chunks := document.Split(doc.ID, texts)
		must(t, f.db.CreateInBatches(chunks, 50).Error)
	}
	c, err := f.s.CreateConversation(context.Background(), f.u.ID, "paper", &p.ID)
	must(t, err)
	return c, doc
}
func submitReport(t *testing.T, f *fixture, c Conversation, mode string) Run {
	t.Helper()
	r, err := f.s.Submit(context.Background(), f.u.ID, c.ID, SubmitInput{Task: TaskPaperReport, Provider: "glm", Model: "glm-4.7-flash", ContextMode: mode, IdempotencyKey: rand.Text()})
	must(t, err)
	return r
}
func paperOutcome(t *testing.T, f *fixture, c Conversation, r Run) (Run, []Message) {
	t.Helper()
	end, err := f.s.RunByID(context.Background(), f.u.ID, r.ID)
	must(t, err)
	messages, _, err := f.store.Messages(context.Background(), f.u.ID, c.ID, 0)
	must(t, err)
	return end, messages
}
func TestFixedPaperReportSixCallsAndSemanticRejection(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, []string{"The retrieval method uses a held out evaluation dataset to verify the results."})
			g := &workflowGateway{reject: reject}
			f.s.Gateway = g
			r := submitReport(t, f, c, "fulltext")
			claimed := f.claim(t, r.ID)
			if claimed.Deadline == nil || time.Until(*claimed.Deadline) < 14*time.Minute {
				t.Fatal("report deadline not applied")
			}
			f.s.process(context.Background(), claimed)
			end, messages := paperOutcome(t, f, c, r)
			if end.State != "completed" || len(g.calls) != 6 || len(messages) != 2 {
				t.Fatalf("%+v calls=%d messages=%d", end, len(g.calls), len(messages))
			}
			for i, field := range paperFields {
				if !strings.Contains(string(g.calls[i].Input), `"output_language":"zh-CN"`) || !strings.Contains(g.calls[i].System, "输出语言固定为简体中文") {
					t.Fatal("report language policy missing")
				}
				if !strings.Contains(string(g.calls[i].Input), `"field":"`+field+`"`) {
					t.Fatal("task order changed")
				}
			}
			var result PaperResult
			must(t, json.Unmarshal(messages[1].Result, &result))
			if result.Report == nil || result.ContextMode != "fulltext" || result.Coverage != "all_extracted_text" {
				t.Fatal(result)
			}
			if reject && (result.Fields["results"].Status != "insufficient_evidence" || strings.Contains(messages[1].Content, "论文研究检索方法")) {
				t.Fatal("rejected claim published")
			}
			if !reject && result.Fields["results"].Status != "supported" {
				t.Fatal(result)
			}
		})
	}
}
func TestLongReportCoversEveryChunkAndRespectsBudget(t *testing.T) {
	for _, pages := range []int{18, 210} {
		t.Run(fmt.Sprint(pages), func(t *testing.T) {
			f := newFixture(t)
			texts := make([]string, pages)
			for i := range texts {
				texts[i] = strings.Repeat(fmt.Sprintf("Page%03d retrieval experiments ", i), 240)
			}
			c, d := workflowPaper(t, f, texts)
			g := &workflowGateway{candidates: true}
			f.s.Gateway = g
			r := submitReport(t, f, c, "fulltext")
			f.s.process(context.Background(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			if pages == 210 {
				if end.FailureCode != "budget_exhausted" || len(g.calls) != 0 || len(messages) != 1 {
					t.Fatalf("budget %+v calls=%d", end, len(g.calls))
				}
				return
			}
			chunks, err := f.s.Documents.Chunks(context.Background(), d.ID)
			must(t, err)
			expected := []string{}
			for _, chunk := range chunks {
				for _, passage := range document.Passages(chunk.Text, paperPassageLimit) {
					expected = append(expected, fmt.Sprintf("p%d-c%d-s%d", chunk.Page, chunk.Number, passage.Start))
				}
			}
			if end.State != "completed" || end.BatchTotal < 2 || len(g.calls) != end.BatchTotal+6 || len(g.batches) != len(expected) {
				t.Fatalf("%+v calls=%d chunks=%d seen=%d", end, len(g.calls), len(chunks), len(g.batches))
			}
			for i, id := range expected {
				if g.batches[i] != id {
					t.Fatal("lost/reordered passage")
				}
			}
		})
	}
}
func TestReportInvalidReviewAndCancelledCallNeverPublish(t *testing.T) {
	for _, mode := range []string{"invalid", "cancel", "version", "credential", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, []string{"The retrieval method evaluates a held out dataset with fixed experimental conditions."})
			g := &workflowGateway{malformed: mode == "invalid"}
			f.s.Gateway = g
			r := submitReport(t, f, c, "fulltext")
			if mode != "invalid" {
				g.hook = func(req ModelRequest) error {
					switch mode {
					case "cancel":
						must(t, f.store.Cancel(context.Background(), f.u.ID, r.ID))
					case "version":
						must(t, f.db.Model(&paper.Paper{}).Where("id=?", *c.PaperID).Update("authors_json", json.RawMessage(`["Changed author"]`)).Error)
					case "credential":
						g.version = 2
					case "unknown":
						return &ModelError{Code: "transport_failed"}
					}
					return nil
				}
			}
			f.s.process(context.Background(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			if end.State == "completed" || len(messages) != 1 {
				t.Fatalf("unsafe completion %+v", end)
			}
			if mode == "unknown" && (end.State != "unknown" || len(g.calls) != 1) {
				t.Fatal(end)
			}
		})
	}
}
func TestReportRestartsReuseSavedCallsAndUnknownIsNotReplayed(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, []string{"A retrieval experiment evaluates a held out dataset with explicitly reported conditions."})
			g := &workflowGateway{}
			f.s.Gateway = g
			r := submitReport(t, f, c, "fulltext")
			r = f.claim(t, r.ID)
			cp := Checkpoint{Phase: "ready"}
			check := func(context.Context) error { return nil }
			_, evidence, err := f.s.preparePaper(context.Background(), r, c, &cp, check)
			must(t, err)
			_, err = f.s.paperCall(context.Background(), r, &cp, check, "analyzing_problem", fieldPrompt, paperInput(cp.Paper, "problem", evidence), func(raw []byte) error { _, err := decodeField(raw, evidence); return err })
			must(t, err)
			if uncertain {
				cp.Phase = "calling"
				must(t, f.store.Save(context.Background(), r, cp, "analyzing_method", nil))
			}
			must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
			f.s.process(context.Background(), f.claim(t, r.ID))
			end, _ := paperOutcome(t, f, c, r)
			expected := 6
			if uncertain {
				expected = 1
				if end.State != "unknown" {
					t.Fatal(end)
				}
			} else if end.State != "completed" {
				t.Fatal(end)
			}
			if len(g.calls) != expected {
				t.Fatalf("replayed paid call %d", len(g.calls))
			}
		})
	}
}

type failingDocuments struct{ document.Store }

func (s failingDocuments) Ensure(ctx context.Context, src document.Source) (document.Document, error) {
	d, err := s.Store.Ensure(ctx, src)
	d.State = "failed"
	d.FailureCode = "ocr_required"
	return d, err
}
func TestScanFallbackIsPersistedAndFollowupStaysAbstract(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	f.s.Documents = failingDocuments{f.s.Documents}
	g := &workflowGateway{}
	f.s.Gateway = g
	r := submitReport(t, f, c, "fulltext")
	f.s.process(context.Background(), f.claim(t, r.ID))
	end, messages := paperOutcome(t, f, c, r)
	if end.State != "completed" || end.EffectiveContextMode != "abstract" || end.FallbackReason != "ocr_required" || len(messages) != 2 {
		t.Fatal(end)
	}
	next, err := f.s.Submit(context.Background(), f.u.ID, c.ID, SubmitInput{Question: "方法？", Provider: "glm", Model: "glm-4.7-flash", IdempotencyKey: rand.Text()})
	must(t, err)
	f.s.process(context.Background(), f.claim(t, next.ID))
	follow, _ := paperOutcome(t, f, c, next)
	if follow.State != "completed" || follow.EffectiveContextMode != "abstract" || len(g.calls) != 9 {
		t.Fatal(follow)
	}
}
func TestReportInputIdentityAndTaskValidation(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, []string{"A retrieval method with enough evidence."})
	key := rand.Text()
	input := SubmitInput{Task: TaskPaperReport, Provider: "glm", Model: "glm-4.7-flash", IdempotencyKey: key}
	r, err := f.s.Submit(context.Background(), f.u.ID, c.ID, input)
	must(t, err)
	again, err := f.s.Submit(context.Background(), f.u.ID, c.ID, input)
	must(t, err)
	if r.ID != again.ID || r.Question != PaperGoal {
		t.Fatal("report identity changed")
	}
	input.Task = TaskPaperFollowup
	input.Question = "question"
	if _, err = f.s.Submit(context.Background(), f.u.ID, c.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	input.Task = "execute_tool"
	if _, err = f.s.Submit(context.Background(), f.u.ID, c.ID, input); !errors.Is(err, ErrInput) {
		t.Fatal(err)
	}
}

func TestPaperReportHTTPAndResultContract(t *testing.T) {
	f := newFixture(t)
	c, doc := workflowPaper(t, f, []string{"This paper studies retrieval methods on a held out dataset with reported experimental conditions."})
	f.s.Gateway = &workflowGateway{}
	router := gin.New()
	router.Use(func(ctx *gin.Context) { httpx.SetCurrentUserID(ctx, httpx.UserID(f.u.ID)); ctx.Next() })
	h := Handler{Service: f.s}
	router.POST("/api/v2/agent/conversations/:id/messages", h.Handle)
	router.GET("/api/v2/agent/conversations/:id/messages", h.Handle)
	router.GET("/api/v2/agent/runs/:id", h.Handle)
	send := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		return recorder
	}
	input := `{"task":"paper_report","provider":"glm","model":"glm-4.7-flash","idempotency_key":"contract-report"}`
	var response struct {
		Run   Run    `json:"run"`
		RunID string `json:"run_id"`
	}
	decodeAgentHTTPJSON(t, send("POST", "/api/v2/agent/conversations/"+c.ID+"/messages", input), 202, &response)
	if response.RunID == "" || response.RunID != response.Run.ID || response.Run.ConversationID != c.ID || response.Run.Task != TaskPaperReport || response.Run.WorkflowVersion != PaperWorkflowVersion || response.Run.State != "pending" || response.Run.ContextMode != "fulltext" {
		t.Fatalf("invalid accepted paper report: %+v", response)
	}
	f.s.process(context.Background(), f.claim(t, response.Run.ID))
	var outcome struct {
		Run   Run    `json:"run"`
		Steps []Step `json:"steps"`
	}
	decodeAgentHTTPJSON(t, send("GET", "/api/v2/agent/runs/"+response.RunID, ""), 200, &outcome)
	if outcome.Run.ID != response.RunID || outcome.Run.ConversationID != c.ID || outcome.Run.Task != TaskPaperReport || outcome.Run.WorkflowVersion != PaperWorkflowVersion || outcome.Run.State != "completed" || outcome.Run.EffectiveContextMode != "fulltext" || outcome.Run.FailureCode != "" || len(outcome.Steps) != 6 {
		t.Fatalf("invalid completed report response: %+v", outcome)
	}
	for i, step := range outcome.Steps {
		if step.RunID != response.RunID || step.Sequence != i+1 || step.Kind != "model" || step.CallID == "" || step.FailureCode != "" {
			t.Fatalf("invalid report step: %+v", step)
		}
	}
	var history struct {
		Items      []Message `json:"items"`
		NextBefore uint64    `json:"next_before"`
	}
	decodeAgentHTTPJSON(t, send("GET", "/api/v2/agent/conversations/"+c.ID+"/messages", ""), 200, &history)
	if len(history.Items) != 2 || history.NextBefore != 0 {
		t.Fatalf("invalid report message page: %+v", history)
	}
	for _, message := range history.Items {
		if message.ID == 0 || message.ConversationID != c.ID || message.RunID != response.RunID || message.Provider != "glm" || message.Model != "glm-4.7-flash" || message.CreatedAt.IsZero() {
			t.Fatalf("invalid report message identity: %+v", message)
		}
	}
	question, answer := history.Items[0], history.Items[1]
	if question.Role != "user" || question.Content != PaperReportMessage || answer.Role != "assistant" || answer.ID <= question.ID || answer.Content == "" {
		t.Fatalf("invalid report message ordering or content: %+v", history.Items)
	}
	var result PaperResult
	must(t, json.Unmarshal(answer.Result, &result))
	if result.Report == nil || result.WorkflowVersion != PaperWorkflowVersion || result.ContextMode != "fulltext" || result.Coverage != "all_extracted_text" || result.DocumentID != doc.ID || result.ContentHash != "fixture-content-hash" || result.SourceVersion != "1706.03762v1" || len(result.PaperHash) != 64 || len(result.Fields) != len(paperFields) {
		t.Fatalf("invalid structured paper result: %+v", result)
	}
	for _, text := range []string{result.Report.Problem, result.Report.Method, result.Report.Experiments, result.Report.Results, result.Report.Limitations} {
		if strings.TrimSpace(text) == "" {
			t.Fatal("report field is missing from the HTTP result")
		}
	}
	var citations []Citation
	must(t, json.Unmarshal(answer.Citations, &citations))
	refs := make(map[string]Citation, len(citations))
	for _, citation := range citations {
		if citation.ID == "" || citation.DocumentID != result.DocumentID || citation.ContentHash != result.ContentHash || citation.Page != 1 || citation.Quote == "" || citation.URL != "https://arxiv.org/pdf/1706.03762v1#page=1" || !strings.Contains(answer.Content, "["+citation.ID+"]") {
			t.Fatalf("citation is not bound to the published report: %+v", citation)
		}
		if _, duplicate := refs[citation.ID]; duplicate {
			t.Fatalf("duplicate citation ID %q", citation.ID)
		}
		refs[citation.ID] = citation
	}
	for _, field := range paperFields {
		value, ok := result.Fields[field]
		if !ok || value.Status != "supported" || len(value.CitationIDs) == 0 {
			t.Fatalf("missing supported report field %q: %+v", field, value)
		}
		for _, id := range value.CitationIDs {
			if _, ok := refs[id]; !ok {
				t.Fatalf("report field %q refers to missing citation %q", field, id)
			}
		}
	}
}

func TestFollowupRequiresCurrentCompletedReport(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c, _ := workflowPaper(t, f, []string{"This paper evaluates a retrieval method using a held out evaluation dataset."})
	gateway := &workflowGateway{}
	f.s.Gateway = gateway
	question := SubmitInput{Question: "论文解决什么问题？", Provider: "glm", Model: "glm-4.7-flash", IdempotencyKey: rand.Text()}
	for _, task := range []string{"", TaskPaperFollowup} {
		question.Task = task
		if _, err := f.s.Submit(ctx, f.u.ID, c.ID, question); !errors.Is(err, ErrReportRequired) {
			t.Fatalf("ungated task %q: %v", task, err)
		}
	}
	if len(gateway.calls) != 0 {
		t.Fatal("rejected question invoked model")
	}
	current, err := f.s.Conversation(ctx, f.u.ID, c.ID)
	must(t, err)
	if current.PaperReportReady {
		t.Fatal("new conversation unlocked")
	}
	failed := submitReport(t, f, c, "fulltext")
	gateway.malformed = true
	f.s.process(ctx, f.claim(t, failed.ID))
	current, err = f.s.Conversation(ctx, f.u.ID, c.ID)
	must(t, err)
	if current.PaperReportReady {
		t.Fatal("failed report unlocked chat")
	}
	question.IdempotencyKey = rand.Text()
	if _, err = f.s.Submit(ctx, f.u.ID, c.ID, question); !errors.Is(err, ErrReportRequired) {
		t.Fatal(err)
	}
	cancelled := submitReport(t, f, c, "fulltext")
	must(t, f.store.Cancel(ctx, f.u.ID, cancelled.ID))
	current, err = f.s.Conversation(ctx, f.u.ID, c.ID)
	must(t, err)
	if current.PaperReportReady {
		t.Fatal("cancelled report unlocked chat")
	}
	gateway.malformed = false
	report := submitReport(t, f, c, "fulltext")
	f.s.process(ctx, f.claim(t, report.ID))
	current, err = f.s.Conversation(ctx, f.u.ID, c.ID)
	must(t, err)
	if !current.PaperReportReady {
		t.Fatal("completed report did not unlock chat")
	}
	// Successful reports remain discoverable beyond prompt history and UI pages.
	for i := 0; i < 55; i++ {
		old := report
		old.ID = rand.Text()
		old.IdempotencyKey = rand.Text()
		old.Task = TaskPaperFollowup
		old.State = "completed"
		must(t, f.db.Create(&old).Error)
		must(t, f.db.Create(&Message{ConversationID: c.ID, RunID: old.ID, Role: "assistant", Content: "Previous follow-up", Citations: json.RawMessage(`[]`), CreatedAt: time.Now().UTC()}).Error)
	}
	current, err = f.s.Conversation(ctx, f.u.ID, c.ID)
	must(t, err)
	if !current.PaperReportReady {
		t.Fatal("pagination hid prerequisite report")
	}
	next, err := f.s.Submit(ctx, f.u.ID, c.ID, question)
	must(t, err)
	must(t, f.store.Cancel(ctx, f.u.ID, next.ID))
	another, err := f.s.CreateConversation(ctx, f.u.ID, "paper", c.PaperID)
	must(t, err)
	if _, err = f.s.Submit(ctx, f.u.ID, another.ID, question); !errors.Is(err, ErrReportRequired) {
		t.Fatal("report leaked across conversations", err)
	}
	must(t, f.db.Model(&paper.Paper{}).Where("id=?", *c.PaperID).Update("title", "Updated paper").Error)
	current, err = f.s.Conversation(ctx, f.u.ID, c.ID)
	must(t, err)
	if current.PaperReportReady {
		t.Fatal("stale report unlocked chat")
	}
	question.IdempotencyKey = rand.Text()
	if _, err = f.s.Submit(ctx, f.u.ID, c.ID, question); !errors.Is(err, ErrReportRequired) {
		t.Fatal("stale report accepted", err)
	}
}

func TestFollowupHTTPRejectsBeforeAnyReport(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	router := gin.New()
	router.Use(func(ctx *gin.Context) { httpx.SetCurrentUserID(ctx, httpx.UserID(f.u.ID)); ctx.Next() })
	router.POST("/api/v2/agent/conversations/:id/messages", Handler{Service: f.s}.Handle)
	request := httptest.NewRequest("POST", "/api/v2/agent/conversations/"+c.ID+"/messages", strings.NewReader(`{"question":"Explain this paper","provider":"glm","model":"glm-4.7-flash","idempotency_key":"no-report-yet"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != 409 || !strings.Contains(recorder.Body.String(), "PAPER_REPORT_REQUIRED") {
		t.Fatalf("%d %s", recorder.Code, recorder.Body.String())
	}
	messages, _, err := f.store.Messages(context.Background(), f.u.ID, c.ID, 0)
	must(t, err)
	if len(messages) != 0 {
		t.Fatal("blocked question created a message")
	}
}

func TestReportPersistsFailedStageAndRuleWithoutPublishing(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, []string{"This paper studies retrieval methods with a held out evaluation dataset."})
	g := &workflowGateway{hook: func(ModelRequest) error {
		return outputError("evidence_id_unknown", "$.claims[0].evidence[0].id")
	}}
	f.s.Gateway = g
	r := submitReport(t, f, c, "fulltext")
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, messages := paperOutcome(t, f, c, r)
	steps, err := f.store.Steps(t.Context(), f.u.ID, r.ID)
	must(t, err)
	if end.State != "failed" || end.FailureCode != "evidence_id_unknown" || len(messages) != 1 || len(g.calls) != 1 || len(steps) != 1 || steps[0].Tool != "analyzing_problem" || steps[0].FailureCode != end.FailureCode {
		t.Fatalf("run=%+v steps=%+v messages=%d calls=%d", end, steps, len(messages), len(g.calls))
	}
}

func TestPaperProtocolUpgradeDoesNotReplayOldCheckpoints(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, []string{"This paper studies retrieval methods with a held out evaluation dataset."})
	g := &workflowGateway{}
	f.s.Gateway = g
	r := submitReport(t, f, c, "fulltext")
	must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("workflow_version", "paper-fixed-v4").Error)
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, messages := paperOutcome(t, f, c, r)
	if end.FailureCode != "workflow_changed" || len(messages) != 1 || len(g.calls) != 0 {
		t.Fatalf("old protocol replayed: %+v", end)
	}
}

func TestPaperProtocolUpgradePreservesCompletedReportAndFollowup(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, []string{"This paper studies retrieval methods with a held out evaluation dataset."})
	g := &workflowGateway{}
	f.s.Gateway = g
	r := submitReport(t, f, c, "fulltext")
	f.s.process(t.Context(), f.claim(t, r.ID))
	_, messages := paperOutcome(t, f, c, r)
	message := messages[len(messages)-1]
	var result PaperResult
	must(t, json.Unmarshal(message.Result, &result))
	result.WorkflowVersion = "paper-fixed-v1"
	raw, _ := json.Marshal(result)
	must(t, f.db.Model(&Message{}).Where("id=?", message.ID).Update("result", raw).Error)
	must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("workflow_version", "paper-fixed-v1").Error)
	visible, err := f.s.Conversation(t.Context(), f.u.ID, c.ID)
	must(t, err)
	if !visible.PaperReportReady {
		t.Fatal("upgrade hid the completed report")
	}
	followup, err := f.s.Submit(t.Context(), f.u.ID, c.ID, SubmitInput{Task: TaskPaperFollowup, Question: "论文如何实验？", Provider: "glm", Model: "glm-4.7-flash", IdempotencyKey: rand.Text(), ContextMode: "fulltext"})
	must(t, err)
	f.s.process(t.Context(), f.claim(t, followup.ID))
	end, history := paperOutcome(t, f, c, followup)
	if end.State != "completed" || len(g.calls) != 9 || len(history) != 4 || history[1].Content != message.Content {
		t.Fatalf("upgrade lost history or followup: state=%s calls=%d messages=%d", end.State, len(g.calls), len(history))
	}
}

func TestPaperSchemaFailureIsPersistedAcrossAPIReads(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, []string{"The paper proposes a method and evaluates it with a held out dataset."})
	calls := 0
	g := &workflowGateway{hook: func(req ModelRequest) error {
		calls++
		if req.Schema == nil {
			t.Fatal("missing fixed output contract")
		}
		if calls == 2 {
			return req.Validate(generation.Result{CallID: "schema-failed-call", Content: []byte(`{"status":"supported","claims":[{"text":"method","evidence":"not-an-array"}]}`)})
		}
		return nil
	}}
	f.s.Gateway = g
	r := submitReport(t, f, c, "abstract")
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, messages := paperOutcome(t, f, c, r)
	if end.State != "failed" || calls != 2 || len(messages) != 1 || end.FailureDetail == nil {
		t.Fatalf("state=%s calls=%d detail=%+v", end.State, calls, end.FailureDetail)
	}
	detail := end.FailureDetail
	if detail.Code != "output_schema_mismatch" || detail.Path != "$.claims[0].evidence" || detail.Rule != "expected_array" {
		t.Fatalf("%+v", detail)
	}
	again, err := f.s.RunByID(t.Context(), f.u.ID, r.ID)
	must(t, err)
	if again.FailureDetail == nil || *again.FailureDetail != *detail {
		t.Fatal("diagnostic lost on refresh")
	}
	var cp Checkpoint
	must(t, json.Unmarshal(again.Checkpoint, &cp))
	if _, ok := cp.Paper.Outputs["analyzing_method"]; ok {
		t.Fatal("invalid output saved as completed step")
	}
}

func TestEnglishReportClaimFailsBeforePublicationWithoutRetry(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, []string{"Patch validation used a patch-level rather than patient-level split."})
	calls := 0
	g := &workflowGateway{hook: func(req ModelRequest) error {
		calls++
		if calls != 2 {
			return nil
		}
		var input struct {
			Evidence []Citation `json:"evidence"`
		}
		must(t, json.Unmarshal(req.Input, &input))
		raw, _ := json.Marshal(fieldOutput{Status: "supported", Claims: []claimOutput{
			{Text: "模型采用三维网络。", Evidence: []evidenceOutput{{ID: input.Evidence[0].ID}}},
			{Text: "Patch validation used a patch-level rather than patient-level split.", Evidence: []evidenceOutput{{ID: input.Evidence[0].ID}}},
		}})
		return req.Validate(generation.Result{Content: raw})
	}}
	f.s.Gateway = g
	r := submitReport(t, f, c, "fulltext")
	f.s.process(t.Context(), f.claim(t, r.ID))
	end, messages := paperOutcome(t, f, c, r)
	if end.State != "failed" || end.FailureCode != "output_language_mismatch" || calls != 2 || len(messages) != 1 || end.FailureDetail == nil || end.FailureDetail.Path != "$.claims[1].text" || end.FailureDetail.Rule != "chinese_text_required" {
		t.Fatalf("state=%s code=%s calls=%d detail=%+v", end.State, end.FailureCode, calls, end.FailureDetail)
	}
}

func TestCompletedReportCompatibilityIncludesPreviousLanguagePolicy(t *testing.T) {
	for _, version := range []string{"paper-fixed-v1", "paper-fixed-v2", "paper-fixed-v3", "paper-fixed-v4", PaperWorkflowVersion} {
		raw, _ := json.Marshal(PaperResult{Report: &PaperReport{Problem: "An existing English report."}, PaperHash: "hash", ContextMode: "fulltext", WorkflowVersion: version})
		if _, ok := validPaperReport(Message{Result: raw}, "hash"); !ok {
			t.Fatal("historical report hidden:", version)
		}
	}
}

func TestReportMessageLabelDoesNotChangeGoalHistoryOrFollowups(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, []string{"The paper describes a retrieval method."})
	f.s.Gateway = &workflowGateway{}
	input := SubmitInput{Task: TaskPaperReport, Provider: "glm", Model: "glm-4.7-flash", ContextMode: "fulltext", IdempotencyKey: rand.Text()}
	r, err := f.s.Submit(t.Context(), f.u.ID, c.ID, input)
	must(t, err)
	again, err := f.s.Submit(t.Context(), f.u.ID, c.ID, input)
	must(t, err)
	messages, _, err := f.store.Messages(t.Context(), f.u.ID, c.ID, 0)
	must(t, err)
	if again.ID != r.ID || again.InputHash != r.InputHash || r.Question != PaperGoal || len(messages) != 1 || messages[0].Content != "快速了解论文" {
		t.Fatal("message label changed execution or idempotency")
	}
	// Simulate an existing historical message; idempotent reads must not rewrite it.
	must(t, f.db.Model(&Message{}).Where("id=?", messages[0].ID).Update("content", PaperGoal).Error)
	f.s.process(t.Context(), f.claim(t, r.ID))
	_, err = f.s.Submit(t.Context(), f.u.ID, c.ID, input)
	must(t, err)
	_, err = f.s.Submit(t.Context(), f.u.ID, c.ID, SubmitInput{Task: TaskPaperFollowup, Question: PaperGoal, Provider: input.Provider, Model: input.Model, ContextMode: input.ContextMode, IdempotencyKey: rand.Text()})
	must(t, err)
	messages, _, err = f.store.Messages(t.Context(), f.u.ID, c.ID, 0)
	must(t, err)
	if len(messages) != 3 || messages[0].Content != PaperGoal || messages[2].Content != PaperGoal {
		t.Fatal("rewrote history or ordinary followup")
	}
}
