package subscription

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"signalwatch/internal/source"
)

const (
	maxNameRunes      = 100
	maxObjectiveRunes = 500
)

var (
	ErrInvalidSourceID  = errors.New("invalid source id")
	ErrInvalidName      = errors.New("invalid subscription name")
	ErrInvalidObjective = errors.New("invalid subscription objective")
)

type SourceCatalog interface {
	Get(ctx context.Context, id uint64) (source.PublicSource, error)
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
