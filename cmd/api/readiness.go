package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

const (
	// readinessTimeout 限制一次完整就绪检查的总耗时。
	// MySQL 和 Redis 共用同一个 Context，而不是各自拥有两秒超时。
	readinessTimeout = 2 * time.Second
	readyResponse    = `{"status":"ready"}`
	notReadyResponse = `{"status":"not_ready"}`
)

// dependencyCheck 是一个具名函数类型，用于描述外部依赖检查。
// 检查成功时返回 nil，检查失败时返回具体错误。
type dependencyCheck func(context.Context) error

/*
newReadinessHandler 创建处理 GET /readyz 请求的 Handler。
参数：
  - logger：记录依赖检查失败和响应写入失败。
  - mysqlCheck：检查 MySQL 是否可用。
  - redisCheck：检查 Redis 是否可用。

返回值：
  - 可注册到 http.ServeMux 的 http.HandlerFunc。

安全要求：
  - 底层错误只记录到服务端日志。
  - 返回给客户端的响应只包含 ready 或 not_ready。
*/
func newReadinessHandler(
	logger *slog.Logger,
	mysqlCheck dependencyCheck,
	redisCheck dependencyCheck,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 从请求 Context 派生一个带超时的 Context。
		// 客户端断开连接或检查超时时，依赖检查都能及时结束。
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		// Handler 返回时释放与超时 Context 相关的资源。
		defer cancel()

		// 首先检查 MySQL；失败时只把底层错误写入服务端日志。
		if err := mysqlCheck(ctx); err != nil {
			logger.Error(
				"mysql readiness check failed",
				"module", "readiness",
				"error", err,
			)

			writeReadinessResponse(
				w,
				logger,
				http.StatusServiceUnavailable,
				notReadyResponse,
			)
			return
		}

		// Redis 继续使用与 MySQL 相同的 ctx，因此共享同一个总超时时间。
		if err := redisCheck(ctx); err != nil {
			logger.Error(
				"redis readiness check failed",
				"module", "readiness",
				"error", err,
			)
			writeReadinessResponse(
				w,
				logger,
				http.StatusServiceUnavailable,
				notReadyResponse,
			)
			return
		}

		// 只有 MySQL 和 Redis 都可用时，API 才处于 ready 状态。
		writeReadinessResponse(
			w,
			logger,
			http.StatusOK,
			readyResponse,
		)
	}
}

/*
writeReadinessResponse 统一写入就绪检查的 JSON 响应。

这样可以避免成功和失败分支分别重复设置响应头、状态码和响应体。
*/
func writeReadinessResponse(
	w http.ResponseWriter,
	logger *slog.Logger,
	statusCode int,
	body string,
) {
	// Content-Type 必须在 WriteHeader 之前设置，否则响应头可能已经发出。
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if _, err := w.Write([]byte(body)); err != nil {
		logger.Error(
			"write readiness response failed",
			"module", "readiness",
			"error", err,
		)
	}
}
