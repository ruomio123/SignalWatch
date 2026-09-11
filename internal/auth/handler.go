package auth

import (
	"context"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

const CodeInvalidCredentials = "AUTH_INVALID_CREDENTIALS"

type LoginService interface {
	Login(ctx context.Context, input LoginInput) (LoginResult, error)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

type BrowserSessions interface {
	Refresh(context.Context, string) (IssuedToken, error)
	Logout(context.Context, string) error
}

const SessionCookieName = "signalwatch_session"
const SessionRequestHeader = "X-SignalWatch-Session"

type Handler struct {
	sessions     BrowserSessions
	secureCookie bool
	service      LoginService
	logger       *slog.Logger
}

func NewHandler(service LoginService, sessions BrowserSessions, secureCookie bool, logger *slog.Logger) *Handler {
	return &Handler{service: service, sessions: sessions, secureCookie: secureCookie, logger: logger}
}

func (handler *Handler) Login(c *gin.Context) {
	noStore(c)
	mediaType, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if mediaType != "application/json" || c.GetHeader("Sec-Fetch-Site") == "cross-site" {
		httpx.WriteError(c, 400, httpx.CodeValidationError, "request is invalid")
		return
	}
	var request loginRequest
	if !httpx.BindJSON(c, &request) {
		return
	}

	result, err := handler.service.Login(c.Request.Context(), LoginInput{
		Email:    request.Email,
		Password: request.Password,
	})
	if err != nil {
		handler.writeLoginError(c, err)
		return
	}

	handler.setSessionCookie(c, result.RefreshToken, result.SessionExpiresAt)
	c.JSON(http.StatusOK, loginResponse{
		AccessToken: result.AccessToken,
		TokenType:   "Bearer",
		ExpiresIn:   result.ExpiresIn,
	})
}

func (handler *Handler) writeLoginError(c *gin.Context, err error) {
	if errors.Is(err, ErrInvalidCredentials) {
		httpx.WriteError(
			c,
			http.StatusUnauthorized,
			CodeInvalidCredentials,
			"invalid credentials",
		)
		return
	}

	handler.logger.Error(
		"login failed",
		"module", "auth",
		"request_id", httpx.RequestID(c),
		"error", err,
	)
	httpx.WriteError(
		c,
		http.StatusInternalServerError,
		httpx.CodeInternalError,
		"internal server error",
	)
}

func (handler *Handler) Refresh(c *gin.Context) {
	noStore(c)
	if !sessionRequest(c) {
		return
	}
	raw, _ := c.Cookie(SessionCookieName)
	result, err := handler.sessions.Refresh(c.Request.Context(), raw)
	if err != nil {
		if errors.Is(err, ErrInvalidSession) {
			// Renewal never mutates the cookie: a late rejected response must not
			// erase a newer login from another tab. Logout clears it explicitly.
			httpx.WriteError(c, 401, CodeUnauthorized, "login session expired")
			return
		}
		handler.logger.Error("session refresh failed", "module", "auth", "request_id", httpx.RequestID(c), "error", err)
		httpx.WriteError(c, 503, "AUTH_UNAVAILABLE", "authentication temporarily unavailable")
		return
	}
	c.JSON(http.StatusOK, loginResponse{AccessToken: result.AccessToken, TokenType: "Bearer", ExpiresIn: result.ExpiresIn})
}
func (handler *Handler) Logout(c *gin.Context) {
	noStore(c)
	if !sessionRequest(c) {
		return
	}
	raw, _ := c.Cookie(SessionCookieName)
	if err := handler.sessions.Logout(c.Request.Context(), raw); err != nil {
		handler.logger.Error("session logout failed", "module", "auth", "request_id", httpx.RequestID(c), "error", err)
		httpx.WriteError(c, 503, "AUTH_UNAVAILABLE", "authentication temporarily unavailable")
		return
	}
	handler.clearSessionCookie(c)
	c.Status(http.StatusNoContent)
}
func noStore(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Header("Pragma", "no-cache") }

// A non-simple request header prevents cross-origin forms from exercising cookie
// authority. This API intentionally does not grant cross-origin CORS access.
func sessionRequest(c *gin.Context) bool {
	if c.GetHeader(SessionRequestHeader) != "1" || c.GetHeader("Sec-Fetch-Site") == "cross-site" {
		httpx.WriteError(c, 403, "AUTH_ORIGIN_REJECTED", "same-origin session request required")
		return false
	}
	return true
}
func (handler *Handler) setSessionCookie(c *gin.Context, raw string, expires time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{Name: SessionCookieName, Value: raw, Path: "/api/v2/auth", HttpOnly: true, Secure: handler.secureCookie, SameSite: http.SameSiteLaxMode, Expires: expires})
}
func (handler *Handler) clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/api/v2/auth", HttpOnly: true, Secure: handler.secureCookie, SameSite: http.SameSiteLaxMode, Expires: time.Unix(1, 0), MaxAge: -1})
}
