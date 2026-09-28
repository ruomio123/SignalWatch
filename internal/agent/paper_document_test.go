package agent

import (
	"encoding/json"
	"net/http/httptest"
	"signalwatch/internal/document"
	"signalwatch/internal/paper"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func paperDocumentRouter(f *fixture, uid uint64) *gin.Engine {
	router := round1Router(f, uid)
	h := Handler{Service: f.s}
	router.GET("/api/v2/agent/papers/:id/document", h.Handle)
	router.POST("/api/v2/agent/papers/:id/document/prepare", h.Handle)
	return router
}

func paperDocumentPOST(router *gin.Engine, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("POST", path+"/prepare", nil))
	return response
}

func decodePaperDocumentHTTP(t *testing.T, response *httptest.ResponseRecorder, status int) PaperDocumentStatus {
	t.Helper()
	var value PaperDocumentStatus
	decodeAgentHTTPJSON(t, response, status, &value)
	var fields map[string]json.RawMessage
	must(t, json.Unmarshal(response.Body.Bytes(), &fields))
	for key := range fields {
		switch key {
		case "state", "usable", "document_id", "source_version", "parser_version", "page_count", "updated_at", "failure_code":
		default:
			t.Fatalf("unexpected document field %q", key)
		}
	}
	for _, key := range []string{"state", "usable", "document_id", "source_version", "parser_version", "page_count"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing document field %q", key)
		}
	}
	return value
}

func TestPaperDocumentHTTPReadOnlyStatusAndExplicitPreparation(t *testing.T) {
	f := newFixture(t)
	c, initial := workflowPaper(t, f, nil)
	must(t, f.db.Delete(&document.Document{}, "id=?", initial.ID).Error)
	// A status lookup or preparation cannot depend on model selection or calls.
	f.s.Gateway = nil
	router := paperDocumentRouter(f, f.u.ID)
	path := "/api/v2/agent/papers/" + jsonNumber(*c.PaperID) + "/document"
	for i := 0; i < 2; i++ {
		status := decodePaperDocumentHTTP(t, round1GET(router, path), 200)
		if status.State != "not_prepared" || status.Usable || status.DocumentID != initial.ID || status.SourceVersion != "" || status.ParserVersion != document.ParserVersion || status.PageCount != 0 || status.UpdatedAt != nil || status.FailureCode != "" {
			t.Fatalf("unexpected unprepared status: %+v", status)
		}
	}
	var count int64
	must(t, f.db.Model(&document.Document{}).Where("paper_id=?", *c.PaperID).Count(&count).Error)
	if count != 0 {
		t.Fatal("GET created a document")
	}
	responses := make(chan *httptest.ResponseRecorder, 8)
	var wg sync.WaitGroup
	for i := 0; i < cap(responses); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- paperDocumentPOST(router, path)
		}()
	}
	wg.Wait()
	close(responses)
	for response := range responses {
		status := decodePaperDocumentHTTP(t, response, 202)
		if status.State != "pending" || status.Usable || status.DocumentID != initial.ID || status.UpdatedAt == nil {
			t.Fatalf("concurrent prepare changed identity: %+v", status)
		}
	}
	must(t, f.db.Model(&document.Document{}).Where("paper_id=?", *c.PaperID).Count(&count).Error)
	if count != 1 {
		t.Fatalf("concurrent prepares created %d document versions", count)
	}
	must(t, f.db.Model(&Run{}).Where("user_id=?", f.u.ID).Count(&count).Error)
	if count != 0 {
		t.Fatal("document API created an Agent/model run")
	}
	before := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	must(t, f.db.Model(&document.Document{}).Where("id=?", initial.ID).Updates(map[string]any{"state": "failed", "failure_code": "secret-worker-error", "updated_at": before}).Error)
	src, err := f.s.Documents.Source(t.Context(), *c.PaperID)
	must(t, err)
	ensured, err := f.s.Documents.Ensure(t.Context(), src)
	must(t, err)
	if ensured.State != "failed" || ensured.FailureCode != "secret-worker-error" || !ensured.UpdatedAt.Equal(before) {
		t.Fatalf("Ensure retried or rewrote a failed document: %+v", ensured)
	}
	failed := decodePaperDocumentHTTP(t, round1GET(router, path), 200)
	if failed.State != "failed" || failed.Usable || failed.FailureCode != "document_extraction_failed" {
		t.Fatalf("failed status leaked internal failure: %+v", failed)
	}
	unchanged, err := f.s.Documents.Get(t.Context(), initial.ID)
	must(t, err)
	if unchanged.State != "failed" || unchanged.FailureCode != "secret-worker-error" || !unchanged.UpdatedAt.Equal(before) {
		t.Fatal("GET rewrote a failed document")
	}
	retried := decodePaperDocumentHTTP(t, paperDocumentPOST(router, path), 202)
	if retried.State != "pending" || retried.FailureCode != "" {
		t.Fatalf("explicit retry not queued: %+v", retried)
	}
	retriedRow, err := f.s.Documents.Get(t.Context(), initial.ID)
	must(t, err)
	if retriedRow.FailureCode != "" {
		t.Fatal("retry retained the previous failure")
	}
	lease := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	must(t, f.db.Model(&document.Document{}).Where("id=?", initial.ID).Updates(map[string]any{"state": "processing", "epoch": 7, "lease_owner": "private-worker-owner", "lease_until": lease, "updated_at": before}).Error)
	processing := decodePaperDocumentHTTP(t, paperDocumentPOST(router, path), 202)
	if processing.State != "processing" || processing.Usable {
		t.Fatalf("processing document was restarted: %+v", processing)
	}
	processingRow, err := f.s.Documents.Get(t.Context(), initial.ID)
	must(t, err)
	if processingRow.Epoch != 7 || processingRow.LeaseOwner != "private-worker-owner" || processingRow.LeaseUntil == nil || !processingRow.LeaseUntil.Equal(lease) || !processingRow.UpdatedAt.Equal(before) {
		t.Fatal("prepare changed a running document's lease or timestamp")
	}
	must(t, f.db.Model(&document.Document{}).Where("id=?", initial.ID).Updates(map[string]any{"state": "ready", "text_complete": true, "content_hash": strings.Repeat("a", 64), "source_version": "1706.03762v1", "page_count": 2, "updated_at": before}).Error)
	for _, response := range []*httptest.ResponseRecorder{round1GET(router, path), paperDocumentPOST(router, path)} {
		ready := decodePaperDocumentHTTP(t, response, 200)
		if ready.State != "ready" || !ready.Usable || ready.DocumentID != initial.ID || ready.SourceVersion != "1706.03762v1" || ready.PageCount != 2 || ready.UpdatedAt == nil || !ready.UpdatedAt.Equal(before) {
			t.Fatalf("ready document was not reused: %+v", ready)
		}
	}
	must(t, f.db.Model(&document.Document{}).Where("id=?", initial.ID).Update("text_complete", false).Error)
	incomplete := decodePaperDocumentHTTP(t, round1GET(router, path), 200)
	if incomplete.State != "ready" || incomplete.Usable {
		t.Fatalf("incomplete text was advertised as usable: %+v", incomplete)
	}
}

func TestPaperDocumentHTTPUsesCurrentVersionAndChecksAccess(t *testing.T) {
	f := newFixture(t)
	c, old := workflowPaper(t, f, []string{"An existing complete document with extracted paper content."})
	router := paperDocumentRouter(f, f.u.ID)
	path := "/api/v2/agent/papers/" + jsonNumber(*c.PaperID) + "/document"
	var p paper.Paper
	must(t, f.db.Where("id=?", *c.PaperID).Take(&p).Error)
	must(t, f.db.Model(&p).Update("arxiv_updated_at", p.ArXivUpdatedAt.Add(time.Hour)).Error)
	status := decodePaperDocumentHTTP(t, round1GET(router, path), 200)
	if status.State != "not_prepared" || status.Usable || status.DocumentID == old.ID {
		t.Fatalf("new paper version reused an old document: %+v", status)
	}
	var count int64
	must(t, f.db.Model(&document.Document{}).Where("paper_id=?", *c.PaperID).Count(&count).Error)
	if count != 1 {
		t.Fatal("GET prepared the new version")
	}
	prepared := decodePaperDocumentHTTP(t, paperDocumentPOST(router, path), 202)
	if prepared.State != "pending" || prepared.DocumentID != status.DocumentID {
		t.Fatalf("new version preparation identity changed: %+v", prepared)
	}
	previous, err := f.s.Documents.Get(t.Context(), old.ID)
	must(t, err)
	if previous.State != "ready" || !previous.TextComplete {
		t.Fatal("preparation changed the historical ready version")
	}
	must(t, f.db.Model(&document.Document{}).Where("id=?", prepared.DocumentID).Updates(map[string]any{"state": "failed", "failure_code": "ocr_required"}).Error)
	other := newFixture(t)
	otherRouter := paperDocumentRouter(f, other.u.ID)
	var failure map[string]any
	for _, response := range []*httptest.ResponseRecorder{round1GET(otherRouter, path), paperDocumentPOST(otherRouter, path)} {
		decodeAgentHTTPJSON(t, response, 404, &failure)
	}
	must(t, f.db.Where("paper_id=?", *c.PaperID).Delete(&paper.SubscriptionPaper{}).Error)
	for _, response := range []*httptest.ResponseRecorder{round1GET(router, path), paperDocumentPOST(router, path)} {
		decodeAgentHTTPJSON(t, response, 404, &failure)
	}
	must(t, f.db.Model(&document.Document{}).Where("paper_id=?", *c.PaperID).Count(&count).Error)
	if count != 2 {
		t.Fatal("unauthorized requests changed the prepared versions")
	}
	deniedRow, err := f.s.Documents.Get(t.Context(), prepared.DocumentID)
	must(t, err)
	if deniedRow.State != "failed" || deniedRow.FailureCode != "ocr_required" {
		t.Fatal("unauthorized preparation retried a document before checking access")
	}
}

func TestPaperDocumentFailureCodesAndParserCompatibility(t *testing.T) {
	for _, code := range []string{"document_version_unavailable", "document_download_failed", "invalid_pdf", "document_extraction_failed", "text_extraction_failed", "document_resource_limit", "ocr_required", "ocr_failed", "document_timeout"} {
		if status := publicPaperDocument(document.Document{State: "failed", FailureCode: code}); status.FailureCode != code || status.Usable {
			t.Fatalf("supported failure code changed: %+v", status)
		}
	}
	for _, code := range []string{"", "private-parser-output"} {
		if status := publicPaperDocument(document.Document{State: "failed", FailureCode: code}); status.FailureCode != "document_extraction_failed" {
			t.Fatalf("unknown failure code exposed: %+v", status)
		}
	}
	status := publicPaperDocument(document.Document{State: "ready", TextComplete: true, ParserVersion: "outdated-parser", FailureCode: "private-parser-output"})
	if status.Usable || status.FailureCode != "" {
		t.Fatalf("old parser or stale failure was exposed as usable: %+v", status)
	}
}
