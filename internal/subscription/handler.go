package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/source"
)

const (
	CodeSubscriptionLimitReached    = "SUBSCRIPTION_LIMIT_REACHED"
	CodeSubscriptionNotFound        = "SUBSCRIPTION_NOT_FOUND"
	CodeSubscriptionVersionConflict = "SUBSCRIPTION_VERSION_CONFLICT"
)

type CreationService interface {
	Create(ctx context.Context, userID uint64, input CreateInput) (PublicSubscription, error)
}

type QueryService interface {
	List(ctx context.Context, userID uint64, input ListInput) (ListResult, error)
	Get(ctx context.Context, userID, id uint64) (PublicSubscription, error)
}

type MutationService interface {
	Update(ctx context.Context, userID, id uint64, expectedVersion uint32, input UpdateInput) (PublicSubscription, error)
	Delete(ctx context.Context, userID, id uint64, expectedVersion uint32) error
}

type SubscriptionService interface {
	CreationService
	QueryService
	MutationService
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
	IncludeKeywords []string `json:"include_keywords"`
}

type updateRequest struct {
	Name      json.RawMessage `json:"name"`
	Objective json.RawMessage `json:"objective"`
	Enabled   json.RawMessage `json:"enabled"`
	Rules     json.RawMessage `json:"rules"`
}

type updateRulesRequest struct {
	Categories      json.RawMessage `json:"categories"`
	IncludeKeywords json.RawMessage `json:"include_keywords"`
}

type Handler struct {
	service SubscriptionService
	logger  *slog.Logger
}

func NewHandler(service SubscriptionService, logger *slog.Logger) *Handler {
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
			IncludeKeywords: request.Rules.IncludeKeywords,
		},
	})
	if err != nil {
		handler.writeCreateError(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (handler *Handler) List(c *gin.Context) {
	userID, ok := httpx.CurrentUserID(c)
	if !ok || userID == 0 {
		httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		return
	}

	input, err := parseListInput(c)
	if err != nil {
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "subscription query is invalid")
		return
	}
	result, err := handler.service.List(c.Request.Context(), uint64(userID), input)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidPagination), errors.Is(err, ErrInvalidFilter):
			httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "subscription query is invalid")
		case errors.Is(err, ErrUserNotFound):
			httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		default:
			handler.writeInternalError(c, "list subscriptions failed", err)
		}
		return
	}
	c.JSON(http.StatusOK, result)
}

func (handler *Handler) Get(c *gin.Context) {
	userID, ok := httpx.CurrentUserID(c)
	if !ok || userID == 0 {
		httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "subscription id is invalid")
		return
	}
	found, err := handler.service.Get(c.Request.Context(), uint64(userID), id)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidID):
			httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "subscription id is invalid")
		case errors.Is(err, ErrNotFound):
			httpx.WriteError(c, http.StatusNotFound, CodeSubscriptionNotFound, "subscription not found")
		case errors.Is(err, ErrUserNotFound):
			httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		default:
			handler.writeInternalError(c, "get subscription failed", err)
		}
		return
	}
	c.Header("ETag", fmt.Sprintf(`"%d"`, found.Version))
	c.JSON(http.StatusOK, found)
}

func (handler *Handler) Update(c *gin.Context) {
	userID, ok := httpx.CurrentUserID(c)
	if !ok || userID == 0 {
		httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		return
	}

	id, ok := parseSubscriptionID(c)
	if !ok {
		return
	}
	expectedVersion, ok := parseIfMatch(c)
	if !ok {
		return
	}

	var request updateRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	input, err := request.input()
	if err != nil {
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "subscription is invalid")
		return
	}
	updated, err := handler.service.Update(
		c.Request.Context(),
		uint64(userID),
		id,
		expectedVersion,
		input,
	)
	if err != nil {
		handler.writeMutationError(c, "update subscription failed", err)
		return
	}
	c.Header("ETag", fmt.Sprintf(`"%d"`, updated.Version))
	c.JSON(http.StatusOK, updated)
}

func (handler *Handler) Delete(c *gin.Context) {
	userID, ok := httpx.CurrentUserID(c)
	if !ok || userID == 0 {
		httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		return
	}

	id, ok := parseSubscriptionID(c)
	if !ok {
		return
	}
	expectedVersion, ok := parseIfMatch(c)
	if !ok {
		return
	}
	if err := handler.service.Delete(c.Request.Context(), uint64(userID), id, expectedVersion); err != nil {
		handler.writeMutationError(c, "delete subscription failed", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func parseSubscriptionID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "subscription id is invalid")
		return 0, false
	}
	return id, true
}

func parseIfMatch(c *gin.Context) (uint32, bool) {
	values := c.Request.Header.Values("If-Match")
	if len(values) != 1 {
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "If-Match is invalid")
		return 0, false
	}
	raw := values[0]
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "If-Match is invalid")
		return 0, false
	}
	parsed, err := strconv.ParseUint(raw[1:len(raw)-1], 10, 32)
	if err != nil || parsed == 0 {
		httpx.WriteError(c, http.StatusBadRequest, httpx.CodeValidationError, "If-Match is invalid")
		return 0, false
	}
	return uint32(parsed), true
}

func (request updateRequest) input() (UpdateInput, error) {
	if request.Name == nil && request.Objective == nil && request.Enabled == nil && request.Rules == nil {
		return UpdateInput{}, ErrEmptyUpdate
	}
	var input UpdateInput
	if request.Name != nil {
		var name string
		if err := decodeStrictJSON(request.Name, &name); err != nil {
			return UpdateInput{}, err
		}
		input.Name = &name
	}
	if request.Objective != nil {
		input.ObjectiveSet = true
		if !bytes.Equal(bytes.TrimSpace(request.Objective), []byte("null")) {
			var objective string
			if err := decodeStrictJSON(request.Objective, &objective); err != nil {
				return UpdateInput{}, err
			}
			input.Objective = &objective
		}
	}
	if request.Enabled != nil {
		if bytes.Equal(bytes.TrimSpace(request.Enabled), []byte("null")) {
			return UpdateInput{}, ErrInvalidFilter
		}
		var enabled bool
		if err := decodeStrictJSON(request.Enabled, &enabled); err != nil {
			return UpdateInput{}, err
		}
		input.Enabled = &enabled
	}
	if request.Rules != nil {
		rules, err := parseUpdateRules(request.Rules)
		if err != nil {
			return UpdateInput{}, err
		}
		input.Rules = &rules
	}
	return input, nil
}

func parseUpdateRules(raw json.RawMessage) (RulesInput, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return RulesInput{}, ErrInvalidRule
	}
	var request updateRulesRequest
	if err := decodeStrictJSON(raw, &request); err != nil {
		return RulesInput{}, err
	}
	if request.Categories == nil || bytes.Equal(bytes.TrimSpace(request.Categories), []byte("null")) {
		return RulesInput{}, ErrInvalidRule
	}

	var result RulesInput
	if err := decodeStrictJSON(request.Categories, &result.Categories); err != nil {
		return RulesInput{}, err
	}
	result.IncludeKeywords = []string{}
	if request.IncludeKeywords != nil {
		if bytes.Equal(bytes.TrimSpace(request.IncludeKeywords), []byte("null")) {
			return RulesInput{}, ErrInvalidRule
		}
		if err := decodeStrictJSON(request.IncludeKeywords, &result.IncludeKeywords); err != nil {
			return RulesInput{}, err
		}
	}
	return result, nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func parseListInput(c *gin.Context) (ListInput, error) {
	page, err := parsePositiveIntQuery(c, "page", DefaultPage)
	if err != nil {
		return ListInput{}, err
	}
	pageSize, err := parsePositiveIntQuery(c, "page_size", DefaultPageSize)
	if err != nil || pageSize > MaxPageSize {
		return ListInput{}, ErrInvalidPagination
	}

	var enabled *bool
	if raw, exists := c.GetQuery("enabled"); exists {
		var parsed bool
		switch raw {
		case "true":
			parsed = true
		case "false":
			parsed = false
		default:
			return ListInput{}, ErrInvalidFilter
		}
		enabled = &parsed
	}

	var sourceID *uint64
	if raw, exists := c.GetQuery("source_id"); exists {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			return ListInput{}, ErrInvalidFilter
		}
		sourceID = &parsed
	}
	return ListInput{
		Page: page, PageSize: pageSize,
		Filter: ListFilter{Enabled: enabled, SourceID: sourceID},
	}, nil
}

func parsePositiveIntQuery(c *gin.Context, name string, defaultValue int) (int, error) {
	raw, exists := c.GetQuery(name)
	if !exists {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, ErrInvalidPagination
	}
	return value, nil
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
		handler.writeInternalError(c, "create subscription failed", err)
	}
}

func (handler *Handler) writeMutationError(c *gin.Context, logMessage string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(c, http.StatusNotFound, CodeSubscriptionNotFound, "subscription not found")
	case errors.Is(err, ErrVersionConflict):
		httpx.WriteError(c, http.StatusConflict, CodeSubscriptionVersionConflict, "subscription version conflict")
	case errors.Is(err, ErrLimitReached):
		httpx.WriteError(c, http.StatusConflict, CodeSubscriptionLimitReached, "enabled subscription limit reached")
	case errors.Is(err, ErrInvalidID),
		errors.Is(err, ErrInvalidVersion),
		errors.Is(err, ErrEmptyUpdate),
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
		handler.writeInternalError(c, logMessage, err)
	}
}

func (handler *Handler) writeInternalError(c *gin.Context, message string, err error) {
	handler.logger.Error(
		message,
		"module", "subscription",
		"request_id", httpx.RequestID(c),
		"error", err,
	)
	httpx.WriteError(c, http.StatusInternalServerError, httpx.CodeInternalError, "internal server error")
}
