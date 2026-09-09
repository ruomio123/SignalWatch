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

func TestRequireRolesReadsCurrentDatabaseRoleOnEachRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	currentRole := user.RoleUser
	loader := identityLoaderFunc(func(_ context.Context, id uint64) (user.User, error) {
		return user.User{ID: id, Status: user.StatusActive, Role: currentRole}, nil
	})
	router := roleTestRouter(loader, user.RoleOperator)

	first := performRoleRequest(router)
	if first.Code != http.StatusForbidden || !strings.Contains(first.Body.String(), httpx.CodeForbidden) {
		t.Fatalf("expected user role to be forbidden, got %d %s", first.Code, first.Body.String())
	}

	currentRole = user.RoleOperator
	second := performRoleRequest(router)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), user.RoleOperator) {
		t.Fatalf("expected live operator grant to take effect, got %d %s", second.Code, second.Body.String())
	}

	currentRole = user.RoleUser
	third := performRoleRequest(router)
	if third.Code != http.StatusForbidden {
		t.Fatalf("expected live operator revocation to take effect, got %d", third.Code)
	}
}

func TestRequireRolesMapsIdentityFailures(t *testing.T) {
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
			router := roleTestRouter(identityLoaderFunc(func(context.Context, uint64) (user.User, error) {
				return user.User{}, test.err
			}), user.RoleUser)
			response := performRoleRequest(router)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("expected %d/%s, got %d %s", test.wantStatus, test.wantCode, response.Code, response.Body.String())
			}
		})
	}
}

func roleTestRouter(loader ActiveIdentityLoader, role string) *gin.Engine {
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware(), func(c *gin.Context) {
		httpx.SetCurrentUserID(c, 42)
		c.Next()
	}, RequireRoles(loader, slog.New(slog.NewJSONHandler(io.Discard, nil)), role))
	router.GET("/protected", func(c *gin.Context) {
		currentRole, _ := httpx.CurrentRole(c)
		c.JSON(http.StatusOK, gin.H{"role": currentRole})
	})
	return router
}

func performRoleRequest(router http.Handler) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
