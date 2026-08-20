package server

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
	"time"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

const testServiceName = "signalwatch-api"

func TestHealthHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.GET("/healthz", healthHandler(testServiceName))

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertStatusResponse(
		t,
		recorder,
		http.StatusOK,
		"ok",
		testServiceName,
	)
}

func TestReadinessHandlerReady(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logger, logs := newServerTestLogger()

	var mysqlContext context.Context
	var redisContext context.Context
	mysqlCalls := 0
	redisCalls := 0

	mysqlCheck := DependencyCheck(func(ctx context.Context) error {
		mysqlCalls++
		mysqlContext = ctx
		return nil
	})

	redisCheck := DependencyCheck(func(ctx context.Context) error {
		redisCalls++
		redisContext = ctx
		return nil
	})

	router := gin.New()
	router.GET(
		"/readyz",
		readinessHandler(logger, mysqlCheck, redisCheck),
	)

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertStatusResponse(
		t,
		recorder,
		http.StatusOK,
		"ready",
		"",
	)

	if mysqlCalls != 1 {
		t.Fatalf("expected MySQL check once, got %d", mysqlCalls)
	}

	if redisCalls != 1 {
		t.Fatalf("expected Redis check once, got %d", redisCalls)
	}

	if mysqlContext == nil || redisContext == nil {
		t.Fatal("expected both dependency checks to receive a context")
	}

	if mysqlContext != redisContext {
		t.Fatal("expected MySQL and Redis to share the same timeout context")
	}

	deadline, ok := mysqlContext.Deadline()
	if !ok {
		t.Fatal("expected readiness context to have a deadline")
	}

	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > readinessTimeout {
		t.Fatalf("unexpected readiness deadline remaining: %s", remaining)
	}

	select {
	case <-mysqlContext.Done():
		// Handler 返回时应通过 cancel 释放 timeout Context 的资源。
	default:
		t.Fatal("expected readiness context to be canceled after the response")
	}

	if logs.Len() != 0 {
		t.Fatalf("expected no readiness error log, got %s", logs.String())
	}
}

func TestReadinessHandlerNotReady(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		mysqlError     error
		redisError     error
		wantDependency string
		wantError      string
		wantMySQLCalls int
		wantRedisCalls int
	}{
		{
			name:           "mysql unavailable",
			mysqlError:     errors.New("MYSQL_INTERNAL_SECRET"),
			wantDependency: "mysql",
			wantError:      "MYSQL_INTERNAL_SECRET",
			wantMySQLCalls: 1,
			wantRedisCalls: 0,
		},
		{
			name:           "redis unavailable",
			redisError:     errors.New("REDIS_INTERNAL_SECRET"),
			wantDependency: "redis",
			wantError:      "REDIS_INTERNAL_SECRET",
			wantMySQLCalls: 1,
			wantRedisCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger, logs := newServerTestLogger()
			mysqlCalls := 0
			redisCalls := 0

			mysqlCheck := DependencyCheck(func(context.Context) error {
				mysqlCalls++
				return test.mysqlError
			})

			redisCheck := DependencyCheck(func(context.Context) error {
				redisCalls++
				return test.redisError
			})

			router := gin.New()
			router.Use(httpx.RequestIDMiddleware())
			router.GET(
				"/readyz",
				readinessHandler(logger, mysqlCheck, redisCheck),
			)

			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertStatusResponse(
				t,
				recorder,
				http.StatusServiceUnavailable,
				"not_ready",
				"",
			)

			if strings.Contains(recorder.Body.String(), test.wantError) {
				t.Fatalf("readiness response leaked internal error %q", test.wantError)
			}

			if mysqlCalls != test.wantMySQLCalls {
				t.Fatalf(
					"expected MySQL check %d times, got %d",
					test.wantMySQLCalls,
					mysqlCalls,
				)
			}

			if redisCalls != test.wantRedisCalls {
				t.Fatalf(
					"expected Redis check %d times, got %d",
					test.wantRedisCalls,
					redisCalls,
				)
			}

			responseID := recorder.Header().Get(httpx.HeaderRequestID)
			if responseID == "" {
				t.Fatal("expected response to contain a request ID")
			}

			var record readinessLogRecord
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
				t.Fatalf("decode readiness log: %v", err)
			}

			if record.Level != "ERROR" {
				t.Fatalf("expected log level ERROR, got %q", record.Level)
			}

			if record.Message != "readiness check failed" {
				t.Fatalf("unexpected log message %q", record.Message)
			}

			if record.Module != "readiness" {
				t.Fatalf("expected module readiness, got %q", record.Module)
			}

			if record.Dependency != test.wantDependency {
				t.Fatalf(
					"expected dependency %q, got %q",
					test.wantDependency,
					record.Dependency,
				)
			}

			if record.RequestID != responseID {
				t.Fatalf(
					"expected log request ID %q, got %q",
					responseID,
					record.RequestID,
				)
			}

			if record.Error != test.wantError {
				t.Fatalf("expected log error %q, got %q", test.wantError, record.Error)
			}
		})
	}
}

type readinessLogRecord struct {
	Level      string `json:"level"`
	Message    string `json:"msg"`
	Module     string `json:"module"`
	Dependency string `json:"dependency"`
	RequestID  string `json:"request_id"`
	Error      string `json:"error"`
}

func newServerTestLogger() (*slog.Logger, *bytes.Buffer) {
	var output bytes.Buffer
	handler := slog.NewJSONHandler(&output, nil)

	return slog.New(handler), &output
}

func assertStatusResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatusCode int,
	wantStatus string,
	wantService string,
) {
	t.Helper()

	if recorder.Code != wantStatusCode {
		t.Fatalf("expected status %d, got %d", wantStatusCode, recorder.Code)
	}

	contentType := recorder.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected JSON content type, got %q", contentType)
	}

	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if response["status"] != wantStatus {
		t.Fatalf("expected status body %q, got %q", wantStatus, response["status"])
	}

	wantFieldCount := 1
	if wantService != "" {
		wantFieldCount = 2
		if response["service"] != wantService {
			t.Fatalf(
				"expected service %q, got %q",
				wantService,
				response["service"],
			)
		}
	}

	if len(response) != wantFieldCount {
		t.Fatalf(
			"expected %d response fields, got %d: %#v",
			wantFieldCount,
			len(response),
			response,
		)
	}
}
