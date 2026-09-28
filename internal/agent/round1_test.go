package agent

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"signalwatch/internal/paper"
	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/subscription"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func round1Router(f *fixture, uid uint64) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) { httpx.SetCurrentUserID(c, httpx.UserID(uid)); c.Next() })
	h := Handler{Service: f.s}
	router.GET("/api/v2/agent/conversations", h.Handle)
	router.GET("/api/v2/agent/conversations/:id/messages", h.Handle)
	router.GET("/api/v2/agent/conversations/:id/paper-report", h.Handle)
	return router
}

func round1GET(router *gin.Engine, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
	return response
}

type round1MessagesPage struct {
	Items      []Message `json:"items"`
	NextBefore uint64    `json:"next_before"`
}

type round1ConversationsPage struct {
	Items    []Conversation `json:"items"`
	Page     int            `json:"page"`
	HasMore  bool           `json:"has_more"`
	NextPage int            `json:"next_page"`
}

// These historical rows isolate pagination from the model workflow and never
// enter the dispatcher. Each message has a real, completed owning run.
func round1SeedMessages(t *testing.T, f *fixture, c Conversation, count int) []Message {
	t.Helper()
	now := time.Now().UTC()
	runs := make([]Run, count)
	messages := make([]Message, count)
	for i := range runs {
		r := Run{ID: rand.Text(), ConversationID: c.ID, UserID: c.UserID, IdempotencyKey: rand.Text(), InputHash: strings.Repeat("0", 64), Question: "historical question", Provider: "glm", Model: "glm-4.7-flash", Generation: "agent-fixture", Version: 1, ContextMode: "abstract", State: "completed", Checkpoint: json.RawMessage(`{"phase":"ready"}`), CreatedAt: now, UpdatedAt: now}
		if c.Kind == "paper" {
			r.Task, r.WorkflowVersion = TaskPaperFollowup, PaperWorkflowVersion
		}
		runs[i] = r
		messages[i] = Message{ConversationID: c.ID, RunID: r.ID, Role: "assistant", Content: fmt.Sprintf("historical answer %d", i), Provider: r.Provider, Model: r.Model, Citations: json.RawMessage(`[]`), CreatedAt: now}
	}
	must(t, f.db.CreateInBatches(runs, 50).Error)
	must(t, f.db.CreateInBatches(&messages, 50).Error)
	return messages
}

func TestRound1MessagePaginationBoundariesAndConcurrentInsert(t *testing.T) {
	for _, count := range []int{50, 51} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := newFixture(t)
			c, err := f.s.CreateConversation(t.Context(), f.u.ID, "subscription", nil)
			must(t, err)
			seeded := round1SeedMessages(t, f, c, count)
			router := round1Router(f, f.u.ID)
			path := "/api/v2/agent/conversations/" + c.ID + "/messages"
			var first round1MessagesPage
			decodeAgentHTTPJSON(t, round1GET(router, path), 200, &first)
			if len(first.Items) != 50 {
				t.Fatalf("first page has %d messages", len(first.Items))
			}
			for i, message := range first.Items {
				if message.ID != seeded[count-50+i].ID {
					t.Fatalf("message order or boundary wrong at %d: %d", i, message.ID)
				}
			}
			if count == 50 {
				if first.NextBefore != 0 {
					t.Fatalf("exactly 50 messages must end pagination, got %d", first.NextBefore)
				}
				return
			}
			if first.NextBefore != first.Items[0].ID {
				t.Fatalf("cursor skipped the lookahead row: %+v", first)
			}
			// Another request inserts a message between fetching the two pages.
			inserted := round1SeedMessages(t, f, c, 1)[0]
			var older round1MessagesPage
			decodeAgentHTTPJSON(t, round1GET(router, path+"?before="+jsonNumber(first.NextBefore)), 200, &older)
			if len(older.Items) != 1 || older.Items[0].ID != seeded[0].ID || older.NextBefore != 0 {
				t.Fatalf("older page shifted after insert: %+v", older)
			}
			seen := map[uint64]bool{}
			for _, message := range append(older.Items, first.Items...) {
				if seen[message.ID] || message.ID == inserted.ID {
					t.Fatal("history page repeated a message or included a newer insert")
				}
				seen[message.ID] = true
			}
			if len(seen) != count {
				t.Fatalf("pagination lost messages: %d", len(seen))
			}
			var newest round1MessagesPage
			decodeAgentHTTPJSON(t, round1GET(router, path), 200, &newest)
			if newest.Items[len(newest.Items)-1].ID != inserted.ID {
				t.Fatal("refresh did not include the concurrent insert")
			}
		})
	}
}

func TestRound1ConversationPaginationFiltersBeforeLimitWithoutDuplicates(t *testing.T) {
	for _, count := range []int{20, 21} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := newFixture(t)
			visible, _ := workflowPaper(t, f, nil)
			hidden, _ := workflowPaper(t, f, nil)
			var extraMatch paper.SubscriptionPaper
			must(t, f.db.Where("paper_id=?", *hidden.PaperID).Take(&extraMatch).Error)
			// Two subscriptions grant access to the same paper: a JOIN must not
			// turn one conversation into two rows.
			must(t, f.db.Create(&paper.SubscriptionPaper{SubscriptionID: extraMatch.SubscriptionID, PaperID: *visible.PaperID, MatchedKeywordsJSON: json.RawMessage(`[]`), MatchedAt: time.Now().UTC()}).Error)
			must(t, f.db.Where("paper_id=?", *hidden.PaperID).Delete(&paper.SubscriptionPaper{}).Error)
			now := time.Now().UTC()
			must(t, f.db.Model(&subscription.Subscription{}).Where("user_id=?", f.u.ID).Updates(map[string]any{"enabled": false, "deleted_at": now}).Error)
			if _, err := f.s.Papers.Get(t.Context(), f.u.ID, *visible.PaperID); err != nil {
				t.Fatalf("historical paper access changed: %v", err)
			}
			want := map[string]bool{visible.ID: true}
			for i := 1; i < count; i++ {
				c := visible
				c.ID, c.UpdatedAt = rand.Text(), now.Add(time.Duration(i)*time.Millisecond)
				must(t, f.store.CreateConversation(t.Context(), c))
				want[c.ID] = true
			}
			// Invisible records occupy the newest positions and would consume
			// the page if authorization were applied after OFFSET/LIMIT.
			for i := 0; i < 3; i++ {
				c := hidden
				c.ID, c.UpdatedAt = rand.Text(), now.Add(time.Hour)
				must(t, f.store.CreateConversation(t.Context(), c))
			}
			other := newFixture(t)
			foreign := visible
			foreign.ID, foreign.UserID, foreign.UpdatedAt = rand.Text(), other.u.ID, now.Add(time.Hour)
			must(t, f.store.CreateConversation(t.Context(), foreign))
			router := round1Router(f, f.u.ID)
			var first round1ConversationsPage
			decodeAgentHTTPJSON(t, round1GET(router, "/api/v2/agent/conversations?kind=paper"), 200, &first)
			if len(first.Items) != 20 || first.Page != 1 || first.HasMore != (count == 21) || first.NextPage != map[bool]int{false: 0, true: 2}[count == 21] {
				t.Fatalf("incorrect first page: %+v", first)
			}
			var second round1ConversationsPage
			decodeAgentHTTPJSON(t, round1GET(router, "/api/v2/agent/conversations?kind=paper&page=2"), 200, &second)
			if len(second.Items) != count-20 || second.Page != 2 || second.HasMore || second.NextPage != 0 {
				t.Fatalf("incorrect last page: %+v", second)
			}
			seen := map[string]bool{}
			for _, c := range append(first.Items, second.Items...) {
				if !want[c.ID] || seen[c.ID] {
					t.Fatalf("unowned, invisible, or repeated conversation: %s", c.ID)
				}
				seen[c.ID] = true
			}
			if len(seen) != len(want) {
				t.Fatalf("lost visible conversations: got %d, want %d", len(seen), len(want))
			}
			var filtered round1ConversationsPage
			decodeAgentHTTPJSON(t, round1GET(router, "/api/v2/agent/conversations?paper_id="+jsonNumber(*hidden.PaperID)), 200, &filtered)
			if len(filtered.Items) != 0 || filtered.HasMore || filtered.NextPage != 0 {
				t.Fatalf("paper filter bypassed visibility: %+v", filtered)
			}
			sub, err := f.s.CreateConversation(t.Context(), f.u.ID, "subscription", nil)
			must(t, err)
			var subscriptions round1ConversationsPage
			decodeAgentHTTPJSON(t, round1GET(router, "/api/v2/agent/conversations?kind=subscription"), 200, &subscriptions)
			if len(subscriptions.Items) != 1 || subscriptions.Items[0].ID != sub.ID || subscriptions.HasMore || subscriptions.NextPage != 0 {
				t.Fatalf("subscription conversations lost: %+v", subscriptions)
			}
		})
	}
}

func TestRound1PaperReportIsIndependentOfMessagesAndRechecksVisibility(t *testing.T) {
	f := newFixture(t)
	c, _ := workflowPaper(t, f, nil)
	router := round1Router(f, f.u.ID)
	path := "/api/v2/agent/conversations/" + c.ID + "/paper-report"
	var absent PaperReportResponse
	response := round1GET(router, path)
	decodeAgentHTTPJSON(t, response, 200, &absent)
	if absent.Report != nil || absent.MatchesCurrentPaper || !strings.Contains(response.Body.String(), `"report":null`) {
		t.Fatalf("missing report contract: %s", response.Body.String())
	}
	f.s.Gateway = &workflowGateway{}
	r := submitReport(t, f, c, "abstract")
	f.s.process(t.Context(), f.claim(t, r.ID))
	report, err := f.store.LatestPaperReport(t.Context(), c.ID)
	must(t, err)
	round1SeedMessages(t, f, c, 51)
	var messages round1MessagesPage
	decodeAgentHTTPJSON(t, round1GET(router, "/api/v2/agent/conversations/"+c.ID+"/messages"), 200, &messages)
	for _, message := range messages.Items {
		if message.ID == report.ID {
			t.Fatal("test report was not pushed outside the newest messages page")
		}
	}
	var current PaperReportResponse
	decodeAgentHTTPJSON(t, round1GET(router, path), 200, &current)
	if current.Report == nil || current.Report.ID != report.ID || current.Report.RunID != r.ID || !current.MatchesCurrentPaper {
		t.Fatalf("independent current report not returned: %+v", current)
	}
	must(t, f.db.Model(&paper.Paper{}).Where("id=?", *c.PaperID).Update("abstract", "The paper now has a different abstract.").Error)
	var stale PaperReportResponse
	decodeAgentHTTPJSON(t, round1GET(router, path), 200, &stale)
	if stale.Report == nil || stale.Report.ID != report.ID || stale.MatchesCurrentPaper {
		t.Fatalf("stale report must remain readable and be marked stale: %+v", stale)
	}
	other := newFixture(t)
	var failure map[string]any
	decodeAgentHTTPJSON(t, round1GET(round1Router(f, other.u.ID), path), 404, &failure)
	sub, err := f.s.CreateConversation(t.Context(), f.u.ID, "subscription", nil)
	must(t, err)
	decodeAgentHTTPJSON(t, round1GET(router, "/api/v2/agent/conversations/"+sub.ID+"/paper-report"), 404, &failure)
	must(t, f.db.Where("paper_id=?", *c.PaperID).Delete(&paper.SubscriptionPaper{}).Error)
	decodeAgentHTTPJSON(t, round1GET(router, path), 404, &failure)
}
