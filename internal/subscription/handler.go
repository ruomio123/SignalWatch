package subscription

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/source"
)

const CodeSubscriptionLimitReached = "SUBSCRIPTION_LIMIT_REACHED"

type CreationService interface {
	Create(ctx context.Context, userID uint64, input CreateInput) (PublicSubscription, error)
}

type createRequest struct {
	SourceID  uint64             `json:"source_id"`
	Name      string             `json:"name"`
	Objective *string            `json:"objective"`
	Enabled   *bool              `json:"enabled"`
	Rules     createRulesRequest `json:"rules"`
}

type createRulesRequest struct {
	Categories      []string `json:"categories"`
	Authors         []string `json:"authors"`
	IncludeKeywords []string `json:"include_keywords"`
	ExcludeKeywords []string `json:"exclude_keywords"`
}

type Handler struct {
	service CreationService
	logger  *slog.Logger
}

func NewHandler(service CreationService, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

func (handler *Handler) Create(c *gin.Context) {
	userID, ok := httpx.CurrentUserID(c)
	if !ok || userID == 0 {
		httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		return
	}

	var request createRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	created, err := handler.service.Create(c.Request.Context(), uint64(userID), CreateInput{
		SourceID:  request.SourceID,
		Name:      request.Name,
		Objective: request.Objective,
		Enabled:   request.Enabled,
		Rules: RulesInput{
			Categories:      request.Rules.Categories,
			Authors:         request.Rules.Authors,
			IncludeKeywords: request.Rules.IncludeKeywords,
			ExcludeKeywords: request.Rules.ExcludeKeywords,
		},
	})
	if err != nil {
		handler.writeCreateError(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (handler *Handler) writeCreateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, source.ErrNotFound):
		httpx.WriteError(c, http.StatusNotFound, source.CodeSourceNotFound, "source not found")
	case errors.Is(err, ErrLimitReached):
		httpx.WriteError(c, http.StatusConflict, CodeSubscriptionLimitReached, "enabled subscription limit reached")
	case errors.Is(err, ErrInvalidSourceID),
		errors.Is(err, source.ErrInvalidID),
		errors.Is(err, ErrInvalidName),
		errors.Is(err, ErrInvalidObjective),
		errors.Is(err, ErrInvalidRule),
		errors.Is(err, ErrDuplicateRule),
		errors.Is(err, ErrRuleLimit),
		errors.Is(err, ErrCategoryRequired),
		errors.Is(err, ErrCategoryNotAllowed),
		errors.Is(err, ErrRuleNotSupported):
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "subscription is invalid")
	case errors.Is(err, ErrUserNotFound):
		httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
	default:
		handler.logger.Error(
			"create subscription failed",
			"module", "subscription",
			"request_id", httpx.RequestID(c),
			"error", err,
		)
		httpx.WriteError(c, http.StatusInternalServerError, httpx.CodeInternalError, "internal server error")
	}
}
