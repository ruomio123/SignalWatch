// Package llm implements the allowlisted external Chat Completions providers.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"signalwatch/internal/generation"
	"strconv"
	"strings"
	"time"
)

type Result = generation.Result
type Failure = generation.Failure

type Client struct {
	provider, endpoint, model, key string
	configure                      requestConfigurer
	http                           *http.Client
}

type Model = generation.Model
type Provider = generation.Provider

const UnavailableProviderDisabled = generation.UnavailableProviderDisabled
const UnavailableModelRetired = generation.UnavailableModelRetired

type requestConfigurer func(map[string]any, int)

type providerSpec struct {
	name, base string
	models     []Model
	configure  requestConfigurer
}

var providers = map[string]providerSpec{
	"glm": {
		name: "智谱 GLM", base: "https://open.bigmodel.cn/api/paas/v4",
		models:    []Model{{ID: "glm-4.7-flash", Name: "GLM 4.7 Flash", Default: true}, {ID: "glm-5.2", Name: "GLM 5.2"}},
		configure: configureThinkingWithMaxTokens,
	},
	"qwen": {
		name: "阿里云百炼 Qwen", base: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		models:    []Model{{ID: "qwen3.8-flash", Name: "Qwen 3.8 Flash", Default: true}, {ID: "qwen3.8-max", Name: "Qwen 3.8 Max"}},
		configure: configureQwen,
	},
	"deepseek": {
		name: "DeepSeek", base: "https://api.deepseek.com",
		models:    []Model{{ID: "deepseek-flash", Name: "DeepSeek Flash", Default: true}},
		configure: configureThinkingWithMaxTokens,
	},
	"openai": {
		name: "OpenAI API", base: "https://api.openai.com/v1",
		models:    []Model{{ID: "gpt-5.6-luna", Name: "GPT 5.6 Luna", Default: true}, {ID: "gpt-5.6-terra", Name: "GPT 5.6 Terra"}},
		configure: configureOpenAI,
	},
	"kimi": {
		name: "Kimi（月之暗面）", base: "https://api.moonshot.cn/v1",
		models:    []Model{{ID: "kimi-k2.6", Name: "Kimi K2.6", Default: true}},
		configure: configureKimi,
	},
}

func configureCompatible(payload map[string]any, maxTokens int) {
	payload["max_tokens"] = maxTokens
}

func configureThinkingWithMaxTokens(payload map[string]any, maxTokens int) {
	payload["max_tokens"] = maxTokens
	payload["thinking"] = map[string]string{"type": "disabled"}
}

func configureQwen(payload map[string]any, maxTokens int) {
	payload["max_completion_tokens"] = maxTokens
	payload["enable_thinking"] = false
	payload["preserve_thinking"] = false
}

func configureOpenAI(payload map[string]any, maxTokens int) {
	payload["max_completion_tokens"] = maxTokens
	payload["reasoning_effort"] = "none"
}

func configureKimi(payload map[string]any, maxTokens int) {
	payload["max_completion_tokens"] = maxTokens
	payload["thinking"] = map[string]string{"type": "disabled"}
}

func enabledProvider(enabled []string, provider string) bool {
	for _, id := range enabled {
		if id == provider {
			return true
		}
	}
	return false
}

// SelectionAvailability distinguishes a deployment-disabled provider from a
// model that was saved while it was allowlisted but has since been retired.
func SelectionAvailability(enabled []string, provider, model string) (bool, string) {
	p, known := providers[provider]
	if !known || !enabledProvider(enabled, provider) {
		return false, UnavailableProviderDisabled
	}
	for _, item := range p.models {
		if item.ID == model {
			return true, ""
		}
	}
	return false, UnavailableModelRetired
}

func Providers(enabled []string) []Provider {
	result := make([]Provider, 0, len(enabled))
	for _, id := range enabled {
		if p, ok := providers[id]; ok {
			models := append([]Model(nil), p.models...)
			result = append(result, Provider{ID: id, Name: p.name, Models: models})
		}
	}
	return result
}

func ValidateSelection(enabled []string, provider, model string) bool {
	available, _ := SelectionAvailability(enabled, provider, model)
	return available
}

func NewProviderClient(enabled []string, provider, model, key string) (*Client, error) {
	if !ValidateSelection(enabled, provider, model) || strings.TrimSpace(key) == "" {
		return nil, errors.New("invalid provider configuration")
	}
	p := providers[provider]
	return newClient(provider, p.base, model, key, p.configure, nil)
}

func New(base, model, key string) (*Client, error) {
	return newClient("compatible", base, model, key, configureCompatible, nil)
}

func newClient(provider, base, model, key string, configure requestConfigurer, client *http.Client) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) || strings.TrimSpace(model) == "" || strings.TrimSpace(key) == "" {
		return nil, errors.New("invalid LLM configuration")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if configure == nil {
		return nil, errors.New("invalid LLM configuration")
	}
	return &Client{provider: provider, endpoint: strings.TrimRight(base, "/") + "/chat/completions", model: model, key: key, configure: configure, http: client}, nil
}
func (c *Client) Generate(ctx context.Context, system string, input []byte) (Result, error) {
	return c.GenerateLimit(ctx, system, input, 4096)
}

func (c *Client) GenerateLimit(ctx context.Context, system string, input []byte, maxTokens int) (Result, error) {
	return c.GenerateStructured(ctx, system, input, maxTokens, nil)
}

func (c *Client) GenerateStructured(ctx context.Context, system string, input []byte, maxTokens int, schema *generation.Schema) (Result, error) {
	if len(input) > 100000 {
		return Result{}, &Failure{Code: "input_too_large"}
	}
	if maxTokens < 1 || maxTokens > 8192 {
		return Result{}, &Failure{Code: "invalid_request"}
	}
	payload := map[string]any{
		"model":           c.model,
		"messages":        []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": string(input)}},
		"stream":          false,
		"response_format": map[string]string{"type": "json_object"},
	}
	// Native JSON Schema is enabled only for the explicitly verified Qwen
	// models. Other providers retain JSON Object and the shared schema prompt.
	if schema != nil && c.provider == "qwen" && (c.model == "qwen3.8-flash" || c.model == "qwen3.8-max") {
		payload["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "paper_output", "strict": true, "schema": schema.Structural()}}
	}
	c.configure(payload, maxTokens)
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, &Failure{Code: "request_failed"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return Result{}, &Failure{Code: "result_unknown"}
		}
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return Result{}, &Failure{Code: "timeout", Retryable: true}
		}
		return Result{}, &Failure{Code: "transport_failed", Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		failure := responseFailure(resp)
		if strings.Contains(failure.ProviderCode, c.key) {
			failure.ProviderCode = ""
		}
		if strings.Contains(failure.ProviderRequestID, c.key) {
			failure.ProviderRequestID = ""
		}
		return Result{}, failure
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return Result{}, &Failure{Code: "invalid_response"}
	}
	var wire struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			Input  int64 `json:"prompt_tokens"`
			Output int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &wire) != nil || len(wire.Choices) != 1 {
		return Result{}, &Failure{Code: "invalid_response"}
	}
	result := Result{Content: []byte(wire.Choices[0].Message.Content)}
	if wire.Usage != nil && wire.Usage.Input >= 0 && wire.Usage.Output >= 0 {
		result.InputTokens = wire.Usage.Input
		result.OutputTokens = wire.Usage.Output
		result.UsageKnown = true
	}
	if wire.Choices[0].Finish == "length" {
		return result, &Failure{Code: "output_truncated"}
	}
	if wire.Choices[0].Finish != "stop" || wire.Choices[0].Message.Content == "" {
		return result, &Failure{Code: "invalid_response"}
	}
	return result, nil
}
func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

// Catalog supplies allowlisted provider metadata without exposing wire protocols.
type Catalog struct{}

func (Catalog) Providers(enabled []string) []generation.Provider { return Providers(enabled) }
func (Catalog) SelectionAvailability(enabled []string, p, m string) (bool, string) {
	return SelectionAvailability(enabled, p, m)
}
func (Catalog) ValidateSelection(enabled []string, p, m string) bool {
	return ValidateSelection(enabled, p, m)
}

// Only bounded identifier-shaped metadata is retained. Vendor messages may contain secrets.
func safeIdentifier(s string) string {
	if len(s) > 128 {
		return ""
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':') {
			return ""
		}
	}
	return s
}
func responseFailure(resp *http.Response) *Failure {
	f := &Failure{Code: "provider_rejected", HTTPStatus: resp.StatusCode, RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now()), ProviderRequestID: safeIdentifier(resp.Header.Get("X-Request-ID"))}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err == nil && len(raw) <= 8192 {
		var v struct {
			Error struct {
				Code json.RawMessage `json:"code"`
			} `json:"error"`
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(raw, &v) == nil {
			var code string
			if json.Unmarshal(v.Error.Code, &code) != nil {
				code = string(v.Error.Code)
			}
			f.ProviderCode = safeIdentifier(code)
			if f.ProviderRequestID == "" {
				f.ProviderRequestID = safeIdentifier(v.RequestID)
			}
		}
	}
	switch resp.StatusCode {
	case 401:
		f.Code = "credential_rejected"
	case 403:
		f.Code = "model_access_denied"
	case 429:
		f.Code = "provider_rate_limited"
		f.Retryable = true
	default:
		if resp.StatusCode >= 500 {
			f.Code = "provider_unavailable"
			f.Retryable = true
		}
	}
	return f
}
