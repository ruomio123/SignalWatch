package ai

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/paper"
	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/user"
)

type Handler struct {
	Service        *Service
	Configurations *ConfigurationService
	Calls          *CallRunner
	Enabled        bool
}

func (h Handler) Get(c *gin.Context)     { h.handleSummary(c, false) }
func (h Handler) Request(c *gin.Context) { h.handleSummary(c, true) }
func (h Handler) handleSummary(c *gin.Context, request bool) {
	uid, _ := httpx.CurrentUserID(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpx.WriteError(c, 400, httpx.CodeValidationError, "invalid paper id")
		return
	}
	lang := c.Query("language")
	if request {
		var input struct {
			Language string `json:"language"`
		}
		if !httpx.BindJSON(c, &input) {
			return
		}
		lang = input.Language
	}
	result, err := h.Service.Summary(c.Request.Context(), uint64(uid), id, lang, request)
	if err != nil {
		var callErr *CallError
		if errors.As(err, &callErr) {
			h.writeConfigurationError(c, err)
			return
		}
		switch {
		case errors.Is(err, ErrDisabled):
			httpx.WriteError(c, 503, "AI_DISABLED", "AI enrichment is disabled")
		case errors.Is(err, ErrConfigurationRequired):
			httpx.WriteError(c, 409, "AI_CONFIGURATION_REQUIRED", "configure an AI provider first")
		case errors.Is(err, ErrConfigurationInvalid):
			httpx.WriteError(c, 422, "AI_CONFIGURATION_INVALID", "AI provider configuration is invalid")
		case errors.Is(err, ErrModelUnavailable):
			httpx.WriteError(c, 422, "AI_MODEL_UNAVAILABLE", "saved AI model is no longer available")
		case errors.Is(err, ErrProviderDisabled):
			httpx.WriteError(c, 422, "AI_PROVIDER_DISABLED", "saved AI provider is currently disabled")
		case errors.Is(err, ErrLeaseLost):
			httpx.WriteError(c, 409, "AI_TASK_CONFLICT", "task changed; query current state")
		case errors.Is(err, ErrLanguage):
			httpx.WriteError(c, 400, httpx.CodeValidationError, "language must be zh or en")
		case errors.Is(err, ErrTaskNotFound), errors.Is(err, user.ErrNotFound), errors.Is(err, paper.ErrQueryNotFound):
			httpx.WriteError(c, 404, httpx.CodeNotFound, "paper not found")
		default:
			httpx.WriteError(c, 503, "AI_UNAVAILABLE", "AI enrichment temporarily unavailable")
		}
		return
	}
	status := http.StatusOK
	if request {
		for _, item := range result.Items {
			if item.State == "pending" {
				status = http.StatusAccepted
			}
		}
	}
	c.JSON(status, result)
}

type modelConfigurationRequest struct {
	Model string `json:"model"`
}
type secretConfigurationRequest struct {
	APIKey string `json:"api_key"`
}

func (h Handler) Providers(c *gin.Context) {
	if !h.Enabled || h.Configurations == nil {
		c.JSON(http.StatusOK, gin.H{"items": []any{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": h.Configurations.Providers()})
}
func (h Handler) GetConfiguration(c *gin.Context) {
	userID, ok := currentAIUser(c)
	if !ok {
		return
	}
	if !h.Enabled || h.Configurations == nil {
		c.JSON(http.StatusOK, PublicConfiguration{Configured: false})
		return
	}
	value, err := h.Configurations.Get(c.Request.Context(), userID)
	if err != nil {
		h.writeConfigurationError(c, err)
		return
	}
	if value.Configured {
		c.Header("ETag", configurationETag(value))
	}
	c.JSON(http.StatusOK, value)
}
func (h Handler) PutConfiguration(c *gin.Context) {
	userID, ok := currentAIUser(c)
	if !ok || !h.requireEnabled(c) {
		return
	}
	var request struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		APIKey   string `json:"api_key"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if !httpx.BindJSON(c, &request) {
		return
	}
	expected, present, valid := h.optionalIfMatch(c)
	if !valid {
		return
	}
	var expectedPtr *uint64
	if present {
		expectedPtr = &expected
	}
	value, err := h.Configurations.Put(c.Request.Context(), userID, strings.TrimSpace(request.Provider), strings.TrimSpace(request.Model), request.APIKey, expectedPtr)
	request.APIKey = ""
	if err != nil {
		h.writeConfigurationError(c, err)
		return
	}
	writeConfiguration(c, value)
}
func (h Handler) PatchConfiguration(c *gin.Context) {
	userID, ok := currentAIUser(c)
	if !ok || !h.requireEnabled(c) {
		return
	}
	expected, present, valid := h.optionalIfMatch(c)
	if !valid || !present {
		if valid {
			httpx.WriteError(c, 400, httpx.CodeValidationError, "If-Match is required")
		}
		return
	}
	var request modelConfigurationRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	value, err := h.Configurations.ChangeModel(c.Request.Context(), userID, expected, strings.TrimSpace(request.Model))
	if err != nil {
		h.writeConfigurationError(c, err)
		return
	}
	writeConfiguration(c, value)
}
func (h Handler) RotateSecret(c *gin.Context) {
	userID, ok := currentAIUser(c)
	if !ok || !h.requireEnabled(c) {
		return
	}
	expected, present, valid := h.optionalIfMatch(c)
	if !valid || !present {
		if valid {
			httpx.WriteError(c, 400, httpx.CodeValidationError, "If-Match is required")
		}
		return
	}
	var request secretConfigurationRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	if !httpx.BindJSON(c, &request) {
		return
	}
	value, err := h.Configurations.RotateSecret(c.Request.Context(), userID, expected, request.APIKey)
	request.APIKey = ""
	if err != nil {
		h.writeConfigurationError(c, err)
		return
	}
	writeConfiguration(c, value)
}
func (h Handler) TestConfiguration(c *gin.Context) {
	userID, ok := currentAIUser(c)
	if !ok || !h.requireEnabled(c) {
		return
	}
	value, err := h.Configurations.TestSaved(c.Request.Context(), userID)
	if err != nil {
		h.writeConfigurationError(c, err)
		return
	}
	writeConfiguration(c, value)
}
func (h Handler) DeleteConfiguration(c *gin.Context) {
	userID, ok := currentAIUser(c)
	if !ok || !h.requireEnabled(c) {
		return
	}
	expected, present, valid := h.optionalIfMatch(c)
	if !valid || !present {
		if valid {
			httpx.WriteError(c, 400, httpx.CodeValidationError, "If-Match is required")
		}
		return
	}
	if err := h.Configurations.Delete(c.Request.Context(), userID, expected); err != nil {
		h.writeConfigurationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h Handler) Usage(c *gin.Context) {
	userID, ok := currentAIUser(c)
	if !ok || !h.requireEnabled(c) {
		return
	}
	now := h.Calls.now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	oldest := today.AddDate(0, 0, -29)
	from, to := oldest, today
	var err error
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		from, err = time.Parse("2006-01-02", raw)
		if err != nil {
			httpx.WriteError(c, 400, httpx.CodeValidationError, "from is invalid")
			return
		}
	}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		to, err = time.Parse("2006-01-02", raw)
		if err != nil {
			httpx.WriteError(c, 400, httpx.CodeValidationError, "to is invalid")
			return
		}
	}
	if from.Before(oldest) || to.After(today) || to.Before(from) || to.Sub(from) > 29*24*time.Hour {
		httpx.WriteError(c, 400, httpx.CodeValidationError, "usage range must be at most 30 days")
		return
	}
	rows, err := h.Calls.store.Usage(c.Request.Context(), userID, from, to)
	if err != nil {
		h.writeConfigurationError(c, err)
		return
	}
	todayUsage, err := h.Calls.Today(c.Request.Context(), userID)
	if err != nil {
		h.writeConfigurationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"today": todayUsage, "timezone": "UTC", "from": from.Format("2006-01-02"), "to": to.Format("2006-01-02"), "items": rows})
}

func currentAIUser(c *gin.Context) (uint64, bool) {
	id, ok := httpx.CurrentUserID(c)
	if !ok || id == 0 {
		httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "authentication required")
		return 0, false
	}
	return uint64(id), true
}
func (h Handler) requireEnabled(c *gin.Context) bool {
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), callRequestKey{}, httpx.RequestID(c)))
	if h.Enabled && h.Configurations != nil {
		return true
	}
	httpx.WriteError(c, 503, "AI_DISABLED", "AI configuration is disabled")
	return false
}
func writeConfiguration(c *gin.Context, value PublicConfiguration) {
	c.Header("ETag", configurationETag(value))
	c.JSON(http.StatusOK, value)
}
func (h Handler) optionalIfMatch(c *gin.Context) (uint64, bool, bool) {
	values := c.Request.Header.Values("If-Match")
	if len(values) == 0 {
		return 0, false, true
	}
	if len(values) != 1 || len(values[0]) < 3 || values[0][0] != '"' || values[0][len(values[0])-1] != '"' {
		httpx.WriteError(c, 400, httpx.CodeValidationError, "If-Match is invalid")
		return 0, false, false
	}
	parts := strings.Split(values[0][1:len(values[0])-1], ":")
	if len(parts) != 2 {
		httpx.WriteError(c, 400, httpx.CodeValidationError, "If-Match must contain generation and version")
		return 0, false, false
	}
	value, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || value == 0 {
		httpx.WriteError(c, 400, httpx.CodeValidationError, "If-Match is invalid")
		return 0, false, false
	}
	uid, ok := currentAIUser(c)
	if !ok {
		return 0, false, false
	}
	current, err := h.Configurations.Get(c.Request.Context(), uid)
	if err != nil {
		h.writeConfigurationError(c, err)
		return 0, false, false
	}
	if !current.Configured || current.Generation != parts[0] || current.Version != value {
		h.writeConfigurationError(c, ErrConfigurationConflict)
		return 0, false, false
	}
	return value, true, true
}
func (h Handler) writeConfigurationError(c *gin.Context, err error) {
	var failure *CallError
	if errors.As(err, &failure) {
		code, status := publicCallError(failure.Code)
		response := gin.H{"code": code, "message": code, "request_id": httpx.RequestID(c)}
		if failure.CallID != "" {
			response["call_id"] = failure.CallID
		}
		if failure.RetryAt != nil {
			seconds := max(0, int(math.Ceil(time.Until(*failure.RetryAt).Seconds())))
			response["retry_after_seconds"] = seconds
			c.Header("Retry-After", strconv.Itoa(seconds))
			if failure.Code == "daily_limit" {
				response["reset_at"] = failure.RetryAt
			}
		}
		c.AbortWithStatusJSON(status, response)
		return
	}

	switch {
	case errors.Is(err, ErrInvalidCredentialName):
		httpx.WriteError(c, 400, httpx.CodeValidationError, "name must contain 1 to 80 characters")
	case errors.Is(err, ErrConfigurationRequired):
		httpx.WriteError(c, 409, "AI_CONFIGURATION_REQUIRED", "configure an AI provider first")
	case errors.Is(err, ErrConfigurationInvalid):
		httpx.WriteError(c, 422, "AI_CONFIGURATION_INVALID", "AI provider rejected the configuration")
	case errors.Is(err, ErrModelUnavailable):
		httpx.WriteError(c, 422, "AI_MODEL_UNAVAILABLE", "saved AI model is no longer available")
	case errors.Is(err, ErrProviderDisabled):
		httpx.WriteError(c, 400, "AI_PROVIDER_DISABLED", "provider or model is not enabled")
	case errors.Is(err, ErrUsageLimit):
		httpx.WriteError(c, 429, "AI_DAILY_LIMIT_REACHED", "daily AI limit reached")
	case errors.Is(err, ErrCallInProgress):
		httpx.WriteError(c, 409, "AI_CALL_IN_PROGRESS", "another AI call is already in progress")
	case errors.Is(err, ErrConfigurationConflict):
		httpx.WriteError(c, 409, "AI_CONFIGURATION_VERSION_CONFLICT", "AI configuration changed; reload and retry")
	case errors.Is(err, ErrInvalidSecret):
		httpx.WriteError(c, 400, httpx.CodeValidationError, "api_key is invalid")
	case errors.Is(err, ErrProviderUnavailable):
		httpx.WriteError(c, 502, "AI_PROVIDER_UNAVAILABLE", "AI provider request failed")
	default:
		httpx.WriteError(c, 503, "AI_UNAVAILABLE", "AI configuration temporarily unavailable")
	}
}

func configurationETag(c PublicConfiguration) string {
	return `"` + c.Generation + ":" + strconv.FormatUint(c.Version, 10) + `"`
}

func publicCallError(code string) (string, int) {
	switch code {
	case "rate_limited":
		return "AI_RATE_LIMITED", 429
	case "daily_limit":
		return "AI_DAILY_LIMIT_REACHED", 429
	case "call_in_progress":
		return "AI_CALL_IN_PROGRESS", 409
	case "credential_rejected":
		return "AI_CONFIGURATION_INVALID", 422
	case "model_access_denied":
		return "AI_MODEL_ACCESS_DENIED", 422
	case "provider_rate_limited":
		return "AI_PROVIDER_RATE_LIMITED", 429
	case "timeout":
		return "AI_PROVIDER_TIMEOUT", 504
	case "result_unknown":
		return "AI_RESULT_UNKNOWN", 502
	case "provider_unavailable":
		return "AI_PROVIDER_UNAVAILABLE", 502
	case "provider_rejected":
		return "AI_PROVIDER_REJECTED", 502
	case "transport_failed":
		return "AI_NETWORK_FAILED", 502
	case "output_truncated":
		return "AI_OUTPUT_TRUNCATED", 502
	case "invalid_response", "invalid_output":
		return "AI_INVALID_OUTPUT", 502
	default:
		return "AI_UNAVAILABLE", 503
	}
}
func (h Handler) ListCalls(c *gin.Context) {
	userID, ok := currentAIUser(c)
	if !ok || !h.requireEnabled(c) {
		return
	}
	feature, status := c.Query("feature"), c.Query("status")
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, e := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || e != nil || page < 1 || page > 100000 || size < 1 || size > 100 || (feature != "" && !validFeature(feature)) || (status != "" && status != "started" && status != "succeeded" && status != "failed" && status != "unknown") {
		httpx.WriteError(c, 400, httpx.CodeValidationError, "invalid call filters")
		return
	}
	result, err := h.Calls.store.List(c.Request.Context(), userID, feature, status, page, size)
	if err != nil {
		h.writeConfigurationError(c, err)
		return
	}
	c.JSON(200, result)
}

func (h Handler) Credentials(c *gin.Context) {
	uid, ok := currentAIUser(c)
	if !ok {
		return
	}
	if c.Request.Method == "POST" && !h.requireEnabled(c) {
		return
	}
	if !h.Enabled || h.Configurations == nil {
		c.JSON(200, gin.H{"items": []any{}})
		return
	}
	if c.Request.Method == "POST" {
		if !h.requireEnabled(c) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		var input struct {
			Name     string `json:"name"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
			APIKey   string `json:"api_key"`
		}
		if !httpx.BindJSON(c, &input) {
			return
		}
		value, err := h.Configurations.CreateCredential(c.Request.Context(), uid, input.Name, input.Provider, input.Model, input.APIKey)
		input.APIKey = ""
		if err != nil {
			h.writeConfigurationError(c, err)
			return
		}
		c.Header("ETag", configurationETag(value))
		c.JSON(http.StatusCreated, value)
		return
	}
	rows, err := h.Configurations.ListCredentials(c.Request.Context(), uid)
	if err != nil {
		h.writeConfigurationError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": rows})
}
func (h Handler) Credential(c *gin.Context) {
	uid, ok := currentAIUser(c)
	if !ok || !h.requireEnabled(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	id := c.Param("id")
	c.Request = c.Request.WithContext(CredentialIDContext(c.Request.Context(), id))
	switch c.Request.Method {
	case "GET":
		h.GetConfiguration(c)
	case "DELETE":
		h.DeleteConfiguration(c)
	case "POST":
		h.TestConfiguration(c)
	case "PUT":
		var input struct {
			Name   string `json:"name"`
			Model  string `json:"model"`
			APIKey string `json:"api_key"`
		}
		if !httpx.BindJSON(c, &input) {
			return
		}
		version, present, valid := h.optionalIfMatch(c)
		if !valid {
			return
		}
		if !present {
			httpx.WriteError(c, 400, httpx.CodeValidationError, "If-Match is required")
			return
		}
		value, err := h.Configurations.UpdateCredential(c.Request.Context(), uid, id, input.Name, input.Model, input.APIKey, version)
		input.APIKey = ""
		if err != nil {
			h.writeConfigurationError(c, err)
			return
		}
		writeConfiguration(c, value)
	}
}
func (h Handler) DefaultSelection(c *gin.Context) {
	if c.Request.Method == "GET" {
		h.GetConfiguration(c)
		return
	}
	uid, ok := currentAIUser(c)
	if !ok || !h.requireEnabled(c) {
		return
	}
	var input struct {
		Provider   string `json:"provider"`
		Model      string `json:"model"`
		Generation string `json:"generation"`
		Version    uint64 `json:"version"`
	}
	if !httpx.BindJSON(c, &input) {
		return
	}
	ctx := CredentialIDContext(c.Request.Context(), input.Generation)
	current, err := h.Configurations.Get(ctx, uid)
	if err == nil && (current.Generation != input.Generation || current.Version != input.Version || current.ProviderID != input.Provider) {
		err = ErrConfigurationConflict
	}
	if err == nil && input.Model != "" && input.Model != current.ModelID {
		current, err = h.Configurations.ChangeModel(ctx, uid, input.Version, input.Model)
	}
	if err == nil {
		err = h.Configurations.SelectDefault(ctx, uid, input.Provider, current.Generation, current.Version)
	}
	if err != nil {
		h.writeConfigurationError(c, err)
		return
	}
	current.IsDefault = true
	writeConfiguration(c, current)
}
