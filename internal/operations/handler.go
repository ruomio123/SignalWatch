package operations

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

type Handler struct {
	service *Service
	logger  *slog.Logger
}

func NewHandler(service *Service, logger *slog.Logger) (*Handler, error) {
	if service == nil || logger == nil {
		return nil, errors.New("invalid operations handler configuration")
	}
	return &Handler{service: service, logger: logger}, nil
}

func (handler *Handler) Status(c *gin.Context) {
	result, err := handler.service.Status(c.Request.Context())
	if err != nil {
		handler.logger.Error("operations status assembly failed", "module", "operations", "event", "ops_status_failed",
			"request_id", httpx.RequestID(c), "error", err)
		httpx.WriteError(c, http.StatusServiceUnavailable, httpx.CodeServiceUnavailable, "operational status temporarily unavailable")
		return
	}
	c.JSON(http.StatusOK, result)
}

func (handler *Handler) Sources(c *gin.Context) {
	result, err := handler.service.Sources(c.Request.Context())
	if err != nil {
		handler.logger.Error("operations source status failed", "module", "operations", "event", "ops_sources_failed",
			"request_id", httpx.RequestID(c), "error", err)
		httpx.WriteError(c, http.StatusServiceUnavailable, httpx.CodeServiceUnavailable, "operational status temporarily unavailable")
		return
	}
	c.JSON(http.StatusOK, result)
}

func AuditMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		operatorID, _ := httpx.CurrentUserID(c)
		logger.Info("operator API accessed", "module", "operations", "event", "operator_api_access",
			"operator_user_id", operatorID, "request_id", httpx.RequestID(c),
			"method", c.Request.Method, "path", c.Request.URL.Path,
			"status", c.Writer.Status(), "duration_ms", time.Since(started).Milliseconds())
	}
}
