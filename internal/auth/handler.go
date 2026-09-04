package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

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

type Handler struct {
	service LoginService
	logger  *slog.Logger
}

func NewHandler(service LoginService, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

func (handler *Handler) Login(c *gin.Context) {
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
