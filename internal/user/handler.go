package user

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

// 定义重复邮箱的 HTTP 业务错误码
const CodeEmailAlreadyRegistered = "EMAIL_ALREADY_REGISTERED"

// 定义 Handler 所依赖的 Service 接口
type RegistrationService interface {
	Register(ctx context.Context, input RegisterInput) (User, error)
}

type ProfileService interface {
	GetProfile(ctx context.Context, userID uint64) (User, error)
	UpdateProfile(ctx context.Context, userID uint64, input UpdateProfileInput) (User, error)
}

type UserService interface {
	RegistrationService
	ProfileService
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type updateProfileRequest struct {
	AIEnabled         *bool   `json:"ai_enabled"`
	AILanguage        *string `json:"ai_language"`
	Timezone          *string `json:"timezone"`
	DigestTime        *string `json:"digest_time"`
	MaxItemsPerDigest *uint16 `json:"max_items_per_digest"`
}

type Handler struct {
	service UserService  //执行用户业务
	logger  *slog.Logger //只记录无法预期的内部错误
}

func NewHandler(service UserService, logger *slog.Logger) *Handler {
	return &Handler{
		service: service,
		logger:  logger,
	}
}

func (handler *Handler) GetProfile(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		writeProfileUnauthorized(c)
		return
	}

	profile, err := handler.service.GetProfile(c.Request.Context(), userID)
	if err != nil {
		handler.writeProfileError(c, "get user profile failed", err)
		return
	}
	c.JSON(http.StatusOK, profile.Public())
}

func (handler *Handler) UpdateProfile(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		writeProfileUnauthorized(c)
		return
	}

	var request updateProfileRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	profile, err := handler.service.UpdateProfile(
		c.Request.Context(),
		userID,
		UpdateProfileInput{AIEnabled: request.AIEnabled, AILanguage: request.AILanguage,
			Timezone:          request.Timezone,
			DigestTime:        request.DigestTime,
			MaxItemsPerDigest: request.MaxItemsPerDigest,
		},
	)
	if err != nil {
		handler.writeProfileError(c, "update user profile failed", err)
		return
	}
	c.JSON(http.StatusOK, profile.Public())
}

func currentUserID(c *gin.Context) (uint64, bool) {
	userID, ok := httpx.CurrentUserID(c)
	return uint64(userID), ok && userID != 0
}

func writeProfileUnauthorized(c *gin.Context) {
	httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
}

func (handler *Handler) writeProfileError(c *gin.Context, message string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeProfileUnauthorized(c)
	case errors.Is(err, ErrEmptyProfileUpdate):
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "profile update is empty")
	case errors.Is(err, ErrInvalidAILanguage):
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "ai_language is invalid")
	case errors.Is(err, ErrInvalidTimezone):
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "timezone is invalid")
	case errors.Is(err, ErrInvalidDigestTime):
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "digest_time is invalid")
	case errors.Is(err, ErrInvalidMaxItemsPerDigest):
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "max_items_per_digest is invalid")
	case errors.Is(err, ErrAIConfigurationRequired):
		httpx.WriteError(c, http.StatusConflict, "AI_CONFIGURATION_REQUIRED", "configure an AI provider first")
	default:
		handler.logger.Error(
			message,
			"module", "user",
			"request_id", httpx.RequestID(c),
			"error", err,
		)
		httpx.WriteError(c, http.StatusInternalServerError, httpx.CodeInternalError, "internal server error")
	}
}

func (handler *Handler) Register(c *gin.Context) {
	//创建用于接收 JSON 的 HTTP 请求对象。
	var request registerRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	registeredUser, err := handler.service.Register(
		//当前 HTTP 请求的 Context 传给 Service，
		// 最终会继续传给 Repository 和数据库。
		// 如果客户端断开连接，数据库操作可以被取消。
		c.Request.Context(),

		//把 HTTP 层的 registerRequest 转成业务层的 RegisterInput。
		//这能避免 Service 与 JSON 格式耦合。
		RegisterInput{
			Email:    request.Email,
			Password: request.Password,
		},
	)
	if err != nil {
		handler.writeRegisterError(c, err)
		return
	}
	c.JSON(http.StatusCreated, registeredUser.Public())
}

// 错误映射函数
func (handler *Handler) writeRegisterError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrInvalidEmail):
		httpx.WriteError(
			c,
			http.StatusBadRequest,
			httpx.CodeValidationError,
			"email is invalid",
		)

	case errors.Is(err, ErrInvalidPassword):
		httpx.WriteError(
			c,
			http.StatusBadRequest,
			httpx.CodeValidationError,
			"password is invalid",
		)

	case errors.Is(err, ErrEmailAlreadyRegistered):
		httpx.WriteError(
			c,
			http.StatusConflict,
			CodeEmailAlreadyRegistered,
			"email is already registered",
		)

	default:
		handler.logger.Error(
			"register user failed",
			"module", "user",
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
}
