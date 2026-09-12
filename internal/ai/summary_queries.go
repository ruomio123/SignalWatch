package ai

import (
	"context"
	"errors"
	"signalwatch/internal/digest"
	"signalwatch/internal/insight"
	"sync/atomic"
	"time"
)

type SummaryStore interface {
	Ensure(context.Context, *Task) error
	Find(context.Context, Task) (Task, error)
	Retry(context.Context, Task, time.Time) error
	ReadyDigest(context.Context, Task) ([]Task, error)
}
type SummaryQueries struct {
	repo           SummaryStore
	configurations *ConfigurationService
	enabled        bool
	users          UserReader
	papers         PaperReader
	now            func() time.Time
	hits, misses   atomic.Int64
}
type SummaryState struct {
	Language    string           `json:"language"`
	State       string           `json:"state"`
	FailureCode string           `json:"failure_code,omitempty"`
	RetryAt     *time.Time       `json:"retry_at,omitempty"`
	Content     *insight.Summary `json:"content,omitempty"`
}
type SummaryResponse struct {
	PaperID   uint64         `json:"paper_id"`
	InputHash string         `json:"input_hash"`
	Provider  string         `json:"provider,omitempty"`
	Model     string         `json:"model,omitempty"`
	Items     []SummaryState `json:"items"`
}

func (s *SummaryQueries) Summary(ctx context.Context, userID, paperID uint64, lang string, request bool) (SummaryResponse, error) {
	if !s.enabled {
		return SummaryResponse{}, ErrDisabled
	}
	u, err := s.users.FindActiveByID(ctx, userID)
	if err != nil {
		return SummaryResponse{}, err
	}
	p, err := s.papers.Get(ctx, userID, paperID)
	if err != nil {
		return SummaryResponse{}, err
	}
	if !u.AIEnabled {
		return SummaryResponse{}, ErrDisabled
	}
	if lang == "" {
		lang = u.AILanguage
	}
	if lang != "zh" && lang != "en" {
		return SummaryResponse{}, ErrLanguage
	}
	configuration, err := s.configurations.Get(ctx, userID)
	if err != nil {
		return SummaryResponse{}, err
	}
	if err := configurationUsabilityError(configuration); err != nil {
		return SummaryResponse{}, err
	}
	languages := []string{lang}
	input := Input{Papers: []insight.Paper{{ID: p.ID, Title: p.Title, Abstract: p.Abstract}}}
	profile := generationProfile(configuration.ProviderID, configuration.ModelID)
	result := SummaryResponse{PaperID: p.ID, InputHash: insight.PaperHash(input.Papers[0]), Provider: configuration.ProviderID, Model: configuration.ModelID, Items: []SummaryState{}}
	for _, language := range languages {
		key := newUserTask(PaperKind, userID, p.ID, "", language, configuration.ProviderID, configuration.ModelID, configuration.Generation, configuration.Version, profile, input, 1, s.now().UTC())
		var t Task
		if request {
			err = s.repo.Ensure(ctx, &key)
			if err == nil && key.Status == "failed" {
				if key.NextRetryAt.After(s.now().UTC()) {
					at := key.NextRetryAt
					code := "rate_limited"
					if key.FailureCode == "daily_limit" || key.FailureCode == "budget_exhausted" {
						code = "daily_limit"
					}
					return SummaryResponse{}, &CallError{Code: code, RetryAt: &at}
				}
				err = s.repo.Retry(ctx, key, s.now().UTC())
				if err == nil {
					key.Status = "pending"
					key.Attempts = 0
				}
			}
			t = key
		} else {
			t, err = s.repo.Find(ctx, key)
		}
		item := SummaryState{Language: language, State: "unavailable"}
		if err != nil && !errors.Is(err, ErrTaskNotFound) {
			return SummaryResponse{}, err
		}
		if err == nil {
			item.State = t.Status
			if t.Status == "failed" {
				item.FailureCode = t.FailureCode
				if t.NextRetryAt.After(s.now()) {
					at := t.NextRetryAt
					item.RetryAt = &at
				}
			}
			if t.Status == "processing" {
				item.State = "pending"
			}
			if t.Status == "ready" {
				var content insight.Summary
				if insight.Decode(t.PayloadJSON, &content) == nil && insight.ValidateSummary(content, input.Papers[0]) == nil {
					item.Content = &content
					s.hits.Add(1)
				} else {
					item.State = "unavailable"
				}
			}
		}
		if item.Content == nil {
			s.misses.Add(1)
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}
func inputs(items []digest.Item) []insight.Paper {
	out := make([]insight.Paper, len(items))
	for i, p := range items {
		out[i] = insight.Paper{ID: p.PaperID, Title: p.Title, Abstract: p.Abstract}
	}
	return out
}

// Lookup never generates, schedules, or calls the provider. The caller owns a 200ms deadline.
func (s *SummaryQueries) Lookup(ctx context.Context, u digest.User, job digest.Job, items []digest.Item) (insight.Enrichment, error) {
	result := insight.Enrichment{Papers: map[uint64][]insight.PaperResult{}}
	if !s.enabled || !u.DigestAIEnabled {
		return result, nil
	}
	configuration, err := s.configurations.Get(ctx, u.ID)
	if err != nil || !configuration.Usable {
		return result, err
	}
	papers := inputs(items)
	var rows []Task
	if u.DigestAIEnabled && len(papers) >= 2 {
		profile := generationProfile(configuration.ProviderID, configuration.ModelID)
		key := newUserTask(DigestKind, u.ID, u.ID, job.LocalDate, u.DigestAILanguage, configuration.ProviderID, configuration.ModelID, configuration.Generation, configuration.Version, profile, Input{SubscriptionID: job.SubscriptionID, Papers: papers, Timezone: u.Timezone, Limit: int(u.MaxItemsPerDigest)}, 0, s.now().UTC())
		rows, err = s.repo.ReadyDigest(ctx, key)
		if err != nil {
			return result, err
		}
		for _, t := range rows {
			var content insight.Overview
			if insight.Decode(t.PayloadJSON, &content) == nil && insight.ValidateOverview(content, papers) == nil {
				result.Digests = append(result.Digests, insight.DigestResult{Language: t.Language, Content: content})
			}
		}
	}
	s.hits.Add(int64(len(result.Digests)))
	if u.DigestAIEnabled && len(papers) >= 2 {
		s.misses.Add(int64(1 - len(result.Digests)))
	}
	return result, nil
}

func generationProfile(provider, model string) string {
	return insight.Hash([]string{provider, model, insight.SchemaVersion, "prompt-v2-byok"})
}
