package ai

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

func TestMySQLRecreatedConfigurationCannotReuseTasksOrCommands(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	uid := f.users[0].ID
	before, err := f.configurations.Get(ctx, uid)
	mustAI(t, err)
	_, err = f.service.Summary(ctx, uid, f.papers[0].ID, "zh", true)
	mustAI(t, err)
	rows, err := f.service.repo.Pending(ctx, PaperKind, "", f.now, 10)
	mustAI(t, err)
	if len(rows) != 1 {
		t.Fatalf("tasks=%d", len(rows))
	}
	mustAI(t, f.configurations.Delete(ctx, uid, before.Version))
	after, err := f.configurations.Put(ctx, uid, before.ProviderID, before.ModelID, "new-test-key-4321", nil)
	mustAI(t, err)
	if after.Generation == before.Generation || after.Version <= before.Version {
		t.Fatal("configuration identity was reused")
	}
	if err := f.configurations.Delete(ctx, uid, before.Version); !errors.Is(err, ErrConfigurationConflict) {
		t.Fatalf("stale delete=%v", err)
	}
	mustAI(t, f.db.Table("users").Where("id=?", uid).Update("ai_enabled", true).Error)
	calls := f.calls.Load()
	f.service.Process(ctx, rows[0])
	if f.calls.Load() != calls {
		t.Fatal("obsolete task called provider")
	}
	response, err := f.service.Summary(ctx, uid, f.papers[0].ID, "zh", false)
	mustAI(t, err)
	if response.Items[0].State != "unavailable" {
		t.Fatalf("old task reused: %+v", response)
	}
}

func TestConfigurationHTTPRejectsETagFromDeletedLifecycle(t *testing.T) {
	f := newAIIntegrationFixture(t)
	uid := f.users[0].ID
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	router.Use(func(c *gin.Context) { httpx.SetCurrentUserID(c, httpx.UserID(uid)); c.Next() })
	h := Handler{Configurations: f.configurations, Service: f.service, Enabled: true}
	router.GET("/configuration", h.GetConfiguration)
	router.DELETE("/configuration", h.DeleteConfiguration)
	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/configuration", nil))
	validateAIJSON(t, get, "AIConfiguration")
	oldTag := get.Header().Get("ETag")
	var current PublicConfiguration
	mustAI(t, json.Unmarshal(get.Body.Bytes(), &current))
	if oldTag != configurationETag(current) {
		t.Fatal("ETag does not encode configuration identity")
	}
	req := httptest.NewRequest(http.MethodDelete, "/configuration", nil)
	req.Header.Set("If-Match", oldTag)
	deleted := httptest.NewRecorder()
	router.ServeHTTP(deleted, req)
	if deleted.Code != 204 {
		t.Fatalf("delete=%d", deleted.Code)
	}
	recreated, err := f.configurations.Put(t.Context(), uid, current.ProviderID, current.ModelID, "recreated-test-key-1234", nil)
	mustAI(t, err)
	stale := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/configuration", nil)
	req.Header.Set("If-Match", oldTag)
	router.ServeHTTP(stale, req)
	validateAIJSON(t, stale, "ErrorResponse")
	if stale.Code != 409 {
		t.Fatalf("stale lifecycle ETag accepted: %d", stale.Code)
	}
	saved, err := f.configurations.Get(t.Context(), uid)
	mustAI(t, err)
	if saved.Generation != recreated.Generation {
		t.Fatal("new configuration was deleted by stale request")
	}
}

func validateAIJSON(t *testing.T, r *httptest.ResponseRecorder, schema string) {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/openapi.yaml")
	mustAI(t, err)
	var value any
	mustAI(t, json.Unmarshal(r.Body.Bytes(), &value))
	mustAI(t, doc.Components.Schemas[schema].Value.VisitJSON(value))
}
func TestAIErrorBoundaryDistinguishesProviderAndStorage(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{ErrProviderUnavailable, 502, "AI_PROVIDER_UNAVAILABLE"}, {errors.New("private database connection detail"), 503, "AI_UNAVAILABLE"}} {
		r := httptest.NewRecorder()
		router := gin.New()
		router.Use(httpx.RequestIDMiddleware())
		router.GET("/", func(c *gin.Context) { Handler{}.writeConfigurationError(c, tc.err) })
		router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/", nil))
		if r.Code != tc.status {
			t.Fatalf("status=%d", r.Code)
		}
		var value map[string]any
		mustAI(t, json.Unmarshal(r.Body.Bytes(), &value))
		if value["code"] != tc.code || value["message"] == tc.err.Error() {
			t.Fatalf("wrong error boundary: %+v", value)
		}
		validateAIJSON(t, r, "ErrorResponse")
	}
}

func TestMySQLAICallHTTPContractsAndIsolation(t *testing.T) {
	f := newAIIntegrationFixture(t)
	mustAI(t, f.configurations.test(t.Context(), f.users[0].ID, "glm", "glm-4.7-flash", "test-secret-1234"))
	h := Handler{Configurations: f.configurations, Calls: f.configurations.calls, Enabled: true}
	r := gin.New()
	r.Use(httpx.RequestIDMiddleware())
	r.Use(func(c *gin.Context) { httpx.SetCurrentUserID(c, httpx.UserID(f.users[0].ID)) })
	r.GET("/usage", h.Usage)
	r.GET("/calls", h.ListCalls)
	for _, tc := range []struct{ path, schema string }{{"/usage", "AIUsagePage"}, {"/calls?page=1&page_size=1&feature=config_test&status=succeeded", "AICallPage"}} {
		out := httptest.NewRecorder()
		r.ServeHTTP(out, httptest.NewRequest("GET", tc.path, nil))
		if out.Code != 200 {
			t.Fatalf("%s=%d %s", tc.path, out.Code, out.Body.String())
		}
		validateAIJSON(t, out, tc.schema)
	}
	page, e := h.Calls.store.List(t.Context(), f.users[1].ID, "", "", 1, 10)
	mustAI(t, e)
	if page.Total != 0 {
		t.Fatal("calls leaked to other user")
	}
	at := time.Now().UTC().Add(10 * time.Second)
	r.GET("/error", func(c *gin.Context) {
		h.writeConfigurationError(c, &CallError{Code: "rate_limited", CallID: "test-call", RetryAt: &at})
	})
	out := httptest.NewRecorder()
	r.ServeHTTP(out, httptest.NewRequest("GET", "/error", nil))
	if out.Code != 429 || out.Header().Get("Retry-After") == "" {
		t.Fatal("missing retry guidance")
	}
	validateAIJSON(t, out, "ErrorResponse")
}
