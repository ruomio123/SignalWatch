package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"signalwatch/internal/generation"
	"strings"
	"testing"
	"time"
)

type paperBudgetTransport func(*http.Request) (*http.Response, error)

func (f paperBudgetTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPaperOutputTokenBudgetsPreserveProviderContracts(t *testing.T) {
	for _, provider := range []string{"glm", "qwen", "deepseek", "openai", "kimi"} {
		t.Run(provider, func(t *testing.T) {
			var requests []map[string]any
			transport := paperBudgetTransport(func(r *http.Request) (*http.Response, error) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				requests = append(requests, payload)
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))}, nil
			})
			client, err := newClient(provider, "https://fixture.invalid/v1", providers[provider].models[0].ID, "fixture-key", providers[provider].configure, &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.Generate(t.Context(), "JSON", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			if _, err = client.GenerateLimit(t.Context(), "JSON", []byte(`{}`), 8192); err != nil {
				t.Fatal(err)
			}
			for _, invalid := range []int{-1, 0, 8193} {
				_, err = client.GenerateLimit(t.Context(), "JSON", []byte(`{}`), invalid)
				var failure *generation.Failure
				if !errors.As(err, &failure) || failure.Code != "invalid_request" {
					t.Fatalf("budget %d accepted: %v", invalid, err)
				}
			}
			if len(requests) != 2 {
				t.Fatalf("requests=%d", len(requests))
			}
			field, forbidden := "max_tokens", "max_completion_tokens"
			if provider == "qwen" || provider == "openai" || provider == "kimi" {
				field, forbidden = forbidden, field
			}
			for i, budget := range []int{4096, 8192} {
				if requests[i][field] != float64(budget) || requests[i][forbidden] != nil {
					t.Fatalf("provider budget changed: %+v", requests[i])
				}
			}
		})
	}
}

func TestPaperTransportHonorsLongerCallerDeadlineAndRetainsTruncationUsage(t *testing.T) {
	client, err := newClient("qwen", "https://fixture.invalid/v1", "qwen3.8-flash", "fixture-key", configureQwen, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.http.Timeout != 0 {
		t.Fatal("HTTP client must not shorten the feature-specific caller deadline")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	wantDeadline, _ := ctx.Deadline()
	client.http.Transport = paperBudgetTransport(func(request *http.Request) (*http.Response, error) {
		if deadline, _ := request.Context().Deadline(); deadline != wantDeadline {
			t.Fatalf("caller deadline changed: %v, want %v", deadline, wantDeadline)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"claims\":["},"finish_reason":"length"}],"usage":{"prompt_tokens":7,"completion_tokens":8192}}`))}, nil
	})
	result, err := client.GenerateLimit(ctx, "JSON", []byte(`{}`), 8192)
	var failure *generation.Failure
	if !errors.As(err, &failure) || failure.Code != "output_truncated" || string(result.Content) != `{"claims":[` || !result.UsageKnown || result.InputTokens != 7 || result.OutputTokens != 8192 {
		t.Fatalf("truncation outcome lost: result=%+v err=%v", result, err)
	}
}

func TestPaperCatalogLimitsUseIndependentModelOverrides(t *testing.T) {
	custom := generation.ModelLimits{ContextTokens: 65536, MaxOutputTokens: 4096}
	catalog := Catalog{Limits: map[string]generation.ModelLimits{"qwen/qwen3.8-flash": custom}}
	if got := catalog.ModelLimits("qwen", "qwen3.8-flash"); got != custom {
		t.Fatalf("override=%+v", got)
	}
	if got := catalog.ModelLimits("qwen", "qwen3.8-max"); got != generation.DefaultModelLimits() {
		t.Fatalf("default=%+v", got)
	}
}

func TestQwenSchemaProjectsLimitsAndNeverRetriesRefusal(t *testing.T) {
	schema := generation.SchemaFor[struct {
		Claims []struct {
			Text string   `json:"text"`
			Refs []string `json:"refs"`
		} `json:"claims"`
	}]()
	minimum, maximum, refsMaximum := 0, 8, 3
	schema.Properties["claims"].MinItems = &minimum
	schema.Properties["claims"].MaxItems = &maximum
	schema.Properties["claims"].Items.Properties["refs"].MaxItems = &refsMaximum
	before, _ := json.Marshal(schema)
	for _, status := range []int{200, 400} {
		requests := 0
		transport := paperBudgetTransport(func(r *http.Request) (*http.Response, error) {
			requests++
			var payload struct {
				Tokens   int `json:"max_completion_tokens"`
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
				Format struct {
					Type   string `json:"type"`
					Schema struct {
						Strict bool               `json:"strict"`
						Schema *generation.Schema `json:"schema"`
					} `json:"json_schema"`
				} `json:"response_format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Format.Type != "json_schema" || !payload.Format.Schema.Strict || payload.Tokens != 8192 {
				t.Fatalf("Qwen protocol changed: %+v", payload)
			}
			if !reflect.DeepEqual(payload.Format.Schema.Schema, schema.Structural()) {
				t.Fatalf("unexpected structural projection: %+v", payload.Format.Schema.Schema)
			}
			claims := payload.Format.Schema.Schema.Properties["claims"]
			if claims.MinItems != nil || claims.MaxItems != nil || claims.Items.Properties["refs"].MaxItems != nil {
				t.Fatal("Qwen received unverified numeric constraints")
			}
			if !strings.Contains(payload.Messages[0].Content, `"maxItems":8`) || !strings.Contains(payload.Messages[0].Content, `"maxItems":3`) {
				t.Fatal("full constraints absent from prompt")
			}
			body := `{"choices":[{"message":{"content":"{\"claims\":[]}"},"finish_reason":"stop"}]}`
			if status != 200 {
				body = `{"error":{"code":"unsupported_schema","message":"private failure details"}}`
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		client, err := newClient("qwen", "https://fixture.invalid/v1", "qwen3.8-flash", "fixture-key", configureQwen, &http.Client{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.GenerateStructured(t.Context(), "JSON"+schema.Instructions(), []byte(`{}`), 8192, schema)
		if status == 200 && err != nil {
			t.Fatal(err)
		}
		if status == 400 {
			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Code != "provider_rejected" {
				t.Fatalf("refusal=%v", err)
			}
		}
		if requests != 1 {
			t.Fatalf("provider refusal retried with a changed protocol: %d", requests)
		}
	}
	after, _ := json.Marshal(schema)
	if string(before) != string(after) {
		t.Fatal("provider projection mutated shared schema")
	}
}
