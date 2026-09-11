package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

func TestHandlerListDoesNotLeakSensitiveSourceFields(t *testing.T) {
	service := catalogServiceStub{list: func(context.Context) ([]PublicSource, error) {
		return []PublicSource{{
			ID: 1, SourceKey: "arxiv", Kind: KindArXiv, Name: "arXiv",
			RuleTypes: []string{RuleTypeCategory}, AllowedCategories: []string{"cs.AI"},
		}}, nil
	}}
	router := newSourceTestRouter(service)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v2/sources", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"endpoint", "config_json", "credential", "schedule"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked forbidden field %q: %s", forbidden, body)
		}
	}
	var response []PublicSource
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || len(response) != 1 {
		t.Fatalf("unexpected response %s: %v", body, err)
	}
}

func TestHandlerGetValidatesIDAndMapsNotFound(t *testing.T) {
	service := catalogServiceStub{get: func(_ context.Context, id uint64) (PublicSource, error) {
		if id == 9 {
			return PublicSource{}, ErrNotFound
		}
		return PublicSource{ID: id}, nil
	}}
	router := newSourceTestRouter(service)

	for _, test := range []struct {
		path   string
		status int
		code   string
	}{
		{"/api/v2/sources/nope", http.StatusBadRequest, httpx.CodeValidationError},
		{"/api/v2/sources/0", http.StatusBadRequest, httpx.CodeValidationError},
		{"/api/v2/sources/9", http.StatusNotFound, CodeSourceNotFound},
		{"/api/v2/sources/2", http.StatusOK, ""},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != test.status {
			t.Fatalf("%s: expected %d, got %d", test.path, test.status, recorder.Code)
		}
		if test.code != "" {
			var response httpx.ErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Code != test.code {
				t.Fatalf("%s: unexpected error response %s", test.path, recorder.Body.String())
			}
		}
	}
}

func TestHandlerHidesAndLogsInternalErrors(t *testing.T) {
	logger, logs := sourceTestLogger()
	handler := NewHandler(catalogServiceStub{list: func(context.Context) ([]PublicSource, error) {
		return nil, errors.New("database password must stay internal")
	}}, logger)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	router.GET("/api/v2/sources", handler.List)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v2/sources", nil))
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "database password") {
		t.Fatalf("unexpected response %s", recorder.Body.String())
	}
	if !strings.Contains(logs.String(), "database password") {
		t.Fatal("expected internal cause in server log")
	}
}

type catalogServiceStub struct {
	list func(context.Context) ([]PublicSource, error)
	get  func(context.Context, uint64) (PublicSource, error)
}

func (stub catalogServiceStub) List(ctx context.Context) ([]PublicSource, error) {
	if stub.list == nil {
		return []PublicSource{}, nil
	}
	return stub.list(ctx)
}

func (stub catalogServiceStub) Get(ctx context.Context, id uint64) (PublicSource, error) {
	return stub.get(ctx, id)
}

func newSourceTestRouter(service CatalogService) *gin.Engine {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := NewHandler(service, logger)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	router.GET("/api/v2/sources", handler.List)
	router.GET("/api/v2/sources/:id", handler.Get)
	return router
}

func sourceTestLogger() (*slog.Logger, *bytes.Buffer) {
	var logs bytes.Buffer
	return slog.New(slog.NewJSONHandler(&logs, nil)), &logs
}
