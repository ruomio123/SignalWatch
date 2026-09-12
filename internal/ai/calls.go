package ai

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"signalwatch/internal/generation"
	"sync"
	"time"
)

// CallPolicy is shared by interactive validation and background generation.
// Zero daily limits explicitly mean unlimited; intervals are independent of billing.
type CallPolicy struct {
	ConfigInterval              time.Duration
	GenerationInterval          time.Duration
	ConfigDailyLimit            int
	PaperDailyLimit             int
	DigestDailyLimit            int
	SubscriptionAgentDailyLimit int
	PaperQADailyLimit           int
}

func DefaultCallPolicy() CallPolicy {
	return CallPolicy{ConfigInterval: 10 * time.Second, GenerationInterval: 2 * time.Second}
}
func (p CallPolicy) limit(feature string) int {
	switch feature {
	case FeatureConfigTest:
		return p.ConfigDailyLimit
	case FeatureSubscriptionAgent:
		return p.SubscriptionAgentDailyLimit
	case FeaturePaperQA:
		return p.PaperQADailyLimit
	case FeaturePaper:
		return p.PaperDailyLimit
	default:
		return p.DigestDailyLimit
	}
}
func (p CallPolicy) interval(feature string) time.Duration {
	if feature == FeatureConfigTest {
		return p.ConfigInterval
	}
	return p.GenerationInterval
}
func validFeature(f string) bool {
	return f == FeatureConfigTest || f == FeaturePaper || f == FeatureDigest || f == FeatureSubscriptionAgent || f == FeaturePaperQA
}

type CallError struct {
	Code    string
	CallID  string
	RetryAt *time.Time
}

func (e *CallError) Error() string { return e.Code }

type CallRecord struct {
	ID                string     `json:"id" gorm:"primaryKey"`
	UserID            uint64     `json:"-"`
	Feature           string     `json:"feature"`
	Provider          string     `json:"provider"`
	Model             string     `json:"model"`
	Generation        string     `json:"-"`
	Version           uint64     `json:"-"`
	RequestID         string     `json:"-"`
	Status            string     `json:"status"`
	FailureCode       string     `json:"failure_code,omitempty"`
	ProviderCode      string     `json:"-"`
	ProviderRequestID string     `json:"-"`
	HTTPStatus        int        `json:"-"`
	CreatedAt         time.Time  `json:"created_at"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	LeaseUntil        time.Time  `json:"-"`
	DurationMS        int64      `json:"duration_ms"`
	InputTokens       int64      `json:"input_tokens"`
	OutputTokens      int64      `json:"output_tokens"`
	UsageKnown        bool       `json:"usage_known"`
}

func (CallRecord) TableName() string { return "ai_call_records" }

type CallPage struct {
	Items    []CallRecord `json:"items"`
	Total    int64        `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}
type FeatureUsage struct {
	Feature            string     `json:"feature"`
	DailyLimit         int        `json:"daily_limit"`
	Calls              int        `json:"calls"`
	Remaining          *int       `json:"remaining,omitempty"`
	ResetAt            *time.Time `json:"reset_at,omitempty"`
	MinIntervalSeconds int        `json:"min_interval_seconds"`
}
type CallStore interface {
	Admit(context.Context, CallRecord, CallPolicy) error
	Start(context.Context, string, time.Time) error
	Finish(context.Context, string, generation.Result, error, time.Time) error
	Release(context.Context, CallRecord, time.Time) error
	Recover(context.Context, time.Time) error
	Usage(context.Context, uint64, time.Time, time.Time) ([]UsageRow, error)
	List(context.Context, uint64, string, string, int, int) (CallPage, error)
}
type CallRequest struct {
	Schema                                               *generation.Schema
	UserID                                               uint64
	Feature, Provider, Model, Key, Generation, RequestID string
	Version                                              uint64
	System                                               string
	Input                                                []byte
	MaxTokens                                            int
	BeforeStart                                          func(context.Context) error
	Validate                                             func(generation.Result) error
	Commit                                               func(context.Context) error
}

// Optional transport capability; clients without native structured output still
// receive the same schema in the prompt and undergo the same local validation.
type structuredGenerator interface {
	GenerateStructured(context.Context, string, []byte, int, *generation.Schema) (generation.Result, error)
}
type CallRunner struct {
	store    CallStore
	policy   CallPolicy
	factory  ClientFactory
	enabled  []string
	now      func() time.Time
	logger   *slog.Logger
	mu       sync.Mutex
	outcomes map[string]int64
}

func NewCallRunner(store CallStore, policy CallPolicy, factory ClientFactory, enabled []string, now func() time.Time, logger *slog.Logger) *CallRunner {
	if store == nil || factory == nil || now == nil || logger == nil {
		panic("invalid call dependencies")
	}
	return &CallRunner{store: store, policy: policy, factory: factory, enabled: append([]string(nil), enabled...), now: now, logger: logger, outcomes: map[string]int64{}}
}

type callLeaseKey struct{}
type callRequestKey struct{}

type callLease struct {
	ID  string
	Now func() time.Time
}

func activeCall(ctx context.Context) string {
	v, _ := ctx.Value(callLeaseKey{}).(callLease)
	return v.ID
}
func activeCallTime(ctx context.Context) time.Time {
	v, ok := ctx.Value(callLeaseKey{}).(callLease)
	if ok {
		return v.Now().UTC()
	}
	return time.Now().UTC()
}
func (r *CallRunner) Run(ctx context.Context, req CallRequest) (generation.Result, error) {
	if !validFeature(req.Feature) {
		return generation.Result{}, &CallError{Code: "invalid_input"}
	}
	client, err := r.factory(r.enabled, req.Provider, req.Model, req.Key)
	if err != nil {
		return generation.Result{}, err
	}
	now := r.now().UTC()
	if req.RequestID == "" {
		req.RequestID, _ = ctx.Value(callRequestKey{}).(string)
	}
	c := CallRecord{ID: rand.Text(), UserID: req.UserID, Feature: req.Feature, Provider: req.Provider, Model: req.Model, Generation: req.Generation, Version: req.Version, RequestID: req.RequestID, Status: "reserved", CreatedAt: now, LeaseUntil: now.Add(time.Minute)}
	if err = r.store.Admit(ctx, c, r.policy); err != nil {
		return generation.Result{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if e := r.store.Release(cleanup, c, r.now().UTC()); e != nil {
			r.logger.Warn("AI call release failed", "call_id", c.ID)
		}
	}()
	ctx = context.WithValue(ctx, callLeaseKey{}, callLease{c.ID, r.now})
	if req.BeforeStart != nil {
		if err = req.BeforeStart(ctx); err != nil {
			return generation.Result{}, err
		}
	}
	if err = ctx.Err(); err != nil {
		return generation.Result{}, err
	}
	if err = r.store.Start(ctx, c.ID, r.now().UTC()); err != nil {
		return generation.Result{}, err
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	system := req.System
	if req.Schema != nil {
		system += req.Schema.Instructions()
	}
	var result generation.Result
	var callErr error
	if structured, ok := client.(structuredGenerator); ok && req.Schema != nil {
		result, callErr = structured.GenerateStructured(call, system, req.Input, req.MaxTokens, req.Schema)
	} else {
		result, callErr = client.GenerateLimit(call, system, req.Input, req.MaxTokens)
	}
	result.CallID = c.ID
	if call.Err() != nil && callErr == nil {
		code := "timeout"
		if errors.Is(call.Err(), context.Canceled) {
			code = "result_unknown"
		}
		callErr = &generation.Failure{Code: code}
	}
	cancel()
	if callErr == nil && req.Validate != nil {
		callErr = req.Validate(result)
	}
	finished := r.now().UTC()
	if !c.LeaseUntil.After(finished) {
		callErr = &generation.Failure{Code: "result_unknown"}
	}
	cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
	settleErr := r.store.Finish(cleanup, c.ID, result, callErr, finished)
	stop()
	code := "success"
	if callErr != nil {
		code = callFailure(callErr)
	}
	r.mu.Lock()
	r.outcomes[req.Feature+":"+code]++
	r.mu.Unlock()
	attrs := []any{"module", "ai", "call_id", c.ID, "request_id", req.RequestID, "feature", req.Feature, "provider", req.Provider, "model", req.Model, "outcome", code, "duration_ms", finished.Sub(now).Milliseconds()}
	var failure *generation.Failure
	if errors.As(callErr, &failure) {
		attrs = append(attrs, "validation_path", failure.ValidationPath, "validation_rule", failure.ValidationRule, "provider_code", failure.ProviderCode, "provider_request_id", failure.ProviderRequestID, "http_status", failure.HTTPStatus)
	}
	r.logger.Info("AI call completed", attrs...)
	if settleErr != nil {
		return result, &CallError{Code: "storage_failed", CallID: c.ID}
	}
	if callErr != nil {
		e := &CallError{Code: code, CallID: c.ID}
		if failure != nil && failure.RetryAfter > 0 {
			at := finished.Add(failure.RetryAfter)
			e.RetryAt = &at
		}
		return result, e
	}
	if req.Commit != nil {
		if err = req.Commit(ctx); err != nil {
			if !errors.Is(err, ErrConfigurationConflict) {
				return result, &CallError{Code: "storage_failed", CallID: c.ID}
			}
			return result, err
		}
	}
	return result, nil
}
func callFailure(err error) string {
	var f *generation.Failure
	if errors.As(err, &f) {
		if generation.IsOutputFailure(f.Code) {
			return f.Code
		}
		switch f.Code {
		case "credential_rejected", "model_access_denied", "provider_rate_limited", "provider_unavailable", "provider_rejected", "transport_failed", "timeout", "result_unknown", "output_truncated", "invalid_response", "invalid_output":
			return f.Code
		}
		return "provider_rejected"
	}
	return "invalid_output"
}
func (r *CallRunner) Today(ctx context.Context, u uint64) ([]FeatureUsage, error) {
	now := r.now().UTC()
	rows, err := r.store.Usage(ctx, u, now, now)
	if err != nil {
		return nil, err
	}
	out := []FeatureUsage{}
	for _, f := range []string{FeatureConfigTest, FeaturePaper, FeatureDigest, FeatureSubscriptionAgent, FeaturePaperQA} {
		v := FeatureUsage{Feature: f, DailyLimit: r.policy.limit(f), MinIntervalSeconds: int(r.policy.interval(f) / time.Second)}
		for _, row := range rows {
			if row.Feature == f {
				v.Calls = row.Calls
			}
		}
		if v.DailyLimit > 0 {
			n := max(0, v.DailyLimit-v.Calls)
			v.Remaining = &n
			at := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
			v.ResetAt = &at
		}
		out = append(out, v)
	}
	return out, nil
}
func (r *CallRunner) Stats() map[string]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int64{}
	for k, v := range r.outcomes {
		out[k] = v
	}
	return out
}
