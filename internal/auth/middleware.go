package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

const CodeUnauthorized = httpx.CodeUnauthorized

type TokenVerifier interface {
	Verify(rawToken string) (uint64, error)
}

func Middleware(verifier TokenVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		rawToken, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			writeUnauthorized(c)
			return
		}

		userID, err := verifier.Verify(rawToken)
		if err != nil || userID == 0 {
			writeUnauthorized(c)
			return
		}

		httpx.SetCurrentUserID(c, httpx.UserID(userID))
		c.Next()
	}
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func writeUnauthorized(c *gin.Context) {
	httpx.WriteError(
		c,
		http.StatusUnauthorized,
		CodeUnauthorized,
		"unauthorized",
	)
}
