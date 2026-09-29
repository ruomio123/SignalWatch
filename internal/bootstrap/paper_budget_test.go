package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"signalwatch/internal/agent"
	"signalwatch/internal/ai"
	"signalwatch/internal/generation"
	"signalwatch/internal/platform/llm"
	"strconv"
	"testing"
	"time"
)

type gatewayBudgetStore struct{ ai.ConfigurationStore }

func (gatewayBudgetStore) Read(context.Context, uint64) (ai.Configuration, error) {
	return ai.Configuration{UserID: 1, Generation: "fixture", ConfigVersion: 1, ProviderID: "qwen", ModelID: "qwen3.8-flash", Status: ai.ConfigurationActive}, nil
}
func (gatewayBudgetStore) MarkUsed(context.Context, uint64, uint64, time.Time) error { return nil }

type gatewayBudgetCipher struct{ ai.CredentialCipher }

func (gatewayBudgetCipher) Decrypt([]byte, []byte, string, string) ([]byte, error) {
	return []byte("fixture-key"), nil
}

type gatewayBudgetCalls struct{ ai.CallStore }

func (gatewayBudgetCalls) Admit(context.Context, ai.CallRecord, ai.CallPolicy) error { return nil }
func (gatewayBudgetCalls) Start(context.Context, string, time.Time) error            { return nil }
func (gatewayBudgetCalls) Finish(context.Context, string, generation.Result, error, time.Time) error {
	return nil
}
func (gatewayBudgetCalls) Release(context.Context, ai.CallRecord, time.Time) error { return nil }

type gatewayBudgetGenerator func(int)

func (f gatewayBudgetGenerator) GenerateLimit(_ context.Context, _ string, _ []byte, tokens int) (generation.Result, error) {
	f(tokens)
	return generation.Result{Content: []byte(`{}`), UsageKnown: true}, nil
}

func TestAgentGatewayForwardsInternalTokenBudget(t *testing.T) {
	for _, budget := range []int{0, 4096, 8192} {
		t.Run(strconv.Itoa(budget), func(t *testing.T) {
			calls := 0
			factory := func([]string, string, string, string) (ai.Generator, error) {
				return gatewayBudgetGenerator(func(actual int) {
					calls++
					expected := budget
					if expected == 0 {
						expected = 4096
					}
					if actual != expected {
						t.Fatalf("tokens=%d, want=%d", actual, expected)
					}
				}), nil
			}
			runner := ai.NewCallRunner(gatewayBudgetCalls{}, ai.CallPolicy{}, factory, []string{"qwen"}, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
			gateway := agentGateway{ai.NewConfigurationService(gatewayBudgetStore{}, gatewayBudgetCipher{}, []string{"qwen"}, time.Now, runner, llm.Catalog{})}
			result, err := gateway.Generate(t.Context(), agent.ModelRequest{Run: agent.Run{UserID: 1, Generation: "fixture", Version: 1, Provider: "qwen", Model: "qwen3.8-flash"}, Feature: ai.FeaturePaperQA, MaxTokens: budget, Validate: func(generation.Result) error { return nil }})
			if err != nil || calls != 1 || result.CallID == "" {
				t.Fatalf("gateway result=%+v calls=%d error=%v", result, calls, err)
			}
		})
	}
}

func TestAgentGatewayPreservesPreCallWorkflowFailures(t *testing.T) {
	for _, failure := range []error{agent.ErrLease, agent.ErrConflict, agent.ErrNotFound, agent.ErrBudget, context.Canceled, context.DeadlineExceeded, &agent.ModelError{Code: "AI_CONFIGURATION_VERSION_CONFLICT"}} {
		t.Run(failure.Error(), func(t *testing.T) {
			calls, before, validations := 0, 0, 0
			factory := func([]string, string, string, string) (ai.Generator, error) {
				return gatewayBudgetGenerator(func(int) { calls++ }), nil
			}
			runner := ai.NewCallRunner(gatewayBudgetCalls{}, ai.CallPolicy{}, factory, []string{"qwen"}, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
			gateway := agentGateway{ai.NewConfigurationService(gatewayBudgetStore{}, gatewayBudgetCipher{}, []string{"qwen"}, time.Now, runner, llm.Catalog{})}
			result, err := gateway.Generate(t.Context(), agent.ModelRequest{
				Run: agent.Run{UserID: 1, Generation: "fixture", Version: 1, Provider: "qwen", Model: "qwen3.8-flash"}, Feature: ai.FeaturePaperQA, MaxTokens: 8192,
				Before:   func(context.Context) error { before++; return fmt.Errorf("workflow check: %w", failure) },
				Validate: func(generation.Result) error { validations++; return nil },
			})
			if !errors.Is(err, failure) || before != 1 || validations != 0 || calls != 0 || result.CallID != "" {
				t.Fatalf("workflow failure masked or model called: result=%+v calls=%d before=%d validations=%d error=%v", result, calls, before, validations, err)
			}
		})
	}
}

func TestAgentGatewaySelectionIncludesConfiguredPaperBudgets(t *testing.T) {
	limits := generation.ModelLimits{ContextTokens: 65536, MaxOutputTokens: 4096}
	factory := func([]string, string, string, string) (ai.Generator, error) {
		return gatewayBudgetGenerator(func(int) { t.Fatal("selection must not call the model") }), nil
	}
	runner := ai.NewCallRunner(gatewayBudgetCalls{}, ai.CallPolicy{PaperCallTimeout: 90 * time.Second}, factory, []string{"qwen"}, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	gateway := agentGateway{ai.NewConfigurationService(gatewayBudgetStore{}, gatewayBudgetCipher{}, []string{"qwen"}, time.Now, runner, llm.Catalog{Limits: map[string]generation.ModelLimits{"qwen/qwen3.8-flash": limits}})}
	selection, err := gateway.Selection(t.Context(), 1, "qwen", "qwen3.8-flash", "fixture")
	if err != nil || selection.Generation != "fixture" || selection.Version != 1 || selection.Limits != limits || selection.CallTimeout != 90*time.Second {
		t.Fatalf("selection=%+v err=%v", selection, err)
	}
}
