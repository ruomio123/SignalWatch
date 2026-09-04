package subscription

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"signalwatch/internal/source"
)

func TestServiceCreatePersistsNormalizedSubscriptionAndDefaultsEnabled(t *testing.T) {
	createdAt := time.Date(2026, time.September, 4, 1, 2, 3, 0, time.UTC)
	var captured Subscription
	var capturedRules []Rule
	repository := subscriptionRepositoryStub{create: func(_ context.Context, item *Subscription, rules []Rule) error {
		item.ID = 88
		item.CreatedAt = createdAt
		item.UpdatedAt = createdAt
		captured = *item
		capturedRules = append([]Rule(nil), rules...)
		return nil
	}}
	sources := sourceCatalogStub{get: func(_ context.Context, id uint64) (source.PublicSource, error) {
		catalog := arXivCatalog()
		catalog.ID = id
		return catalog, nil
	}}
	objective := "  Agent tools  "

	created, err := NewService(repository, sources).Create(context.Background(), 42, CreateInput{
		SourceID:  1,
		Name:      "  Agent papers  ",
		Objective: &objective,
		Rules: RulesInput{
			Categories: []string{"cs.ai"}, Authors: []string{" Jane\tDoe "},
			IncludeKeywords: []string{"Tool Use"}, ExcludeKeywords: []string{"SURVEY"},
		},
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if captured.UserID != 42 || captured.SourceID != 1 || captured.Name != "Agent papers" ||
		captured.Objective == nil || *captured.Objective != "Agent tools" || !captured.Enabled ||
		captured.Version != InitialVersion {
		t.Fatalf("unexpected persisted subscription %+v", captured)
	}
	if len(capturedRules) != 4 {
		t.Fatalf("expected four rules, got %+v", capturedRules)
	}
	if capturedRules[0].RuleValue != "cs.AI" || capturedRules[0].NormalizedValue != "cs.AI" ||
		capturedRules[1].NormalizedValue != "jane doe" ||
		capturedRules[2].NormalizedValue != "tool use" ||
		capturedRules[3].NormalizedValue != "survey" {
		t.Fatalf("unexpected normalized persistence rules %+v", capturedRules)
	}
	if created.ID != 88 || created.Source.ID != 1 || !created.Enabled || created.Version != 1 ||
		created.Rules.Categories[0] != "cs.AI" || created.CreatedAt != createdAt {
		t.Fatalf("unexpected public response %+v", created)
	}
}

func TestServiceCreatePreservesExplicitFalseAndBlankObjective(t *testing.T) {
	disabled := false
	blank := " \t "
	repository := subscriptionRepositoryStub{create: func(_ context.Context, item *Subscription, _ []Rule) error {
		if item.Enabled {
			t.Fatal("explicit false must be preserved")
		}
		if item.Objective != nil {
			t.Fatalf("blank objective must become nil, got %q", *item.Objective)
		}
		return nil
	}}
	_, err := NewService(repository, sourceCatalogStub{get: validSourceGet}).Create(
		context.Background(), 42,
		CreateInput{SourceID: 1, Name: "paused", Objective: &blank, Enabled: &disabled,
			Rules: RulesInput{Categories: []string{"cs.AI"}}},
	)
	if err != nil {
		t.Fatalf("create paused subscription: %v", err)
	}
}

func TestServiceCreateValidatesNameAndObjectiveBoundaries(t *testing.T) {
	validObjective := strings.Repeat("界", maxObjectiveRunes)
	tooLongObjective := strings.Repeat("界", maxObjectiveRunes+1)
	tests := []struct {
		name      string
		inputName string
		objective *string
		wantErr   error
	}{
		{"blank name", "  ", nil, ErrInvalidName},
		{"name boundary", strings.Repeat("界", maxNameRunes), &validObjective, nil},
		{"long name", strings.Repeat("界", maxNameRunes+1), nil, ErrInvalidName},
		{"objective boundary", "valid", &validObjective, nil},
		{"long objective", "valid", &tooLongObjective, ErrInvalidObjective},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			repository := subscriptionRepositoryStub{create: func(context.Context, *Subscription, []Rule) error {
				calls++
				return nil
			}}
			_, err := NewService(repository, sourceCatalogStub{get: validSourceGet}).Create(
				context.Background(), 42,
				CreateInput{SourceID: 1, Name: test.inputName, Objective: test.objective,
					Rules: RulesInput{Categories: []string{"cs.AI"}}},
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("expected %v, got %v", test.wantErr, err)
			}
			if test.wantErr != nil && calls != 0 {
				t.Fatal("invalid input must not be persisted")
			}
		})
	}
}

func TestServiceCreateLoadsEnabledSourceBeforeRuleValidation(t *testing.T) {
	missing := sourceCatalogStub{get: func(context.Context, uint64) (source.PublicSource, error) {
		return source.PublicSource{}, source.ErrNotFound
	}}
	calls := 0
	repository := subscriptionRepositoryStub{create: func(context.Context, *Subscription, []Rule) error {
		calls++
		return nil
	}}
	_, err := NewService(repository, missing).Create(context.Background(), 42, CreateInput{
		SourceID: 999, Name: "valid", Rules: RulesInput{},
	})
	if !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("expected source not found, got %v", err)
	}
	if calls != 0 {
		t.Fatal("missing or disabled source must not create a subscription")
	}
}

func TestServiceCreateRejectsMissingSourceBeforeCatalogLookup(t *testing.T) {
	lookupCalls := 0
	_, err := NewService(subscriptionRepositoryStub{}, sourceCatalogStub{get: func(context.Context, uint64) (source.PublicSource, error) {
		lookupCalls++
		return source.PublicSource{}, nil
	}}).Create(context.Background(), 42, CreateInput{})
	if !errors.Is(err, ErrInvalidSourceID) || lookupCalls != 0 {
		t.Fatalf("expected local source validation, got %v and %d lookups", err, lookupCalls)
	}
}

type subscriptionRepositoryStub struct {
	create func(context.Context, *Subscription, []Rule) error
}

func (stub subscriptionRepositoryStub) CreateAtomic(ctx context.Context, item *Subscription, rules []Rule) error {
	if stub.create == nil {
		return nil
	}
	return stub.create(ctx, item, rules)
}

type sourceCatalogStub struct {
	get func(context.Context, uint64) (source.PublicSource, error)
}

func (stub sourceCatalogStub) Get(ctx context.Context, id uint64) (source.PublicSource, error) {
	return stub.get(ctx, id)
}

func validSourceGet(context.Context, uint64) (source.PublicSource, error) {
	return arXivCatalog(), nil
}
