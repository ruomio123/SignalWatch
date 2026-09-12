package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"signalwatch/internal/generation"
	"strings"
	"testing"
	"time"
)

func TestCompatibleWireAndSafeFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
		retry      bool
	}{
		{"success", 200, `{"choices":[{"message":{"content":"{\"summary\":\"ok\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":10}}`, "", false},
		{"rate", 429, "secret-provider-body", "provider_rate_limited", true},
		{"server", 503, "secret-provider-body", "provider_unavailable", true},
		{"auth", 401, "secret-provider-body", "credential_rejected", false},
		{"malformed", 200, "secret-provider-body", "invalid_response", false},
		{"truncated", 200, `{"choices":[{"message":{"content":"{}"},"finish_reason":"length"}]}`, "output_truncated", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Error("incorrect endpoint/auth")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "fixture" {
					t.Error("invalid request")
				}
				w.Header().Set("Retry-After", "420")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := New(server.URL+"/v1", "fixture", "test-secret")
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Generate(t.Context(), "system", []byte(`{"papers":[]}`))
			if tc.code == "" {
				if err != nil || !result.UsageKnown || result.InputTokens != 20 || result.OutputTokens != 10 {
					t.Fatalf("%+v %v", result, err)
				}
				return
			}
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != tc.code || failure.Retryable != tc.retry {
				t.Fatalf("%v", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("secret leaked")
			}
			if tc.status == 429 && failure.RetryAfter != 7*time.Minute {
				t.Fatal("retry-after lost")
			}
		})
	}
}
func TestTimeoutAndRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()
	client, _ := New(server.URL, "fixture", "secret")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := client.Generate(ctx, "system", []byte(`{}`))
	var f *Failure
	if !errors.As(err, &f) || f.Code != "timeout" {
		t.Fatalf("%v", err)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed redirect with key") }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client, _ = New(redirect.URL, "fixture", "secret")
	_, err = client.Generate(t.Context(), "system", []byte(`{}`))
	if err == nil {
		t.Fatal("redirect accepted")
	}
	for _, url := range []string{"http://example.com/v1", "https://user:secret@example.com", "https://example.com?key=secret"} {
		if _, err := New(url, "model", "key"); err == nil {
			t.Fatal("unsafe config")
		}
	}
}

func TestPaperSchemaWireUsesProviderCapability(t *testing.T) {
	type output struct {
		Status string   `json:"status" enum:"supported,not_stated"`
		Claims []string `json:"claims"`
	}
	schema := generation.SchemaFor[output]()
	for _, provider := range []string{"qwen", "glm"} {
		t.Run(provider, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var payload map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&payload) != nil {
					t.Fatal("bad request")
				}
				var format struct {
					Type   string `json:"type"`
					Schema struct {
						Strict bool            `json:"strict"`
						Schema json.RawMessage `json:"schema"`
					} `json:"json_schema"`
				}
				if json.Unmarshal(payload["response_format"], &format) != nil {
					t.Fatal("missing format")
				}
				if provider == "qwen" {
					expected, _ := json.Marshal(schema)
					if format.Type != "json_schema" || !format.Schema.Strict || string(format.Schema.Schema) != string(expected) {
						t.Errorf("schema lost: %s", payload["response_format"])
					}
				} else if format.Type != "json_object" {
					t.Error("sent unsupported GLM schema format")
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"status\":\"not_stated\",\"claims\":[]}"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			model := "glm-5.2"
			configure := configureThinkingWithMaxTokens
			if provider == "qwen" {
				model = "qwen3.8-flash"
				configure = configureQwen
			}
			client, err := newClient(provider, server.URL, model, "fixture-key", configure, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.GenerateStructured(t.Context(), "Analyze paper."+schema.Instructions(), []byte(`{}`), 4096, schema)
			if err != nil || requests != 1 {
				t.Fatalf("%d %v", requests, err)
			}
		})
	}
}
