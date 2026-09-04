package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/source"
)

const createSubscriptionPath = "/api/v1/subscriptions"

func TestHandlerCreateUsesAuthenticatedUserAndReturnsCreated(t *testing.T) {
	var capturedUserID uint64
	var captured CreateInput
	service := creationServiceStub{create: func(_ context.Context, userID uint64, input CreateInput) (PublicSubscription, error) {
		capturedUserID = userID
		captured = input
		return PublicSubscription{ID: 88, Source: arXivCatalog(), Name: "Agent papers", Enabled: true,
			Version: 1, Rules: PublicRules{Categories: []string{"cs.AI"}, Authors: []string{},
				IncludeKeywords: []string{}, ExcludeKeywords: []string{}}}, nil
	}}
	router, _ := newSubscriptionTestRouter(service, true)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, createSubscriptionPath, strings.NewReader(
		`{"source_id":1,"name":"Agent papers","rules":{"categories":["cs.ai"]}}`,
	))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if capturedUserID != 42 || captured.SourceID != 1 || captured.Enabled != nil ||
		len(captured.Rules.Categories) != 1 {
		t.Fatalf("unexpected service input user=%d input=%+v", capturedUserID, captured)
	}
	var response PublicSubscription
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.ID != 88 {
		t.Fatalf("unexpected response %s: %v", recorder.Body.String(), err)
	}
}

func TestHandlerCreateRequiresAuthenticationContext(t *testing.T) {
	calls := 0
	router, _ := newSubscriptionTestRouter(creationServiceStub{create: func(context.Context, uint64, CreateInput) (PublicSubscription, error) {
		calls++
		return PublicSubscription{}, nil
	}}, false)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, createSubscriptionPath, strings.NewReader(`{}`)))
	assertSubscriptionError(t, recorder, http.StatusUnauthorized, httpx.CodeUnauthorized)
	if calls != 0 {
		t.Fatal("unauthenticated request reached service")
	}
}

func TestHandlerCreateStrictlyRejectsForgedSourceFields(t *testing.T) {
	calls := 0
	service := creationServiceStub{create: func(context.Context, uint64, CreateInput) (PublicSubscription, error) {
		calls++
		return PublicSubscription{}, nil
	}}
	for _, forbidden := range []string{"endpoint", "source_key"} {
		router, _ := newSubscriptionTestRouter(service, true)
		body := `{"source_id":1,"name":"valid","` + forbidden + `":"forged","rules":{"categories":["cs.AI"]}}`
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, createSubscriptionPath, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		assertSubscriptionError(t, recorder, http.StatusBadRequest, httpx.CodeValidationError)
	}
	if calls != 0 {
		t.Fatal("forged source fields must be rejected before service")
	}
}

func TestHandlerCreateMapsExpectedFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"source missing", source.ErrNotFound, http.StatusNotFound, source.CodeSourceNotFound},
		{"limit", ErrLimitReached, http.StatusConflict, CodeSubscriptionLimitReached},
		{"invalid", ErrDuplicateRule, http.StatusBadRequest, httpx.CodeValidationError},
		{"inactive user", ErrUserNotFound, http.StatusUnauthorized, httpx.CodeUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router, _ := newSubscriptionTestRouter(creationServiceStub{create: func(context.Context, uint64, CreateInput) (PublicSubscription, error) {
				return PublicSubscription{}, test.err
			}}, true)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, createSubscriptionPath, strings.NewReader(
				`{"source_id":1,"name":"valid","rules":{"categories":["cs.AI"]}}`,
			))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			assertSubscriptionError(t, recorder, test.status, test.code)
		})
	}
}

func TestHandlerCreateHidesUnexpectedFailure(t *testing.T) {
	service := creationServiceStub{create: func(context.Context, uint64, CreateInput) (PublicSubscription, error) {
		return PublicSubscription{}, errors.New("database detail must stay internal")
	}}
	router, logs := newSubscriptionTestRouter(service, true)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, createSubscriptionPath, strings.NewReader(
		`{"source_id":1,"name":"valid","rules":{"categories":["cs.AI"]}}`,
	))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	assertSubscriptionError(t, recorder, http.StatusInternalServerError, httpx.CodeInternalError)
	if strings.Contains(recorder.Body.String(), "database detail") || !strings.Contains(logs.String(), "database detail") {
		t.Fatal("expected internal error only in server log")
	}
}

type creationServiceStub struct {
	create func(context.Context, uint64, CreateInput) (PublicSubscription, error)
}

func (stub creationServiceStub) Create(ctx context.Context, userID uint64, input CreateInput) (PublicSubscription, error) {
	return stub.create(ctx, userID, input)
}

func newSubscriptionTestRouter(service CreationService, authenticated bool) (*gin.Engine, *bytes.Buffer) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	if authenticated {
		router.Use(func(c *gin.Context) {
			httpx.SetCurrentUserID(c, 42)
			c.Next()
		})
	}
	router.POST(createSubscriptionPath, NewHandler(service, logger).Create)
	return router, &logs
}

func assertSubscriptionError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("expected status %d, got %d: %s", status, recorder.Code, recorder.Body.String())
	}
	var response httpx.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Code != code {
		t.Fatalf("expected error code %q, got %s", code, recorder.Body.String())
	}
}
