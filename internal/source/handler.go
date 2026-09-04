package source

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

const CodeSourceNotFound = "SOURCE_NOT_FOUND"

type CatalogService interface {
	List(ctx context.Context) ([]PublicSource, error)
	Get(ctx context.Context, id uint64) (PublicSource, error)
}

type Handler struct {
	service CatalogService
	logger  *slog.Logger
}

func NewHandler(service CatalogService, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

func (handler *Handler) List(c *gin.Context) {
	sources, err := handler.service.List(c.Request.Context())
	if err != nil {
		handler.writeInternalError(c, "list sources failed", err)
		return
	}
	c.JSON(http.StatusOK, sources)
}

func (handler *Handler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "source id is invalid")
		return
	}

	found, err := handler.service.Get(c.Request.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidID):
			httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "source id is invalid")
		case errors.Is(err, ErrNotFound):
			httpx.WriteError(c, http.StatusNotFound, CodeSourceNotFound, "source not found")
		default:
			handler.writeInternalError(c, "get source failed", err)
		}
		return
	}
	c.JSON(http.StatusOK, found)
}

func (handler *Handler) writeInternalError(c *gin.Context, message string, err error) {
	handler.logger.Error(
		message,
		"module", "source",
		"request_id", httpx.RequestID(c),
		"error", err,
	)
	httpx.WriteError(c, http.StatusInternalServerError, httpx.CodeInternalError, "internal server error")
}
