package auth

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

func TestMiddlewareStoresStronglyTypedUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var capturedToken string
	verifier := tokenVerifierStub{verify: func(rawToken string) (uint64, error) {
		capturedToken = rawToken
		return 42, nil
	}}
	router := gin.New()
	router.Use(Middleware(verifier))
	router.GET("/private", func(c *gin.Context) {
		userID, ok := httpx.CurrentUserID(c)
		if !ok {
			t.Fatal("expected current user ID in context")
		}
		c.JSON(http.StatusOK, gin.H{"user_id": userID})
	})

	request := httptest.NewRequest(http.MethodGet, "/private", nil)
	request.Header.Set("Authorization", "bEaReR signed-token")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || capturedToken != "signed-token" {
		t.Fatalf("expected accepted bearer token, got status %d token %q", recorder.Code, capturedToken)
	}
}

func TestMiddlewareReturnsOneResponseForAllAuthenticationFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		header     string
		verifyFail bool
	}{
		{name: "missing header"},
		{name: "wrong scheme", header: "Basic token"},
		{name: "missing token", header: "Bearer"},
		{name: "extra token part", header: "Bearer one two"},
		{name: "invalid token", header: "Bearer invalid", verifyFail: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			downstreamCalls := 0
			verifier := tokenVerifierStub{verify: func(string) (uint64, error) {
				if test.verifyFail {
					return 0, ErrInvalidToken
				}
				return 42, nil
			}}
			router := gin.New()
			router.Use(httpx.RequestIDMiddleware(), Middleware(verifier))
			router.GET("/private", func(c *gin.Context) {
				downstreamCalls++
				c.Status(http.StatusOK)
			})

			request := httptest.NewRequest(http.MethodGet, "/private", nil)
			request.Header.Set("Authorization", test.header)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("expected status 401, got %d", recorder.Code)
			}
			if !strings.Contains(recorder.Body.String(), `"code":"AUTH_UNAUTHORIZED"`) ||
				!strings.Contains(recorder.Body.String(), `"message":"unauthorized"`) {
				t.Fatalf("unexpected response %s", recorder.Body.String())
			}
			if downstreamCalls != 0 {
				t.Fatalf("expected downstream handler not to run, got %d calls", downstreamCalls)
			}
		})
	}
}

func TestMiddlewareDoesNotLogAuthorizationToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secretToken = "JWT_SECRET_VALUE_MUST_NOT_LEAK"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	router := gin.New()
	router.Use(
		httpx.RequestIDMiddleware(),
		httpx.AccessLogMiddleware(logger),
		Middleware(tokenVerifierStub{verify: func(string) (uint64, error) {
			return 0, errors.New("signature invalid")
		}}),
	)
	router.GET("/private", func(c *gin.Context) { c.Status(http.StatusOK) })

	request := httptest.NewRequest(http.MethodGet, "/private", nil)
	request.Header.Set("Authorization", "Bearer "+secretToken)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", recorder.Code)
	}
	if strings.Contains(logs.String(), secretToken) ||
		strings.Contains(strings.ToLower(logs.String()), "authorization") {
		t.Fatalf("authentication log leaked Authorization data: %s", logs.String())
	}
}

type tokenVerifierStub struct {
	verify func(string) (uint64, error)
}

func (stub tokenVerifierStub) Verify(rawToken string) (uint64, error) {
	return stub.verify(rawToken)
}
