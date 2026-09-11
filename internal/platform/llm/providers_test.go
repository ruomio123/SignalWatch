package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderContractsDisableReasoningAndRequestJSON(t *testing.T) {
	for _, provider := range []string{"glm", "qwen", "deepseek", "openai", "kimi"} {
		t.Run(provider, func(t *testing.T) {
			var body map[string]any
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != "https://provider.example/v1/chat/completions" {
					t.Fatalf("endpoint=%s", request.URL)
				}
				if request.Header.Get("Authorization") != "Bearer sentinel-key" {
					t.Fatal("authorization missing")
				}
				raw, _ := io.ReadAll(request.Body)
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))}, nil
			})}
			client, err := newClient(provider, "https://provider.example/v1", "fixture", "sentinel-key", providers[provider].configure, httpClient)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.GenerateLimit(context.Background(), "system", []byte(`{"paper":1}`), 64)
			if err != nil || !result.UsageKnown {
				t.Fatalf("generate: %+v %v", result, err)
			}
			limitField := "max_tokens"
			if provider == "qwen" || provider == "openai" || provider == "kimi" {
				limitField = "max_completion_tokens"
			}
			if body[limitField] != float64(64) || body["stream"] != false {
				t.Fatalf("limits=%+v", body)
			}
			wrongLimitField := "max_completion_tokens"
			if provider == "qwen" || provider == "openai" || provider == "kimi" {
				wrongLimitField = "max_tokens"
			}
			if _, exists := body[wrongLimitField]; exists {
				t.Fatalf("%s received incompatible %s", provider, wrongLimitField)
			}
			format, ok := body["response_format"].(map[string]any)
			if !ok || format["type"] != "json_object" {
				t.Fatalf("response format=%+v", body)
			}
			switch provider {
			case "glm", "deepseek", "kimi":
				thinking, ok := body["thinking"].(map[string]any)
				if !ok || thinking["type"] != "disabled" {
					t.Fatalf("thinking=%+v", body["thinking"])
				}
			case "qwen":
				if body["enable_thinking"] != false || body["preserve_thinking"] != false {
					t.Fatalf("qwen thinking=%+v", body)
				}
			case "openai":
				if body["reasoning_effort"] != "none" {
					t.Fatalf("reasoning=%+v", body)
				}
			}
		})
	}
}

func TestProviderRegistryIsAllowlisted(t *testing.T) {
	registry := Providers([]string{"glm", "qwen", "deepseek", "kimi", "openai"})
	if len(registry) != 5 || registry[0].ID != "glm" || registry[1].ID != "qwen" || registry[2].ID != "deepseek" || registry[3].ID != "kimi" || registry[4].ID != "openai" {
		t.Fatalf("registry=%+v", registry)
	}
	for _, provider := range registry {
		if len(provider.Models) == 0 || !provider.Models[0].Default {
			t.Fatalf("missing default model: %+v", provider)
		}
	}
	if ValidateSelection([]string{"glm"}, "openai", "gpt-5.6-luna") {
		t.Fatal("disabled provider accepted")
	}
	if ValidateSelection([]string{"glm"}, "glm", "user-supplied-model") {
		t.Fatal("arbitrary model accepted")
	}
	if !ValidateSelection([]string{"qwen"}, "qwen", "qwen3.8-flash") || !ValidateSelection([]string{"kimi"}, "kimi", "kimi-k2.6") || !ValidateSelection([]string{"deepseek"}, "deepseek", "deepseek-flash") {
		t.Fatal("current provider model rejected")
	}
	if available, reason := SelectionAvailability([]string{"deepseek"}, "deepseek", "deepseek-v4-pro"); available || reason != UnavailableModelRetired {
		t.Fatalf("retired model availability=%v reason=%q", available, reason)
	}
	if available, reason := SelectionAvailability([]string{"glm"}, "kimi", "kimi-k2.6"); available || reason != UnavailableProviderDisabled {
		t.Fatalf("disabled provider availability=%v reason=%q", available, reason)
	}
}

func TestProviderClientsUseFixedEndpointsAndSafeStatusMapping(t *testing.T) {
	expectedEndpoints := map[string]string{
		"glm":      "https://open.bigmodel.cn/api/paas/v4/chat/completions",
		"qwen":     "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
		"deepseek": "https://api.deepseek.com/chat/completions",
		"openai":   "https://api.openai.com/v1/chat/completions",
		"kimi":     "https://api.moonshot.cn/v1/chat/completions",
	}
	for provider, endpoint := range expectedEndpoints {
		models := Providers([]string{provider})[0].Models
		client, err := NewProviderClient([]string{provider}, provider, models[0].ID, "sentinel-key")
		if err != nil || client.endpoint != endpoint {
			t.Fatalf("%s endpoint=%q err=%v", provider, client.endpoint, err)
		}
		for status, expected := range map[int]string{401: "credential_rejected", 403: "model_access_denied", 429: "provider_rate_limited", 503: "provider_unavailable"} {
			client.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("provider-secret-body"))}, nil
			})}
			_, callErr := client.GenerateLimit(context.Background(), "system", []byte(`{}`), 64)
			var failure *Failure
			if !errors.As(callErr, &failure) || failure.Code != expected || strings.Contains(callErr.Error(), "provider-secret-body") {
				t.Fatalf("%s status=%d failure=%v", provider, status, callErr)
			}
		}
	}
}

func TestProviderDiagnosticsAreBoundedAndDoNotExposeMessages(t *testing.T) {
	for _, tc := range []struct {
		status                   int
		body, code, providerCode string
	}{
		{400, `{"error":{"code":"bad_parameter","message":"private key and prompt"},"request_id":"vendor-123"}`, "provider_rejected", "bad_parameter"},
		{403, `{"error":{"code":"model_denied"}}`, "model_access_denied", "model_denied"},
		{429, `{"error":{"code":1302}}`, "provider_rate_limited", "1302"},
		{503, strings.Repeat("x", 9000), "provider_unavailable", ""},
	} {
		t.Run(tc.code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client, e := newClient("glm", srv.URL, "glm-4.7-flash", "test-secret", configureThinkingWithMaxTokens, nil)
			if e != nil {
				t.Fatal(e)
			}
			_, e = client.GenerateLimit(t.Context(), "JSON", []byte(`{}`), 1024)
			var f *Failure
			if !errors.As(e, &f) || f.Code != tc.code || f.ProviderCode != tc.providerCode || f.HTTPStatus != tc.status {
				t.Fatalf("diagnostics=%+v", f)
			}
			if strings.Contains(f.Error(), "private") || strings.Contains(f.Error(), "prompt") {
				t.Fatal("private body leaked")
			}
		})
	}
}
func TestProviderRequestCancellation(t *testing.T) {
	client, e := newClient("glm", "https://provider.example", "glm-4.7-flash", "test-secret", configureThinkingWithMaxTokens, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, e = client.GenerateLimit(ctx, "JSON", []byte(`{}`), 1024)
	var f *Failure
	if !errors.As(e, &f) || f.Code != "timeout" {
		t.Fatalf("timeout=%v", e)
	}
}
