package contract_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"

	"signalwatch/internal/server"
)

func TestOpenAPIContractIsValidAndCoversCurrentRoutes(t *testing.T) {
	specPath := projectFile(t, "api", "openapi.yaml")
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	document, err := loader.LoadFromFile(specPath)
	if err != nil {
		t.Fatalf("load OpenAPI contract: %v", err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatalf("validate OpenAPI contract: %v", err)
	}
	if document.OpenAPI != "3.0.3" {
		t.Fatalf("expected OpenAPI 3.0.3, got %q", document.OpenAPI)
	}

	expected := currentOperations()
	if document.Paths.Len() != len(expected) {
		t.Fatalf("expected %d documented paths, got %d", len(expected), document.Paths.Len())
	}
	for path, methods := range expected {
		item := document.Paths.Find(path)
		if item == nil {
			t.Fatalf("OpenAPI path %s is missing", path)
		}
		operations := item.Operations()
		if len(operations) != len(methods) {
			t.Fatalf("path %s has operations %v, expected %v", path, mapKeys(operations), methods)
		}
		for _, method := range methods {
			if operations[method] == nil {
				t.Errorf("OpenAPI operation %s %s is missing", method, path)
			}
		}
	}
}

func TestOpenAPIRegistrationDocumentsPasswordComposition(t *testing.T) {
	document := loadContract(t)
	register := document.Components.Schemas["RegisterRequest"]
	if register == nil || register.Value == nil {
		t.Fatal("RegisterRequest schema is missing")
	}
	password := register.Value.Properties["password"]
	if password == nil || password.Value == nil {
		t.Fatal("RegisterRequest password schema is missing")
	}
	description := strings.ToLower(password.Value.Description)
	for _, requirement := range []string{"letter", "number", "72 utf-8 bytes"} {
		if !strings.Contains(description, requirement) {
			t.Errorf("password schema does not document %q", requirement)
		}
	}
}

func TestOpenAPIContractMatchesRegisteredRouterOperations(t *testing.T) {
	document := loadContract(t)
	expected := currentOperations()
	registered := make(map[string]map[string]bool, len(expected))
	for path := range expected {
		registered[path] = make(map[string]bool)
	}

	noop := func(c *gin.Context) { c.Status(200) }
	router, err := server.NewRouter(server.Dependencies{
		AppEnv:      "test",
		ServiceName: "signalwatch-api",
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		MySQLCheck:  func(context.Context) error { return nil },
		RedisCheck:  func(context.Context) error { return nil }, Accounts: server.AccountsRoutes{RegisterHandler: noop,
			LoginHandler:   noop,
			RefreshHandler: noop, LogoutHandler: noop,

			GetProfileHandler:    noop,
			UpdateProfileHandler: noop}, Authorization: server.AuthorizationRoutes{AuthMiddleware: noop,
			ActiveAccountMiddleware: noop}, Sources: server.SourcesRoutes{ListSourcesHandler: noop,
			GetSourceHandler: noop}, Subscriptions: server.SubscriptionsRoutes{CreateSubscriptionHandler: noop,
			ListSubscriptionsHandler:  noop,
			GetSubscriptionHandler:    noop,
			UpdateSubscriptionHandler: noop,
			DeleteSubscriptionHandler: noop}, Papers: server.PapersRoutes{ListPapersHandler: noop,
			GetPaperHandler: noop},
	})
	if err != nil {
		t.Fatalf("create router for contract comparison: %v", err)
	}
	for _, route := range router.Routes() {
		path := strings.ReplaceAll(strings.ReplaceAll(route.Path, ":id", "{id}"), ":provider", "{provider}")
		if _, covered := registered[path]; covered {
			registered[path][route.Method] = true
		}
	}
	for path, methods := range expected {
		item := document.Paths.Find(path)
		for _, method := range methods {
			if !registered[path][method] {
				t.Errorf("Router is missing documented operation %s %s", method, path)
			}
			if item == nil || item.Operations()[method] == nil {
				t.Errorf("OpenAPI is missing registered operation %s %s", method, path)
			}
		}
	}
}

func TestOpenAPISecurityVersionHeadersAndDeleteResponseMatchHandlers(t *testing.T) {
	document := loadContract(t)
	if len(document.Security) != 1 {
		t.Fatal("protected operations must inherit the bearer security requirement")
	}
	for _, route := range []struct {
		path   string
		method string
	}{
		{path: "/healthz", method: "GET"},
		{path: "/readyz", method: "GET"},
		{path: "/api/v2/auth/register", method: "POST"},
		{path: "/api/v2/auth/login", method: "POST"},
	} {
		operation := document.Paths.Find(route.path).Operations()[route.method]
		if operation.Security == nil || len(*operation.Security) != 0 {
			t.Errorf("%s %s must explicitly be public", route.method, route.path)
		}
	}

	item := document.Paths.Find("/api/v2/subscriptions/{id}")
	for _, operation := range []*openapi3.Operation{item.Patch, item.Delete} {
		found := false
		for _, parameter := range operation.Parameters {
			if parameter.Value != nil && parameter.Value.Name == "If-Match" && parameter.Value.Required {
				found = true
			}
		}
		if !found {
			t.Error("PATCH and DELETE subscriptions must require If-Match")
		}
		if operation.Responses.Value("409") == nil {
			t.Error("PATCH and DELETE subscriptions must document version-conflict 409")
		}
	}
	if item.Get.Responses.Value("200").Value.Headers["ETag"] == nil ||
		item.Patch.Responses.Value("200").Value.Headers["ETag"] == nil {
		t.Fatal("GET and PATCH subscription responses must document ETag")
	}
	deleted := item.Delete.Responses.Value("204")
	if deleted == nil || deleted.Value == nil || len(deleted.Value.Content) != 0 {
		t.Fatal("DELETE 204 must have no response content")
	}
}

func TestOpenAPIContainsNoDeploymentSecretsAndStatesM4Boundary(t *testing.T) {
	content, err := os.ReadFile(projectFile(t, "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read OpenAPI contract: %v", err)
	}
	lower := strings.ToLower(string(content))
	for _, forbidden := range []string{
		"change-me-app-password",
		"change-me-root-password",
		"development-only-change-me",
		"export.arxiv.org",
		"mysql_dsn",
		"redis_password",
	} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("OpenAPI contract contains forbidden deployment detail %q", forbidden)
		}
	}
	if !strings.Contains(lower, "background local matching") ||
		!strings.Contains(lower, "explainable match reasons") ||
		!strings.Contains(lower, "matched arxiv papers") ||
		!strings.Contains(lower, "daily digest") ||
		!strings.Contains(lower, "email delivery worker") {
		t.Error("OpenAPI contract must describe the M4 matching, paper-query, and Digest boundary")
	}
	for _, boundary := range []string{"not exposed as public endpoints", "user-supplied provider credentials", "agents"} {
		if !strings.Contains(lower, boundary) {
			t.Errorf("OpenAPI contract must state the M4 public API boundary %q", boundary)
		}
	}
}

func loadContract(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	document, err := loader.LoadFromFile(projectFile(t, "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("load OpenAPI contract: %v", err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatalf("validate OpenAPI contract: %v", err)
	}
	return document
}

func projectFile(t *testing.T, elements ...string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	return filepath.Join(append([]string{root}, elements...)...)
}

func mapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func currentOperations() map[string][]string {
	return map[string][]string{
		"/api/v2/ai/credentials":                         {"GET", "POST"},
		"/api/v2/ai/credentials/{id}":                    {"GET", "PUT", "DELETE"},
		"/api/v2/ai/credentials/{id}/test":               {"POST"},
		"/api/v2/ai/default-selection":                   {"GET", "PUT"},
		"/api/v2/agent/conversations":                    {"GET", "POST"},
		"/api/v2/agent/conversations/{id}":               {"GET", "DELETE"},
		"/api/v2/agent/conversations/{id}/messages":      {"GET", "POST"},
		"/api/v2/agent/runs/{id}":                        {"GET"},
		"/api/v2/agent/runs/{id}/cancel":                 {"POST"},
		"/api/v2/agent/subscription-drafts/{id}":         {"GET", "PATCH"},
		"/api/v2/agent/subscription-drafts/{id}/confirm": {"POST"},
		"/healthz":                        {"GET"},
		"/readyz":                         {"GET"},
		"/api/v2/auth/register":           {"POST"},
		"/api/v2/auth/refresh":            {"POST"},
		"/api/v2/auth/logout":             {"POST"},
		"/api/v2/auth/login":              {"POST"},
		"/api/v2/me":                      {"GET", "PATCH"},
		"/api/v2/sources":                 {"GET"},
		"/api/v2/sources/{id}":            {"GET"},
		"/api/v2/subscriptions":           {"GET", "POST"},
		"/api/v2/subscriptions/{id}":      {"GET", "PATCH", "DELETE"},
		"/api/v2/papers":                  {"GET"},
		"/api/v2/papers/{id}/ai-summary":  {"GET", "POST"},
		"/api/v2/papers/{id}":             {"GET"},
		"/api/v2/ai/providers":            {"GET"},
		"/api/v2/ai/configuration":        {"GET", "PUT", "PATCH", "DELETE"},
		"/api/v2/ai/configuration/secret": {"PUT"},
		"/api/v2/ai/configuration/test":   {"POST"},
		"/api/v2/ai/calls":                {"GET"},
		"/api/v2/ai/usage":                {"GET"},
	}
}
