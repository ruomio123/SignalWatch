package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

const readinessTimeout = 2 * time.Second

// DependencyCheck 描述一次外部依赖可用性检查。
type DependencyCheck func(context.Context) error

type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

type readinessResponse struct {
	Status string `json:"status"`
}

func healthHandler(serviceName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, healthResponse{
			Status:  "ok",
			Service: serviceName,
		})
	}
}

func readinessHandler(
	logger *slog.Logger,
	mysqlCheck DependencyCheck,
	redisCheck DependencyCheck,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(
			c.Request.Context(),
			readinessTimeout,
		)
		defer cancel()

		if err := mysqlCheck(ctx); err != nil {
			logReadinessFailure(logger, c, "mysql", err)

			c.JSON(
				http.StatusServiceUnavailable,
				readinessResponse{Status: "not_ready"},
			)
			return
		}

		if err := redisCheck(ctx); err != nil {
			logReadinessFailure(logger, c, "redis", err)

			c.JSON(
				http.StatusServiceUnavailable,
				readinessResponse{Status: "not_ready"},
			)
			return
		}

		c.JSON(
			http.StatusOK,
			readinessResponse{Status: "ready"},
		)
	}
}

func logReadinessFailure(
	logger *slog.Logger,
	c *gin.Context,
	dependency string,
	err error,
) {
	logger.Error(
		"readiness check failed",
		"module", "readiness",
		"dependency", dependency,
		"request_id", httpx.RequestID(c),
		"error", err,
	)
}
