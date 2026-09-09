package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/user"
)

type ActiveIdentityLoader interface {
	FindActiveByID(ctx context.Context, userID uint64) (user.User, error)
}

func RequireRoles(loader ActiveIdentityLoader, logger *slog.Logger, roles ...string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		allowed[role] = struct{}{}
	}
	return func(c *gin.Context) {
		current, ok := httpx.CurrentUserID(c)
		if !ok || current == 0 {
			writeUnauthorized(c)
			return
		}
		identity, err := loader.FindActiveByID(c.Request.Context(), uint64(current))
		if errors.Is(err, user.ErrNotFound) {
			writeUnauthorized(c)
			return
		}
		if err != nil {
			logger.Error("authorization identity lookup failed", "module", "auth", "event", "authorization_lookup_failed", "request_id", httpx.RequestID(c), "error", err)
			httpx.WriteError(c, http.StatusServiceUnavailable, httpx.CodeAuthorizationUnavailable, "authorization temporarily unavailable")
			return
		}
		httpx.SetCurrentRole(c, identity.Role)
		if _, accepted := allowed[identity.Role]; !accepted {
			httpx.WriteError(c, http.StatusForbidden, httpx.CodeForbidden, "forbidden")
			return
		}
		c.Next()
	}
}
