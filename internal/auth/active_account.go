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

// RequireActiveAccount rechecks account status on every protected request.
func RequireActiveAccount(loader ActiveIdentityLoader, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		current, ok := httpx.CurrentUserID(c)
		if !ok || current == 0 {
			writeUnauthorized(c)
			return
		}
		_, err := loader.FindActiveByID(c.Request.Context(), uint64(current))
		if errors.Is(err, user.ErrNotFound) {
			writeUnauthorized(c)
			return
		}
		if err != nil {
			logger.Error("authorization identity lookup failed", "module", "auth", "event", "authorization_lookup_failed", "request_id", httpx.RequestID(c), "error", err)
			httpx.WriteError(c, http.StatusServiceUnavailable, httpx.CodeAuthorizationUnavailable, "authorization temporarily unavailable")
			return
		}
		c.Next()
	}
}
