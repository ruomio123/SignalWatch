package paper

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

const CodePaperNotFound = "PAPER_NOT_FOUND"

type PaperQueryService interface {
	List(ctx context.Context, userID uint64, input QueryInput) (QueryPage, error)
	Get(ctx context.Context, userID, paperID uint64) (PublicPaper, error)
}

type QueryHandler struct {
	service PaperQueryService
	logger  *slog.Logger
}

func NewQueryHandler(service PaperQueryService, logger *slog.Logger) *QueryHandler {
	return &QueryHandler{service: service, logger: logger}
}

func (handler *QueryHandler) List(c *gin.Context) {
	userID, ok := httpx.CurrentUserID(c)
	if !ok || userID == 0 {
		httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		return
	}
	input, err := parsePaperQuery(c)
	if err != nil {
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "paper query is invalid")
		return
	}
	result, err := handler.service.List(c.Request.Context(), uint64(userID), input)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidQueryPagination), errors.Is(err, ErrInvalidQueryFilter):
			httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "paper query is invalid")
		case errors.Is(err, ErrInvalidQueryUser):
			httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		default:
			handler.logger.Error("list matched papers failed", "module", "paper", "error", err)
			httpx.WriteError(c, http.StatusInternalServerError, httpx.CodeInternalError, "internal server error")
		}
		return
	}
	c.JSON(http.StatusOK, result)
}

func (handler *QueryHandler) Get(c *gin.Context) {
	userID, ok := httpx.CurrentUserID(c)
	if !ok || userID == 0 {
		httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		return
	}
	paperID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || paperID == 0 {
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "paper id is invalid")
		return
	}
	result, err := handler.service.Get(c.Request.Context(), uint64(userID), paperID)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidQueryID):
			httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "paper id is invalid")
		case errors.Is(err, ErrQueryNotFound):
			httpx.WriteError(c, http.StatusNotFound, CodePaperNotFound, "paper not found")
		case errors.Is(err, ErrInvalidQueryUser):
			httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		default:
			handler.logger.Error("get matched paper failed", "module", "paper", "paper_id", paperID, "error", err)
			httpx.WriteError(c, http.StatusInternalServerError, httpx.CodeInternalError, "internal server error")
		}
		return
	}
	c.JSON(http.StatusOK, result)
}

func parsePaperQuery(c *gin.Context) (QueryInput, error) {
	page, err := parsePositiveQuery(c, "page", DefaultQueryPage)
	if err != nil {
		return QueryInput{}, err
	}
	pageSize, err := parsePositiveQuery(c, "page_size", DefaultQueryPageSize)
	if err != nil || pageSize > MaxQueryPageSize {
		return QueryInput{}, ErrInvalidQueryPagination
	}
	var subscriptionID *uint64
	if raw, exists := c.GetQuery("subscription_id"); exists {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			return QueryInput{}, ErrInvalidQueryFilter
		}
		subscriptionID = &parsed
	}
	return QueryInput{
		Page: page, PageSize: pageSize,
		Filter: QueryFilter{SubscriptionID: subscriptionID},
	}, nil
}

func parsePositiveQuery(c *gin.Context, name string, fallback int) (int, error) {
	raw, exists := c.GetQuery(name)
	if !exists {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, ErrInvalidQueryPagination
	}
	return value, nil
}
