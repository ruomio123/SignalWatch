package ai

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"signalwatch/internal/digest"
	"signalwatch/internal/generation"
	"signalwatch/internal/insight"
	"sync/atomic"
	"time"
)

type ExecutionStore interface {
	Claim(context.Context, Task, time.Time) (Task, bool, error)
	MarkAttempt(context.Context, Task, time.Time) (bool, error)
	Finish(context.Context, Task, []byte, string, time.Time, bool, time.Time) error
	Requeue(context.Context, Task, time.Time, time.Time, string) error
	PaperEligible(context.Context, Task) (bool, error)
}
type Executor struct {
	repo                      ExecutionStore
	configurations            *ConfigurationService
	calls                     *CallRunner
	enabledProviders          []string
	digest                    digest.CandidateReader
	coordinator               digest.CompletionReader
	logger                    *slog.Logger
	now                       func() time.Time
	active, succeeded, failed atomic.Int64
}

func (s *Executor) warn(stage string) {
	s.logger.Warn("AI execution unavailable", "module", "ai", "stage", stage)
}
func (s *Executor) Process(ctx context.Context, candidate Task) {
	claimContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	t, acquired, err := s.repo.Claim(claimContext, candidate, s.now().UTC())
	cancel()
	if err != nil {
		s.warn("claim")
		return
	}
	if !acquired {
		return
	}
	s.active.Add(1)
	defer s.active.Add(-1)
	work, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	now := s.now().UTC()
	var input Input
	finish := func(payload []byte, code string, next time.Time, terminal bool) bool {
		save, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := s.repo.Finish(save, t, payload, code, next, terminal, s.now().UTC()); err != nil {
			s.warn("save")
			return false
		}
		return true
	}
	requeue := func(next time.Time, code string) {
		save, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := s.repo.Requeue(save, t, next, now, code); err != nil {
			s.warn("requeue")
		}
	}
	if json.Unmarshal(t.InputJSON, &input) != nil || len(input.Papers) == 0 {
		finish(nil, "invalid_input", now, true)
		return
	}
	eligible, err := s.eligible(work, t, input)
	if err != nil {
		requeue(now.Add(time.Minute), "eligibility_unavailable")
		return
	}
	if !eligible {
		finish(nil, "obsolete", now, true)
		return
	}
	configuration, err := s.configurations.ForUse(work, t.OwnerUserID, t.ConfigVersion)
	if err != nil || configuration.Provider != t.ProviderID || configuration.Model != t.ModelID {
		finish(nil, "configuration_changed", now, true)
		return
	}
	defer clear(configuration.Secret)
	request, _ := json.Marshal(struct {
		Language string          `json:"language"`
		Papers   []insight.Paper `json:"papers"`
	}{t.Language, input.Papers})
	maxTokens := 600
	if t.Kind == PaperKind {
		maxTokens = 1000
	}
	feature := FeatureDigest
	if t.Kind == PaperKind {
		feature = FeaturePaper
	}
	var result generation.Result
	result, err = s.calls.Run(work, CallRequest{UserID: t.OwnerUserID, Feature: feature, Provider: t.ProviderID, Model: t.ModelID, Key: string(configuration.Secret), Generation: t.ConfigGeneration, Version: t.ConfigVersion,
		System: GenerationPrompt(t.Kind), Input: request, MaxTokens: maxTokens,
		BeforeStart: func(c context.Context) error {
			ok, e := s.repo.MarkAttempt(c, t, s.now().UTC())
			if e != nil {
				return e
			}
			if !ok {
				return ErrLeaseLost
			}
			return nil
		},
		Validate: func(r generation.Result) error {
			result = r
			if t.Kind == PaperKind {
				var content insight.Summary
				if e := insight.Decode(r.Content, &content); e != nil {
					return &generation.Failure{Code: "invalid_output"}
				}
				if e := insight.ValidateSummary(content, input.Papers[0]); e != nil {
					return &generation.Failure{Code: "invalid_output"}
				}
			} else {
				var content insight.Overview
				if e := insight.Decode(r.Content, &content); e != nil {
					return &generation.Failure{Code: "invalid_output"}
				}
				if e := insight.ValidateOverview(content, input.Papers); e != nil {
					return &generation.Failure{Code: "invalid_output"}
				}
			}
			return nil
		},
		Commit: func(c context.Context) error {
			valid, e := s.eligible(c, t, input)
			if e != nil {
				return e
			}
			if !valid {
				return &CallError{Code: "obsolete"}
			}
			if e = s.repo.Finish(c, t, result.Content, "", s.now().UTC(), false, s.now().UTC()); e != nil {
				return e
			}
			return nil
		},
	})
	if err != nil {
		var ce *CallError
		if errors.As(err, &ce) {
			if ce.Code == "rate_limited" || ce.Code == "call_in_progress" {
				next := now.Add(time.Second)
				if ce.RetryAt != nil {
					next = *ce.RetryAt
				}
				requeue(next, ce.Code)
				return
			}
			if ce.Code == "daily_limit" {
				at := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
				finish(nil, "daily_limit", at, true)
				return
			}
			next := s.now().UTC()
			if ce.RetryAt != nil {
				next = *ce.RetryAt
			}
			finish(nil, ce.Code, next, true)
			if ce.Code == "credential_rejected" {
				cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
				defer stop()
				_ = s.configurations.store.MarkInvalid(cleanup, t.OwnerUserID, t.ConfigVersion)
			}
		} else {
			finish(nil, "storage_failed", s.now().UTC(), true)
		}
		s.failed.Add(1)
		return
	}
	mark, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	_ = s.configurations.store.MarkUsed(mark, t.OwnerUserID, t.ConfigVersion, s.now().UTC())
	s.succeeded.Add(1)
}

func (s *Executor) eligible(ctx context.Context, t Task, input Input) (bool, error) {
	if t.Kind == PaperKind {
		return s.repo.PaperEligible(ctx, t)
	}
	if input.SubscriptionID == 0 {
		return false, nil
	}
	u, err := s.digest.FindActiveUser(ctx, t.ScopeID, input.SubscriptionID)
	if err != nil {
		return false, err
	}
	if !u.DigestAIEnabled || u.DigestAILanguage != t.Language {
		return false, nil
	}
	configuration, err := s.configurations.Get(ctx, u.ID)
	if err != nil || !configuration.Usable || configuration.Generation != t.ConfigGeneration || configuration.Version != t.ConfigVersion {
		return false, err
	}
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return false, err
	}
	local := s.now().In(loc)
	// Only today's or tomorrow's prepared window can still be useful.
	if t.LocalDate != local.Format("2006-01-02") && t.LocalDate != local.AddDate(0, 0, 1).Format("2006-01-02") {
		return false, nil
	}
	if s.coordinator != nil {
		complete, err := s.coordinator.IsComplete(ctx, digest.Job{SubscriptionID: input.SubscriptionID, UserID: u.ID, LocalDate: t.LocalDate})
		if err != nil || complete {
			return false, err
		}
	}
	items, err := s.digest.ListCandidates(ctx, u.ID, input.SubscriptionID, int(u.MaxItemsPerDigest))
	if err != nil {
		return false, err
	}
	return insight.Hash(Input{SubscriptionID: input.SubscriptionID, Papers: inputs(items), Timezone: u.Timezone, Limit: int(u.MaxItemsPerDigest)}) == t.InputHash, nil
}
