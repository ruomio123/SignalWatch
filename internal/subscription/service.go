package subscription

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
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
	ErrInvalidSourceID   = errors.New("invalid source id")
	ErrInvalidName       = errors.New("invalid subscription name")
	ErrInvalidObjective  = errors.New("invalid subscription objective")
	ErrInvalidPagination = errors.New("invalid subscription pagination")
	ErrInvalidFilter     = errors.New("invalid subscription filter")
	ErrInvalidID         = errors.New("invalid subscription id")
	ErrInvalidVersion    = errors.New("invalid subscription version")
	ErrEmptyUpdate       = errors.New("subscription update is empty")
	ErrInvalidStoredRule = errors.New("invalid stored subscription rule")
)

type SourceCatalog interface {
	Get(ctx context.Context, id uint64) (source.PublicSource, error)
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
	repository Repository
	sources    SourceCatalog
}

func NewService(repository Repository, sources SourceCatalog) *Service {
	return &Service{repository: repository, sources: sources}
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
	subscription := Subscription{
		UserID:    userID,
		SourceID:  catalog.ID,
		Name:      name,
		Objective: objective,
		Enabled:   enabled,
		Version:   InitialVersion,
	}
	rules := persistenceRules(normalizedRules)
	if err := service.repository.CreateAtomic(ctx, &subscription, rules); err != nil {
		return PublicSubscription{}, fmt.Errorf("create subscription atomically: %w", err)
	}

	return PublicSubscription{
		ID:        subscription.ID,
		Source:    catalog,
		Name:      subscription.Name,
		Objective: subscription.Objective,
		Enabled:   subscription.Enabled,
		Version:   subscription.Version,
		Rules:     publicRules(normalizedRules),
		CreatedAt: subscription.CreatedAt,
		UpdatedAt: subscription.UpdatedAt,
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
	if input.Name == nil && !input.ObjectiveSet && input.Enabled == nil && input.Rules == nil {
		return PublicSubscription{}, ErrEmptyUpdate
	}

	patch := SubscriptionPatch{Enabled: input.Enabled}
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

	var replacementRules *[]Rule
	if input.Rules != nil {
		catalog, err := existing.Source.Public()
		if err != nil {
			return PublicSubscription{}, fmt.Errorf("build source catalog for update: %w", err)
		}
		normalized, err := NormalizeRules(*input.Rules, catalog)
		if err != nil {
			return PublicSubscription{}, err
		}
		rules := persistenceRules(normalized)
		replacementRules = &rules
	}

	updated, rules, err := service.repository.UpdateAtomic(
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
	result, err := publicSubscription(QueryResult{
		Subscription: updated,
		Source:       existing.Source,
		Rules:        rules,
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

func persistenceRules(rules NormalizedRules) []Rule {
	total := len(rules.Categories) + len(rules.Authors) +
		len(rules.IncludeKeywords) + len(rules.ExcludeKeywords)
	result := make([]Rule, 0, total)
	appendGroup := func(ruleType string, group []NormalizedRule) {
		for _, normalized := range group {
			result = append(result, Rule{
				RuleType:        ruleType,
				RuleValue:       normalized.RuleValue,
				NormalizedValue: normalized.NormalizedValue,
			})
		}
	}
	appendGroup(source.RuleTypeCategory, rules.Categories)
	appendGroup(source.RuleTypeAuthor, rules.Authors)
	appendGroup(source.RuleTypeIncludeKeyword, rules.IncludeKeywords)
	appendGroup(source.RuleTypeExcludeKeyword, rules.ExcludeKeywords)
	return result
}

func publicRules(rules NormalizedRules) PublicRules {
	values := func(group []NormalizedRule) []string {
		result := make([]string, 0, len(group))
		for _, item := range group {
			result = append(result, item.RuleValue)
		}
		return result
	}
	return PublicRules{
		Categories:      values(rules.Categories),
		Authors:         values(rules.Authors),
		IncludeKeywords: values(rules.IncludeKeywords),
		ExcludeKeywords: values(rules.ExcludeKeywords),
	}
}

func publicSubscription(row QueryResult) (PublicSubscription, error) {
	publicSource, err := row.Source.Public()
	if err != nil {
		return PublicSubscription{}, err
	}
	rules := PublicRules{
		Categories:      make([]string, 0),
		Authors:         make([]string, 0),
		IncludeKeywords: make([]string, 0),
		ExcludeKeywords: make([]string, 0),
	}
	for _, rule := range row.Rules {
		switch rule.RuleType {
		case source.RuleTypeCategory:
			rules.Categories = append(rules.Categories, rule.RuleValue)
		case source.RuleTypeAuthor:
			rules.Authors = append(rules.Authors, rule.RuleValue)
		case source.RuleTypeIncludeKeyword:
			rules.IncludeKeywords = append(rules.IncludeKeywords, rule.RuleValue)
		case source.RuleTypeExcludeKeyword:
			rules.ExcludeKeywords = append(rules.ExcludeKeywords, rule.RuleValue)
		default:
			return PublicSubscription{}, fmt.Errorf("%w: %q", ErrInvalidStoredRule, rule.RuleType)
		}
	}

	item := row.Subscription
	return PublicSubscription{
		ID: item.ID, Source: publicSource, Name: item.Name, Objective: item.Objective,
		Enabled: item.Enabled, Version: item.Version, Rules: rules,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}, nil
}
