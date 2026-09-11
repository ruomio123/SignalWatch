package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"signalwatch/internal/source"
)

const (
	maxNameRunes      = 100
	maxObjectiveRunes = 500
	DefaultPage       = 1
	DefaultPageSize   = 20
	MaxPageSize       = 100
)

var (
	ErrInvalidDigestLimit      = errors.New("subscription digest limit must be between 1 and 20")
	ErrInvalidAILanguage       = errors.New("subscription AI language must be zh or en")
	ErrAIConfigurationRequired = errors.New("AI_CONFIGURATION_REQUIRED")
	ErrInvalidSourceID         = errors.New("invalid source id")
	ErrInvalidName             = errors.New("invalid subscription name")
	ErrInvalidObjective        = errors.New("invalid subscription objective")
	ErrInvalidPagination       = errors.New("invalid subscription pagination")
	ErrInvalidFilter           = errors.New("invalid subscription filter")
	ErrInvalidID               = errors.New("invalid subscription id")
	ErrInvalidVersion          = errors.New("invalid subscription version")
	ErrEmptyUpdate             = errors.New("subscription update is empty")
	ErrInvalidStoredRule       = errors.New("invalid stored subscription rule")
)

type SourceCatalog interface {
	Get(ctx context.Context, id uint64) (source.PublicSource, error)
}

type AIConfigurationChecker interface {
	Active(context.Context, uint64) (bool, error)
}

func (service *Service) List(
	ctx context.Context,
	userID uint64,
	input ListInput,
) (ListResult, error) {
	if userID == 0 {
		return ListResult{}, ErrUserNotFound
	}
	if input.Page < 1 || input.PageSize < 1 || input.PageSize > MaxPageSize ||
		input.Page-1 > math.MaxInt/input.PageSize {
		return ListResult{}, ErrInvalidPagination
	}
	if input.Filter.SourceID != nil && *input.Filter.SourceID == 0 {
		return ListResult{}, ErrInvalidFilter
	}

	total, err := service.repository.Count(ctx, userID, input.Filter)
	if err != nil {
		return ListResult{}, fmt.Errorf("count subscriptions: %w", err)
	}
	rows, err := service.repository.List(
		ctx,
		userID,
		input.Filter,
		(input.Page-1)*input.PageSize,
		input.PageSize,
	)
	if err != nil {
		return ListResult{}, fmt.Errorf("list subscriptions: %w", err)
	}

	items := make([]PublicSubscription, 0, len(rows))
	for _, row := range rows {
		item, err := publicSubscription(row)
		if err != nil {
			return ListResult{}, fmt.Errorf("build public subscription %d: %w", row.Subscription.ID, err)
		}
		items = append(items, item)
	}
	return ListResult{
		Items: items, Page: input.Page, PageSize: input.PageSize, Total: total,
	}, nil
}

func (service *Service) Get(
	ctx context.Context,
	userID uint64,
	id uint64,
) (PublicSubscription, error) {
	if userID == 0 {
		return PublicSubscription{}, ErrUserNotFound
	}
	if id == 0 {
		return PublicSubscription{}, ErrInvalidID
	}
	row, err := service.repository.Get(ctx, userID, id)
	if err != nil {
		return PublicSubscription{}, fmt.Errorf("get owned subscription: %w", err)
	}
	result, err := publicSubscription(row)
	if err != nil {
		return PublicSubscription{}, fmt.Errorf("build public subscription %d: %w", id, err)
	}
	return result, nil
}

type Service struct {
	repository      Repository
	sources         SourceCatalog
	now             func() time.Time
	aiConfiguration AIConfigurationChecker
}

func NewService(repository Repository, sources SourceCatalog, checkers ...AIConfigurationChecker) *Service {
	return NewServiceWithClock(repository, sources, time.Now, checkers...)
}
func NewServiceWithClock(repository Repository, sources SourceCatalog, now func() time.Time, checkers ...AIConfigurationChecker) *Service {
	if now == nil {
		now = time.Now
	}
	service := &Service{repository: repository, sources: sources, now: now}
	if len(checkers) > 0 {
		service.aiConfiguration = checkers[0]
	}
	return service
}

func (service *Service) Create(
	ctx context.Context,
	userID uint64,
	input CreateInput,
) (PublicSubscription, error) {
	if userID == 0 {
		return PublicSubscription{}, ErrUserNotFound
	}
	if input.SourceID == 0 {
		return PublicSubscription{}, ErrInvalidSourceID
	}

	catalog, err := service.sources.Get(ctx, input.SourceID)
	if err != nil {
		return PublicSubscription{}, fmt.Errorf("get source catalog entry: %w", err)
	}

	name := strings.TrimSpace(input.Name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxNameRunes {
		return PublicSubscription{}, ErrInvalidName
	}
	objective, err := normalizeObjective(input.Objective)
	if err != nil {
		return PublicSubscription{}, err
	}
	normalizedRules, err := NormalizeRules(input.Rules, catalog)
	if err != nil {
		return PublicSubscription{}, err
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if input.MaxItemsPerDigest != nil && (*input.MaxItemsPerDigest < 1 || *input.MaxItemsPerDigest > 20) {
		return PublicSubscription{}, ErrInvalidDigestLimit
	}
	language := "zh"
	if input.DigestAILanguage != nil {
		language = *input.DigestAILanguage
	}
	if language != "zh" && language != "en" {
		return PublicSubscription{}, ErrInvalidAILanguage
	}
	aiEnabled := input.DigestAIEnabled != nil && *input.DigestAIEnabled
	if aiEnabled {
		if service.aiConfiguration == nil {
			return PublicSubscription{}, ErrAIConfigurationRequired
		}
		active, err := service.aiConfiguration.Active(ctx, userID)
		if err != nil {
			return PublicSubscription{}, err
		}
		if !active {
			return PublicSubscription{}, ErrAIConfigurationRequired
		}
	}
	subscription := Subscription{
		UserID:           userID,
		SourceID:         catalog.ID,
		Name:             name,
		Objective:        objective,
		Enabled:          enabled,
		Version:          InitialVersion,
		DigestAIEnabled:  aiEnabled,
		DigestAILanguage: language,
	}
	if input.MaxItemsPerDigest != nil {
		subscription.MaxItemsPerDigest = *input.MaxItemsPerDigest
	}
	subscription.Category = normalizedRules.Category
	subscription.KeywordsJSON, _ = json.Marshal(normalizedRules.Keywords)
	commandTime := service.now().UTC()
	if err := service.createAtomic(ctx, &subscription, BackfillWindow{
		From: commandTime.Add(-7 * 24 * time.Hour), To: commandTime, MatchedAt: commandTime,
	}); err != nil {
		return PublicSubscription{}, fmt.Errorf("create subscription atomically: %w", err)
	}

	state := "complete"
	if enabled {
		state = "pending"
	}
	return PublicSubscription{
		Backfill:          BackfillStatus{State: state},
		MaxItemsPerDigest: subscription.MaxItemsPerDigest,
		DigestAIEnabled:   subscription.DigestAIEnabled,
		DigestAILanguage:  subscription.DigestAILanguage,
		ID:                subscription.ID,
		Source:            catalog,
		Name:              subscription.Name,
		Objective:         subscription.Objective,
		Enabled:           subscription.Enabled,
		Version:           subscription.Version,
		Rules:             normalizedRules,
		CreatedAt:         subscription.CreatedAt,
		UpdatedAt:         subscription.UpdatedAt,
	}, nil
}

func (service *Service) Update(
	ctx context.Context,
	userID uint64,
	id uint64,
	expectedVersion uint32,
	input UpdateInput,
) (PublicSubscription, error) {
	if userID == 0 {
		return PublicSubscription{}, ErrUserNotFound
	}
	if id == 0 {
		return PublicSubscription{}, ErrInvalidID
	}
	if expectedVersion == 0 {
		return PublicSubscription{}, ErrInvalidVersion
	}
	if input.MaxItemsPerDigest == nil && input.DigestAIEnabled == nil && input.DigestAILanguage == nil && input.Name == nil && !input.ObjectiveSet && input.Enabled == nil && input.Rules == nil {
		return PublicSubscription{}, ErrEmptyUpdate
	}

	if input.MaxItemsPerDigest != nil && (*input.MaxItemsPerDigest < 1 || *input.MaxItemsPerDigest > 20) {
		return PublicSubscription{}, ErrInvalidDigestLimit
	}
	if input.DigestAILanguage != nil && *input.DigestAILanguage != "zh" && *input.DigestAILanguage != "en" {
		return PublicSubscription{}, ErrInvalidAILanguage
	}
	patch := SubscriptionPatch{Enabled: input.Enabled, MaxItemsPerDigest: input.MaxItemsPerDigest, DigestAIEnabled: input.DigestAIEnabled, DigestAILanguage: input.DigestAILanguage}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxNameRunes {
			return PublicSubscription{}, ErrInvalidName
		}
		patch.Name = &name
	}
	if input.ObjectiveSet {
		objective, err := normalizeObjective(input.Objective)
		if err != nil {
			return PublicSubscription{}, err
		}
		patch.ObjectiveSet = true
		patch.Objective = objective
	}

	existing, err := service.repository.Get(ctx, userID, id)
	if err != nil {
		return PublicSubscription{}, fmt.Errorf("get subscription for update: %w", err)
	}
	if input.DigestAIEnabled != nil && *input.DigestAIEnabled && !existing.Subscription.DigestAIEnabled {
		if service.aiConfiguration == nil {
			return PublicSubscription{}, ErrAIConfigurationRequired
		}
		active, err := service.aiConfiguration.Active(ctx, userID)
		if err != nil {
			return PublicSubscription{}, err
		}
		if !active {
			return PublicSubscription{}, ErrAIConfigurationRequired
		}
	}

	var replacementRules *RulesInput
	if input.Rules != nil {
		catalog, err := existing.Source.Public()
		if err != nil {
			return PublicSubscription{}, fmt.Errorf("build source catalog for update: %w", err)
		}
		normalized, err := NormalizeRules(*input.Rules, catalog)
		if err != nil {
			return PublicSubscription{}, err
		}
		replacementRules = &normalized
	}

	updated, err := service.updateAtomic(
		ctx,
		userID,
		id,
		expectedVersion,
		patch,
		replacementRules,
	)
	if err != nil {
		return PublicSubscription{}, fmt.Errorf("update subscription atomically: %w", err)
	}
	updated.Backfill = existing.Subscription.Backfill
	result, err := publicSubscription(QueryResult{
		Subscription: updated,
		Source:       existing.Source,
	})
	if err != nil {
		return PublicSubscription{}, fmt.Errorf("build updated subscription: %w", err)
	}
	return result, nil
}

func (service *Service) Delete(
	ctx context.Context,
	userID uint64,
	id uint64,
	expectedVersion uint32,
) error {
	if userID == 0 {
		return ErrUserNotFound
	}
	if id == 0 {
		return ErrInvalidID
	}
	if expectedVersion == 0 {
		return ErrInvalidVersion
	}
	if err := service.repository.SoftDelete(ctx, userID, id, expectedVersion); err != nil {
		return fmt.Errorf("soft delete subscription: %w", err)
	}
	return nil
}

func normalizeObjective(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}
	if !utf8.ValidString(trimmed) || utf8.RuneCountInString(trimmed) > maxObjectiveRunes {
		return nil, ErrInvalidObjective
	}
	return &trimmed, nil
}

func publicSubscription(row QueryResult) (PublicSubscription, error) {
	source, err := row.Source.Public()
	if err != nil {
		return PublicSubscription{}, err
	}
	item := row.Subscription
	var keywords []string
	if err := json.Unmarshal(item.KeywordsJSON, &keywords); err != nil {
		return PublicSubscription{}, ErrInvalidStoredRule
	}
	if keywords == nil {
		keywords = []string{}
	}
	if item.Backfill.State == "" {
		item.Backfill.State = "complete"
	}
	return PublicSubscription{ID: item.ID, Source: source, Name: item.Name, Objective: item.Objective, Enabled: item.Enabled, Version: item.Version, MaxItemsPerDigest: item.MaxItemsPerDigest, DigestAIEnabled: item.DigestAIEnabled, DigestAILanguage: item.DigestAILanguage, Backfill: item.Backfill, Rules: RulesInput{Category: item.Category, Keywords: keywords}, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}, nil
}
