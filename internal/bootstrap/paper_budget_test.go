package bootstrap

import (
	"context"
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
