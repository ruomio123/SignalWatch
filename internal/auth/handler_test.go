package auth

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
)

const loginPath = "/api/v2/auth/login"

func TestHandlerLoginReturnsBearerToken(t *testing.T) {
	var captured LoginInput
	service := loginServiceStub{login: func(_ context.Context, input LoginInput) (LoginResult, error) {
		captured = input
		return LoginResult{AccessToken: "signed-token", ExpiresIn: 900}, nil
	}}
	logger, logs := newAuthHandlerTestLogger()
	router := newAuthHandlerTestRouter(service, logger)

	request := httptest.NewRequest(
		http.MethodPost,
		loginPath,
		strings.NewReader(`{"email":"Alice@Example.com","password":"correct-horse-123"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	if captured != (LoginInput{Email: "Alice@Example.com", Password: "correct-horse-123"}) {
		t.Fatalf("unexpected service input %+v", captured)
	}
	var response loginResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.AccessToken != "signed-token" || response.TokenType != "Bearer" || response.ExpiresIn != 900 {
		t.Fatalf("unexpected response %+v", response)
	}
	if logs.Len() != 0 {
		t.Fatalf("expected no error logs, got %s", logs.String())
	}
}

func TestHandlerLoginUsesOneResponseForCredentialFailures(t *testing.T) {
	for _, name := range []string{"wrong password", "missing email", "inactive user"} {
		t.Run(name, func(t *testing.T) {
			logger, logs := newAuthHandlerTestLogger()
			router := newAuthHandlerTestRouter(
				loginServiceStub{login: func(context.Context, LoginInput) (LoginResult, error) {
					return LoginResult{}, ErrInvalidCredentials
				}},
				logger,
			)
			request := httptest.NewRequest(
				http.MethodPost,
				loginPath,
				strings.NewReader(`{"email":"alice@example.com","password":"wrong-password"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			assertAuthErrorResponse(
				t,
				recorder,
				http.StatusUnauthorized,
				CodeInvalidCredentials,
				"invalid credentials",
			)
			if logs.Len() != 0 {
				t.Fatalf("credential failure must not produce a distinguishing log, got %s", logs.String())
			}
		})
	}
}

func TestHandlerLoginRejectsUnknownJSONFieldBeforeService(t *testing.T) {
	calls := 0
	logger, _ := newAuthHandlerTestLogger()
	router := newAuthHandlerTestRouter(
		loginServiceStub{login: func(context.Context, LoginInput) (LoginResult, error) {
			calls++
			return LoginResult{}, nil
		}},
		logger,
	)
	request := httptest.NewRequest(
		http.MethodPost,
		loginPath,
		strings.NewReader(`{"email":"alice@example.com","password":"password","admin":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assertAuthErrorResponse(
		t,
		recorder,
		http.StatusBadRequest,
		httpx.CodeValidationError,
		"request is invalid",
	)
	if calls != 0 {
		t.Fatalf("expected service not to be called, got %d calls", calls)
	}
}

func TestHandlerLoginHidesInternalErrorsAndCredentials(t *testing.T) {
	const (
		password       = "PASSWORD_MUST_NOT_LEAK"
		internalReason = "DATABASE_FAILURE_REASON"
	)
	logger, logs := newAuthHandlerTestLogger()
	router := newAuthHandlerTestRouter(
		loginServiceStub{login: func(context.Context, LoginInput) (LoginResult, error) {
			return LoginResult{}, errors.New(internalReason)
		}},
		logger,
	)
	request := httptest.NewRequest(
		http.MethodPost,
		loginPath,
		strings.NewReader(`{"email":"alice@example.com","password":"`+password+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assertAuthErrorResponse(
		t,
		recorder,
		http.StatusInternalServerError,
		httpx.CodeInternalError,
		"internal server error",
	)
	if strings.Contains(recorder.Body.String(), internalReason) ||
		strings.Contains(recorder.Body.String(), password) {
		t.Fatal("response leaked an internal error or password")
	}
	if !strings.Contains(logs.String(), internalReason) {
		t.Fatalf("expected internal error in server log, got %s", logs.String())
	}
	if strings.Contains(logs.String(), password) {
		t.Fatal("server log leaked password")
	}
}

type loginServiceStub struct {
	login func(context.Context, LoginInput) (LoginResult, error)
}

func (stub loginServiceStub) Login(ctx context.Context, input LoginInput) (LoginResult, error) {
	return stub.login(ctx, input)
}

func newAuthHandlerTestRouter(service LoginService, logger *slog.Logger) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	router.POST(loginPath, NewHandler(service, browserSessionsStub{}, false, logger).Login)
	return router
}

func newAuthHandlerTestLogger() (*slog.Logger, *bytes.Buffer) {
	var logs bytes.Buffer
	return slog.New(slog.NewJSONHandler(&logs, nil)), &logs
}

func assertAuthErrorResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	status int,
	code string,
	message string,
) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("expected status %d, got %d", status, recorder.Code)
	}
	var response httpx.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Code != code || response.Message != message {
		t.Fatalf("unexpected error response %+v", response)
	}
	if response.RequestID == "" || response.RequestID != recorder.Header().Get(httpx.HeaderRequestID) {
		t.Fatal("expected matching request ID in response header and body")
	}
}
