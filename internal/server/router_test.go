package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

func TestNewRouterRegistersHealthRoutes(t *testing.T) {
	router := newTestRouter(t)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "health",
			path:       "/healthz",
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok","service":"signalwatch-api"}`,
		},
		{
			name:       "readiness",
			path:       "/readyz",
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ready"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("expected status %d, got %d", test.wantStatus, recorder.Code)
			}

			assertJSONEqual(t, recorder.Body.Bytes(), []byte(test.wantBody))
			assertRequestIDResponseHeader(t, recorder)
		})
	}
}

func TestNewRouterReturnsJSONForUnknownRoute(t *testing.T) {
	router := newTestRouter(t)

	tests := []string{
		"/not-found",
		"/healthz/",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertErrorResponse(
				t,
				recorder,
				http.StatusNotFound,
				httpx.CodeNotFound,
				"route not found",
			)
		})
	}
}

func TestNewRouterReturnsJSONForUnsupportedMethod(t *testing.T) {
	router := newTestRouter(t)

	request := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertErrorResponse(
		t,
		recorder,
		http.StatusMethodNotAllowed,
		httpx.CodeMethodNotAllowed,
		"method not allowed",
	)
}

func TestNewRouterRegistersUserRegistrationRoute(t *testing.T) {
	dependencies := validTestDependencies()

	calls := 0
	dependencies.RegisterHandler = func(c *gin.Context) {
		calls++
		c.Status(http.StatusCreated)
	}

	router, err := NewRouter(dependencies)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/register",
		nil,
	)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusCreated,
			recorder.Code,
		)
	}
	if calls != 1 {
		t.Fatalf("expected register handler once, got %d", calls)
	}
}

func TestNewRouterKeepsPublicRoutesPublicAndProtectsProbe(t *testing.T) {
	dependencies := validTestDependencies()
	loginCalls := 0
	dependencies.LoginHandler = func(c *gin.Context) {
		loginCalls++
		c.Status(http.StatusOK)
	}
	dependencies.AuthMiddleware = func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer valid-token" {
			httpx.WriteError(
				c,
				http.StatusUnauthorized,
				"AUTH_UNAUTHORIZED",
				"unauthorized",
			)
			return
		}
		httpx.SetCurrentUserID(c, 42)
		c.Next()
	}

	router, err := NewRouter(dependencies)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	loginRecorder := httptest.NewRecorder()
	router.ServeHTTP(loginRecorder, loginRequest)
	if loginRecorder.Code != http.StatusOK || loginCalls != 1 {
		t.Fatalf("expected public login route, got status %d and %d calls", loginRecorder.Code, loginCalls)
	}

	unauthorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/probe", nil)
	unauthorizedRecorder := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRecorder, unauthorizedRequest)
	assertErrorResponse(
		t,
		unauthorizedRecorder,
		http.StatusUnauthorized,
		"AUTH_UNAUTHORIZED",
		"unauthorized",
	)

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/probe", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer valid-token")
	authorizedRecorder := httptest.NewRecorder()
	router.ServeHTTP(authorizedRecorder, authorizedRequest)
	if authorizedRecorder.Code != http.StatusOK {
		t.Fatalf("expected protected probe status 200, got %d", authorizedRecorder.Code)
	}
	assertJSONEqual(t, authorizedRecorder.Body.Bytes(), []byte(`{"user_id":42}`))
}

func TestNewRouterUsesStrictJSONBinding(t *testing.T) {
	router := newTestRouter(t)

	type input struct {
		Name string `json:"name"`
	}

	router.POST("/api/v1/test-json", func(c *gin.Context) {
		var body input
		if !httpx.BindJSON(c, &body) {
			return
		}

		c.JSON(http.StatusOK, body)
	})

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "valid body",
			body:       `{"name":"alice"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown field",
			body:       `{"name":"alice","role":"admin"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "second JSON value",
			body:       `{"name":"alice"} {"name":"bob"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "wrong field type",
			body:       `{"name":123}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/test-json",
				strings.NewReader(test.body),
			)
			request.Header.Set("Content-Type", "application/json")

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("expected status %d, got %d", test.wantStatus, recorder.Code)
			}

			assertRequestIDResponseHeader(t, recorder)

			if test.wantStatus == http.StatusBadRequest {
				assertErrorResponse(
					t,
					recorder,
					http.StatusBadRequest,
					httpx.CodeValidationError,
					"request is invalid",
				)
			}
		})
	}
}

func TestNewRouterUsesReleaseModeInProduction(t *testing.T) {
	previousMode := gin.Mode()
	t.Cleanup(func() {
		gin.SetMode(previousMode)
	})

	dependencies := validTestDependencies()
	dependencies.AppEnv = "production"

	if _, err := NewRouter(dependencies); err != nil {
		t.Fatalf("create router: %v", err)
	}

	if gin.Mode() != gin.ReleaseMode {
		t.Fatalf("expected Gin release mode, got %q", gin.Mode())
	}
}

func TestNewRouterDoesNotTrustForwardedClientIP(t *testing.T) {
	router := newTestRouter(t)

	router.GET("/client-ip", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"client_ip": c.ClientIP()})
	})

	request := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
	request.RemoteAddr = "192.0.2.10:4321"
	request.Header.Set("X-Forwarded-For", "203.0.113.20")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	var response struct {
		ClientIP string `json:"client_ip"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if response.ClientIP != "192.0.2.10" {
		t.Fatalf(
			"expected remote IP 192.0.2.10, got %q",
			response.ClientIP,
		)
	}
}

func TestNewRouterValidatesDependencies(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{
			name: "missing app environment",
			mutate: func(dependencies *Dependencies) {
				dependencies.AppEnv = ""
			},
		},
		{
			name: "invalid app environment",
			mutate: func(dependencies *Dependencies) {
				dependencies.AppEnv = "staging"
			},
		},
		{
			name: "missing service name",
			mutate: func(dependencies *Dependencies) {
				dependencies.ServiceName = ""
			},
		},
		{
			name: "missing logger",
			mutate: func(dependencies *Dependencies) {
				dependencies.Logger = nil
			},
		},
		{
			name: "missing MySQL check",
			mutate: func(dependencies *Dependencies) {
				dependencies.MySQLCheck = nil
			},
		},
		{
			name: "missing Redis check",
			mutate: func(dependencies *Dependencies) {
				dependencies.RedisCheck = nil
			},
		},
		{
			name: "missing register handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.RegisterHandler = nil
			},
		},
		{
			name: "missing login handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.LoginHandler = nil
			},
		},
		{
			name: "missing auth middleware",
			mutate: func(dependencies *Dependencies) {
				dependencies.AuthMiddleware = nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies := validTestDependencies()
			test.mutate(&dependencies)

			if _, err := NewRouter(dependencies); err == nil {
				t.Fatal("expected dependency validation error")
			}
		})
	}
}

func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()

	router, err := NewRouter(validTestDependencies())
	if err != nil {
		t.Fatalf("create test router: %v", err)
	}

	return router
}

func validTestDependencies() Dependencies {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	return Dependencies{
		AppEnv:      "test",
		ServiceName: testServiceName,
		Logger:      logger,
		MySQLCheck: func(context.Context) error {
			return nil
		},
		RedisCheck: func(context.Context) error {
			return nil
		},
		RegisterHandler: func(c *gin.Context) {
			c.Status(http.StatusCreated)
		},
		LoginHandler: func(c *gin.Context) {
			c.Status(http.StatusOK)
		},
		AuthMiddleware: func(c *gin.Context) {
			c.Next()
		},
	}
}

func assertErrorResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
	wantMessage string,
) {
	t.Helper()

	if recorder.Code != wantStatus {
		t.Fatalf("expected status %d, got %d", wantStatus, recorder.Code)
	}

	var response httpx.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if response.Code != wantCode {
		t.Fatalf("expected error code %q, got %q", wantCode, response.Code)
	}

	if response.Message != wantMessage {
		t.Fatalf("expected message %q, got %q", wantMessage, response.Message)
	}

	responseID := assertRequestIDResponseHeader(t, recorder)
	if response.RequestID != responseID {
		t.Fatalf(
			"expected response request ID %q, got %q",
			responseID,
			response.RequestID,
		)
	}
}

func assertRequestIDResponseHeader(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) string {
	t.Helper()

	requestID := recorder.Header().Get(httpx.HeaderRequestID)
	if requestID == "" {
		t.Fatal("expected X-Request-ID response header")
	}

	return requestID
}

func assertJSONEqual(t *testing.T, got []byte, want []byte) {
	t.Helper()

	var gotJSON any
	if err := json.Unmarshal(got, &gotJSON); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}

	var wantJSON any
	if err := json.Unmarshal(want, &wantJSON); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}

	gotEncoded, err := json.Marshal(gotJSON)
	if err != nil {
		t.Fatalf("encode actual JSON: %v", err)
	}

	wantEncoded, err := json.Marshal(wantJSON)
	if err != nil {
		t.Fatalf("encode expected JSON: %v", err)
	}

	if string(gotEncoded) != string(wantEncoded) {
		t.Fatalf("expected JSON %s, got %s", wantEncoded, gotEncoded)
	}
}
