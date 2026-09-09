package httpx

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Context 中保存的是一个请求范围内的临时数据。
// 这里统一定义内部键名，避免请求 ID、用户 ID 等数据在不同
const (
	contextKeyRequestID     = "httpx.request_id"
	contextKeyCurrentUserID = "httpx.current_user_id"
	contextKeyCurrentRole   = "httpx.current_role"
)

// 这些是 HTTP 基座可以直接识别的通用错误码。
// HTTP 状态码用于描述协议层结果，例如 400、404、500；
// 后续用户、订阅等业务模块可以再定义自己的业务错误码。
const (
	CodeValidationError          = "VALIDATION_ERROR"
	CodeNotFound                 = "NOT_FOUND"
	CodeMethodNotAllowed         = "METHOD_NOT_ALLOWED"
	CodeInternalError            = "INTERNAL_ERROR"
	CodeUnauthorized             = "AUTH_UNAUTHORIZED"
	CodeForbidden                = "AUTH_FORBIDDEN"
	CodeAuthorizationUnavailable = "AUTHORIZATION_UNAVAILABLE"
	CodeServiceUnavailable       = "SERVICE_UNAVAILABLE"
)

/*
ErrorResponse 定义所有 HTTP 错误响应共用的 JSON 结构。
  - Code：供客户端程序判断错误类型，不依赖可能变化的文字描述。
  - Message：向客户端提供安全、可读的错误说明。
  - RequestID：把客户端看到的错误与服务端日志关联起来。

示例：

	{
	    "code": "VALIDATION_ERROR",
	    "message": "request is invalid",
	    "request_id": "abc123"
	}
*/
type ErrorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

/*
WriteError 向客户端写入统一格式的 JSON 错误，并终止后续 Handler。
参数：
  - c：当前请求的 Gin Context，用于读取请求 ID和写入响应。
  - status：HTTP 状态码，例如 400、404 或 500。
  - code：稳定的机器可读错误码，例如 VALIDATION_ERROR。
  - message：可以安全暴露给客户端的错误说明。

返回值：
  - 无。函数会直接把 HTTP 状态码和 JSON 正文写入响应。
*/
func WriteError(c *gin.Context, status int, code string, message string) {
	c.AbortWithStatusJSON(status, ErrorResponse{
		Code:      code,
		Message:   message,
		RequestID: RequestID(c),
	})
}

/*
RequestID 从当前请求的 Gin Context 中读取请求 ID。
参数：
  - c：当前请求的 Gin Context。

返回值：
  - 成功时返回请求 ID。
  - 键不存在或保存的值不是 string 时返回空字符串。
*/
func RequestID(c *gin.Context) string {
	value, exists := c.Get(contextKeyRequestID)
	if !exists {
		return ""
	}

	requestID, ok := value.(string)
	if !ok {
		return ""
	}

	return requestID
}

/*
UserID 是系统中用户主键的强类型表示。
*/
type UserID uint64

/*
SetCurrentUserID 将已经通过鉴权的用户 ID 保存到当前请求的 Context。
参数：
  - c：当前请求的 Gin Context。
  - userID：已经通过鉴权确认的用户 ID。

返回值：
  - 无。用户 ID 只在当前请求的生命周期内有效，不会跨请求共享。
*/
func SetCurrentUserID(c *gin.Context, userID UserID) {
	c.Set(contextKeyCurrentUserID, userID)
}

/*
CurrentUserID 从当前请求的 Gin Context 中读取已经认证的用户 ID。
参数：
  - c：当前请求的 Gin Context。

返回值：
  - userID：读取成功时为当前用户 ID；失败时为 UserID 的零值 0。
  - ok：只有 Context 中存在该值且值的类型确实为 UserID 时才为 true。
*/
func CurrentUserID(c *gin.Context) (userID UserID, ok bool) {
	value, exists := c.Get(contextKeyCurrentUserID)
	if !exists {
		return 0, false
	}

	userID, ok = value.(UserID)
	return userID, ok
}

func SetCurrentRole(c *gin.Context, role string) {
	c.Set(contextKeyCurrentRole, role)
}

func CurrentRole(c *gin.Context) (string, bool) {
	value, exists := c.Get(contextKeyCurrentRole)
	if !exists {
		return "", false
	}
	role, ok := value.(string)
	return role, ok
}

/*
BindJSON 严格解码当前请求的 JSON 正文，并统一处理解码错误。
参数：
  - c：当前请求的 Gin Context，提供请求体并用于写入错误响应。
  - dst：用于接收解码结果的目标值，必须传入非 nil 指针，
    例如 &input，而不是 input。

返回值：
  - true：请求体是唯一且合法的 JSON，解码结果已经写入 dst。
  - false：请求体不符合要求，函数已经向客户端写入 400 错误；
    调用方应立即 return，不再继续处理当前请求。
*/
func BindJSON(c *gin.Context, dst any) bool {
	decoder := json.NewDecoder(c.Request.Body)

	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		WriteError(
			c,
			http.StatusBadRequest,
			CodeValidationError,
			"request is invalid",
		)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		WriteError(
			c,
			http.StatusBadRequest,
			CodeValidationError,
			"request is invalid",
		)
		return false
	}
	return true
}
