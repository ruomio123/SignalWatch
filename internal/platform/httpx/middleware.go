package httpx

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
)

// 客户端和服务端关联同一次请求所使用的响应头
const HeaderRequestID = "X-Request-ID"

// 中间件
/*
给每一次 HTTP 请求生成一个唯一的 Request ID，
并把它保存到 Gin 上下文中，同时返回给客户端。
*/
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := rand.Text()
		//生成一个随机字符串，作为这次请求的唯一标识。
		c.Set(contextKeyRequestID, requestID)
		c.Header(HeaderRequestID, requestID)
		c.Next() //执行后面的函数
	}
}

// 捕获请求处理期间发生的 panic。
func RecoveryMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()

		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			logger.Error(
				"panic recovered",
				"module", "http",
				"request_id", RequestID(c),
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
				"error", fmt.Sprint(recovered),
				"stack", string(debug.Stack()),
			)
			if c.Writer.Written() {
				c.Abort()
			} else {
				WriteError(
					c,
					http.StatusInternalServerError,
					CodeInternalError,
					"internal server error",
				)
			}
			// AccessLogMiddleware 的后置代码会被 panic 跳过，
			// 因此 panic 请求在 Recovery 写完响应后记录访问日志。
			logAccess(logger, c, startedAt)
		}()
		c.Next()
	}
}

// AccessLogMiddleware 在请求处理完成后记录基础访问日志。
func AccessLogMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		c.Next()
		logAccess(logger, c, startedAt)
	}
}

func logAccess(logger *slog.Logger, c *gin.Context, startedAt time.Time) {
	logger.Info(
		"http request completed",
		"module", "http",
		"request_id", RequestID(c),
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"status", c.Writer.Status(),
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
}
