package ai

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"signalwatch/internal/generation"
	"signalwatch/internal/platform/llm"
	"strconv"
	"strings"
	"testing"
	"time"
)

type paperBudgetConfigurationStore struct {
	ConfigurationStore
	row Configuration
}

func (s paperBudgetConfigurationStore) Read(context.Context, uint64) (Configuration, error) {
	return s.row, nil
}
func (paperBudgetConfigurationStore) MarkUsed(context.Context, uint64, uint64, time.Time) error {
	return nil
}

type paperBudgetCipher struct{ CredentialCipher }

func (paperBudgetCipher) Decrypt([]byte, []byte, string, string) ([]byte, error) {
	return []byte("fixture-key"), nil
}

type paperBudgetCallStore struct {
	CallStore
	started, finished int
	settlementError   error
	result            generation.Result
	failure           error
}

func (*paperBudgetCallStore) Admit(context.Context, CallRecord, CallPolicy) error { return nil }
func (s *paperBudgetCallStore) Start(context.Context, string, time.Time) error {
	s.started++
	return nil
}
func (s *paperBudgetCallStore) Finish(_ context.Context, _ string, result generation.Result, err error, _ time.Time) error {
	s.finished++
	s.result, s.failure = result, err
	return s.settlementError
}
func (*paperBudgetCallStore) Release(context.Context, CallRecord, time.Time) error { return nil }

type paperBudgetGenerator func(context.Context, string, []byte, int) (generation.Result, error)

func (f paperBudgetGenerator) GenerateLimit(ctx context.Context, system string, input []byte, tokens int) (generation.Result, error) {
	return f(ctx, system, input, tokens)
}

type paperBudgetStructuredGenerator struct {
	paperBudgetGenerator
	checkSchema func(*generation.Schema)
}

func (g paperBudgetStructuredGenerator) GenerateStructured(ctx context.Context, system string, input []byte, tokens int, schema *generation.Schema) (generation.Result, error) {
	g.checkSchema(schema)
	return g.paperBudgetGenerator(ctx, system, input, tokens)
}

func TestPaperCredentialTokenBudgetPassesThroughBothAdapterCapabilities(t *testing.T) {
	for _, structured := range []bool{false, true} {
		for _, budget := range []int{-1, 0, 4096, 8192} {
			// -1 exercises the existing business API, whose default stays 4096.
			name := "plain"
			if structured {
				name = "structured"
			}
			t.Run(name+"/"+strconv.Itoa(budget), func(t *testing.T) {
				schema := generation.SchemaFor[struct {
					Status string `json:"status"`
				}]()
				invoked, before, validated, structuredCalls := 0, 0, 0, 0
				generator := paperBudgetGenerator(func(_ context.Context, system string, input []byte, tokens int) (generation.Result, error) {
					invoked++
					expected := budget
					if expected <= 0 {
						expected = 4096
					}
					if tokens != expected || !strings.Contains(system, schema.Instructions()) || string(input) != `{"question":"fixture"}` {
						t.Fatalf("request changed: tokens=%d system=%q input=%s", tokens, system, input)
					}
					return generation.Result{Content: []byte(`{"status":"ok"}`), UsageKnown: true}, nil
				})
				factory := func([]string, string, string, string) (Generator, error) {
					if !structured {
						return generator, nil
					}
					return paperBudgetStructuredGenerator{generator, func(actual *generation.Schema) {
						structuredCalls++
						if actual != schema {
							t.Fatal("schema not forwarded")
						}
					}}, nil
				}
				store := &paperBudgetCallStore{}
				now := time.Now
				runner := NewCallRunner(store, CallPolicy{}, factory, []string{"qwen"}, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
				service := NewConfigurationService(paperBudgetConfigurationStore{row: Configuration{Generation: "fixture", UserID: 1, ProviderID: "qwen", ModelID: "qwen3.8-flash", Status: ConfigurationActive, ConfigVersion: 1}}, paperBudgetCipher{}, []string{"qwen"}, now, runner, llm.Catalog{})
				beforeStart := func(context.Context) error { before++; return nil }
				validate := func(result generation.Result) error { validated++; return schema.Validate(result.Content) }
				var result generation.Result
				var err error
				if budget < 0 {
					result, err = service.GenerateForCredential(t.Context(), 1, "qwen", "qwen3.8-flash", "fixture", 1, FeaturePaperQA, "run", "Analyze", []byte(`{"question":"fixture"}`), beforeStart, validate, schema)
				} else {
					result, err = service.GenerateForCredentialLimit(t.Context(), 1, "qwen", "qwen3.8-flash", "fixture", 1, FeaturePaperQA, "run", "Analyze", []byte(`{"question":"fixture"}`), budget, beforeStart, validate, schema)
				}
				if err != nil || result.CallID == "" || invoked != 1 || before != 1 || validated != 1 || store.started != 1 || store.finished != 1 || (structured && structuredCalls != 1) {
					t.Fatalf("call did not complete once: result=%+v err=%v calls=%d before=%d validate=%d store=%+v", result, err, invoked, before, validated, store)
				}
			})
		}
	}
}

func TestPaperOutputFailureReturnsCandidateOnlyWithDefiniteSettlementCode(t *testing.T) {
	for _, settlementFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "settled", true: "unknown_settlement"}[settlementFails], func(t *testing.T) {
			store := &paperBudgetCallStore{}
			if settlementFails {
				store.settlementError = errors.New("fixture settlement failure")
			}
			calls := 0
			factory := func([]string, string, string, string) (Generator, error) {
				return paperBudgetGenerator(func(context.Context, string, []byte, int) (generation.Result, error) {
					calls++
					return generation.Result{Content: []byte(`{"claims":[]}`), UsageKnown: true, InputTokens: 2, OutputTokens: 3}, nil
				}), nil
			}
			runner := NewCallRunner(store, CallPolicy{}, factory, []string{"qwen"}, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
			result, err := runner.Run(t.Context(), CallRequest{UserID: 1, Provider: "qwen", Model: "qwen3.8-flash", Feature: FeaturePaperQA, MaxTokens: 8192, Validate: func(generation.Result) error {
				return &generation.Failure{Code: "output_limit_exceeded", ValidationPath: "$.claims", ValidationRule: "max_items"}
			}})
			want := "output_limit_exceeded"
			if settlementFails {
				want = "storage_failed"
			}
			var call *CallError
			if !errors.As(err, &call) || call.Code != want || call.CallID == "" || call.CallID != result.CallID || string(result.Content) != `{"claims":[]}` || !result.UsageKnown || store.finished != 1 || calls != 1 {
				t.Fatalf("candidate or definite result lost: result=%+v error=%v", result, err)
			}
		})
	}
}
