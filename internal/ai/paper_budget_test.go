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
	row  Configuration
	read func() (Configuration, error)
}

func (s paperBudgetConfigurationStore) Read(context.Context, uint64) (Configuration, error) {
	if s.read != nil {
		return s.read()
	}
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
	admitted, released int
	started, finished  int
	settlementError    error
	result             generation.Result
	failure            error
	record             CallRecord
}

func (s *paperBudgetCallStore) Admit(_ context.Context, record CallRecord, _ CallPolicy) error {
	s.admitted++
	s.record = record
	return nil
}
func (s *paperBudgetCallStore) Start(context.Context, string, time.Time) error {
	s.started++
	return nil
}
func (s *paperBudgetCallStore) Finish(_ context.Context, _ string, result generation.Result, err error, _ time.Time) error {
	s.finished++
	s.result, s.failure = result, err
	return s.settlementError
}
func (s *paperBudgetCallStore) Release(context.Context, CallRecord, time.Time) error {
	s.released++
	return nil
}

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

func TestPaperCallFailurePrecedenceNeverReplaysOrExposesRepairableTransportFailures(t *testing.T) {
	for _, tc := range []struct {
		name, providerFailure, want         string
		cancel, expireLease, failSettlement bool
		wantValidations                     int
	}{
		{name: "settlement failure after validation", failSettlement: true, want: "storage_failed", wantValidations: 1},
		{name: "lease expiry after validation", expireLease: true, want: "result_unknown", wantValidations: 1},
		{name: "provider timeout", providerFailure: "timeout", want: "timeout"},
		{name: "provider refusal", providerFailure: "provider_rejected", want: "provider_rejected"},
		{name: "truncated content", providerFailure: "output_truncated", want: "output_truncated"},
		{name: "canceled call", cancel: true, want: "result_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store := &paperBudgetCallStore{}
			if tc.failSettlement {
				store.settlementError = errors.New("fixture settlement failure")
			}
			now := time.Now()
			calls, validations := 0, 0
			factory := func([]string, string, string, string) (Generator, error) {
				return paperBudgetGenerator(func(context.Context, string, []byte, int) (generation.Result, error) {
					calls++
					result := generation.Result{Content: []byte(`{"claims":[]}`), UsageKnown: true}
					if tc.cancel {
						cancel()
					}
					if tc.providerFailure != "" {
						return result, &generation.Failure{Code: tc.providerFailure}
					}
					return result, nil
				}), nil
			}
			runner := NewCallRunner(store, CallPolicy{}, factory, []string{"qwen"}, func() time.Time { return now }, slog.New(slog.NewTextHandler(io.Discard, nil)))
			result, err := runner.Run(ctx, CallRequest{UserID: 1, Provider: "qwen", Model: "qwen3.8-flash", Feature: FeaturePaperQA, MaxTokens: 8192, Validate: func(generation.Result) error {
				validations++
				if tc.expireLease {
					now = now.Add(2 * time.Minute)
				}
				return &generation.Failure{Code: "output_limit_exceeded", ValidationPath: "$.claims"}
			}})
			var call *CallError
			if !errors.As(err, &call) || call.Code != tc.want || call.CallID != result.CallID || validations != tc.wantValidations || calls != 1 || store.admitted != 1 || store.started != 1 || store.finished != 1 || store.released != 1 {
				t.Fatalf("failure precedence or no-replay violated: result=%+v err=%v validations=%d calls=%d store=%+v", result, err, validations, calls, store)
			}
		})
	}
}

func TestPaperCredentialChangeAfterSettledLimitBlocksFurtherModelCalls(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
	}{
		{"rotated", ErrConfigurationConflict},
		{"deleted", ErrConfigurationRequired},
		{"invalidated", ErrConfigurationInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := Configuration{Generation: "fixture", UserID: 1, ProviderID: "qwen", ModelID: "qwen3.8-flash", Status: ConfigurationActive, ConfigVersion: 1}
			changed := false
			configurations := paperBudgetConfigurationStore{read: func() (Configuration, error) {
				if !changed {
					return row, nil
				}
				if tc.name == "deleted" {
					return Configuration{}, ErrConfigurationRequired
				}
				updated := row
				if tc.name == "rotated" {
					updated.ConfigVersion++
				}
				if tc.name == "invalidated" {
					updated.Status = ConfigurationInvalid
				}
				return updated, nil
			}}
			calls := 0
			factory := func([]string, string, string, string) (Generator, error) {
				return paperBudgetGenerator(func(context.Context, string, []byte, int) (generation.Result, error) {
					calls++
					changed = true
					return generation.Result{Content: []byte(`{"claims":[]}`), UsageKnown: true}, nil
				}), nil
			}
			store := &paperBudgetCallStore{}
			runner := NewCallRunner(store, CallPolicy{}, factory, []string{"qwen"}, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
			service := NewConfigurationService(configurations, paperBudgetCipher{}, []string{"qwen"}, time.Now, runner, llm.Catalog{})
			validate := func(generation.Result) error { return &generation.Failure{Code: "output_limit_exceeded"} }
			result, err := service.GenerateForCredentialLimit(t.Context(), 1, "qwen", "qwen3.8-flash", "fixture", 1, FeaturePaperQA, "run", "Analyze", []byte(`{}`), 8192, nil, validate, nil)
			var call *CallError
			if !errors.As(err, &call) || call.Code != "output_limit_exceeded" || result.CallID == "" {
				t.Fatalf("original call=%+v err=%v", result, err)
			}
			// The workflow checks its saved selection before scheduling repair; the
			// credential boundary independently rejects a call using that revision.
			selection, selectionErr := service.SelectionForCredential(t.Context(), 1, "fixture", "qwen", "qwen3.8-flash")
			if selectionErr == nil && selection.Version == 1 {
				t.Fatal("stale selection remains valid")
			}
			_, err = service.GenerateForCredentialLimit(t.Context(), 1, "qwen", "qwen3.8-flash", "fixture", 1, FeaturePaperQA, "run", "Repair", []byte(`{}`), 8192, nil, validate, nil)
			if !errors.Is(err, tc.failure) || calls != 1 || store.admitted != 1 || store.started != 1 || store.finished != 1 {
				t.Fatalf("stale credential allowed a second model call: err=%v calls=%d store=%+v", err, calls, store)
			}
		})
	}
}

func TestPaperCallTimeoutAndLeaseFollowFeaturePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, feature string
		configured    time.Duration
		want          time.Duration
	}{
		{name: "paper default", feature: FeaturePaperQA, want: time.Minute},
		{name: "paper configured", feature: FeaturePaperQA, configured: 2 * time.Minute, want: 2 * time.Minute},
		{name: "paper minimum", feature: FeaturePaperQA, configured: 10 * time.Second, want: 10 * time.Second},
		{name: "other calls unchanged", feature: FeatureSubscriptionAgent, configured: 2 * time.Minute, want: 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			started := now
			store := &paperBudgetCallStore{}
			factory := func([]string, string, string, string) (Generator, error) {
				return paperBudgetGenerator(func(ctx context.Context, _ string, _ []byte, _ int) (generation.Result, error) {
					deadline, ok := ctx.Deadline()
					if remaining := time.Until(deadline); !ok || remaining > tc.want || remaining < tc.want-time.Second {
						t.Fatalf("call deadline=%s want=%s", remaining, tc.want)
					}
					// A known response after 31 seconds must fit the default paper
					// lease; no sleeping or external provider is needed for this check.
					if tc.want >= time.Minute {
						now = now.Add(31 * time.Second)
					}
					return generation.Result{Content: []byte(`{}`), UsageKnown: true}, nil
				}), nil
			}
			runner := NewCallRunner(store, CallPolicy{PaperCallTimeout: tc.configured}, factory, []string{"qwen"}, func() time.Time { return now }, slog.New(slog.NewTextHandler(io.Discard, nil)))
			result, err := runner.Run(t.Context(), CallRequest{UserID: 1, Provider: "qwen", Model: "qwen3.8-flash", Feature: tc.feature, MaxTokens: 8192})
			if err != nil || result.CallID == "" || store.finished != 1 || store.failure != nil || store.record.LeaseUntil.Sub(started) != max(time.Minute, tc.want+30*time.Second) {
				t.Fatalf("timeout/lease policy mismatch: result=%+v err=%v record=%+v", result, err, store.record)
			}
		})
	}
}
