package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
