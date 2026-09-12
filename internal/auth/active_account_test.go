package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/user"
)

type identityLoaderFunc func(context.Context, uint64) (user.User, error)

func (loader identityLoaderFunc) FindActiveByID(ctx context.Context, id uint64) (user.User, error) {
	return loader(ctx, id)
}

func TestRequireActiveAccountReadsCurrentStatusOnEachRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	active := true
	reads := 0
	loader := identityLoaderFunc(func(_ context.Context, id uint64) (user.User, error) {
		reads++
		if !active {
			return user.User{}, user.ErrNotFound
		}
		return user.User{ID: id, Status: user.StatusActive}, nil
	})
	router := activeAccountTestRouter(loader)
	if got := performAccountRequest(router).Code; got != http.StatusOK {
		t.Fatalf("active account: %d", got)
	}
	active = false
	if got := performAccountRequest(router).Code; got != http.StatusUnauthorized {
		t.Fatalf("disabled account: %d", got)
	}
	active = true
	if got := performAccountRequest(router).Code; got != http.StatusOK {
		t.Fatalf("reactivated account: %d", got)
	}
	if reads != 3 {
		t.Fatalf("expected one lookup per request, got %d", reads)
	}
}

func TestRequireActiveAccountMapsIdentityFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "missing or inactive account", err: user.ErrNotFound, wantStatus: http.StatusUnauthorized, wantCode: httpx.CodeUnauthorized},
		{name: "database unavailable", err: errors.New("database unavailable"), wantStatus: http.StatusServiceUnavailable, wantCode: httpx.CodeAuthorizationUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := activeAccountTestRouter(identityLoaderFunc(func(context.Context, uint64) (user.User, error) {
				return user.User{}, test.err
			}))
			response := performAccountRequest(router)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("expected %d/%s, got %d %s", test.wantStatus, test.wantCode, response.Code, response.Body.String())
			}
		})
	}
}

func activeAccountTestRouter(loader ActiveIdentityLoader) *gin.Engine {
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware(), func(c *gin.Context) {
		httpx.SetCurrentUserID(c, 42)
		c.Next()
	}, RequireActiveAccount(loader, slog.New(slog.NewJSONHandler(io.Discard, nil))))
	router.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return router
}

func performAccountRequest(router http.Handler) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
