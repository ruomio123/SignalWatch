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
type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type Handler struct {
	service RegistrationService //执行注册业务
	logger  *slog.Logger        //只记录无法预期的内部错误
}

func NewHandler(service RegistrationService, logger *slog.Logger) *Handler {
	return &Handler{
		service: service,
		logger:  logger,
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
