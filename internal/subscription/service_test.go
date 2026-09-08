package subscription

import (
	"context"
	"encoding/json"
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
			Categories: []string{"cs.ai"}, IncludeKeywords: []string{"Tool Use"},
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
	if len(capturedRules) != 2 {
		t.Fatalf("expected category and keyword rules, got %+v", capturedRules)
	}
	if capturedRules[0].RuleValue != "cs.AI" || capturedRules[0].NormalizedValue != "cs.AI" ||
		capturedRules[1].NormalizedValue != "tool use" {
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

func TestServiceListUsesPaginationAndBuildsSafePublicItems(t *testing.T) {
	enabled := false
	sourceID := uint64(3)
	endpoint := "https://secret.example.test/feed"
	createdAt := time.Date(2026, time.September, 4, 1, 2, 3, 0, time.UTC)
	var gotUserID uint64
	var gotFilter ListFilter
	var gotOffset, gotLimit int
	repository := subscriptionRepositoryStub{
		count: func(_ context.Context, userID uint64, filter ListFilter) (int64, error) {
			gotUserID, gotFilter = userID, filter
			return 21, nil
		},
		list: func(_ context.Context, userID uint64, filter ListFilter, offset, limit int) ([]QueryResult, error) {
			gotUserID, gotFilter, gotOffset, gotLimit = userID, filter, offset, limit
			return []QueryResult{{
				Subscription: Subscription{ID: 9, Name: "paused", SourceID: 3, Version: 2, CreatedAt: createdAt},
				Source: source.Source{ID: 3, SourceKey: "arxiv", Kind: source.KindArXiv, Name: "arXiv",
					Endpoint: &endpoint, ConfigJSON: json.RawMessage(`{"allowed_categories":["cs.AI"],"token":"secret"}`)},
				Rules: []Rule{{RuleType: source.RuleTypeCategory, RuleValue: "cs.AI", NormalizedValue: "must-not-leak"}},
			}}, nil
		},
	}

	result, err := NewService(repository, sourceCatalogStub{get: validSourceGet}).List(
		context.Background(), 42,
		ListInput{Page: 2, PageSize: 10, Filter: ListFilter{Enabled: &enabled, SourceID: &sourceID}},
	)
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if gotUserID != 42 || gotOffset != 10 || gotLimit != 10 ||
		gotFilter.Enabled == nil || *gotFilter.Enabled ||
		gotFilter.SourceID == nil || *gotFilter.SourceID != 3 {
		t.Fatalf("unexpected repository query user=%d filter=%+v offset=%d limit=%d", gotUserID, gotFilter, gotOffset, gotLimit)
	}
	if result.Total != 21 || result.Page != 2 || result.PageSize != 10 || len(result.Items) != 1 {
		t.Fatalf("unexpected list result %+v", result)
	}
	item := result.Items[0]
	if item.Rules.Categories[0] != "cs.AI" || item.Rules.Authors == nil ||
		item.Rules.IncludeKeywords == nil || item.Rules.ExcludeKeywords == nil {
		t.Fatalf("expected four non-nil rule groups, got %+v", item.Rules)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal public item: %v", err)
	}
	for _, secret := range []string{"endpoint", "config_json", "normalized_value", "must-not-leak", "secret.example"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public response leaked %q: %s", secret, encoded)
		}
	}
}

func TestServiceListReturnsNonNilItemsForEmptyPage(t *testing.T) {
	repository := subscriptionRepositoryStub{
		count: func(context.Context, uint64, ListFilter) (int64, error) { return 0, nil },
		list:  func(context.Context, uint64, ListFilter, int, int) ([]QueryResult, error) { return nil, nil },
	}
	result, err := NewService(repository, sourceCatalogStub{get: validSourceGet}).List(
		context.Background(), 42, ListInput{Page: 1, PageSize: 20},
	)
	if err != nil {
		t.Fatalf("list empty subscriptions: %v", err)
	}
	if result.Items == nil || len(result.Items) != 0 {
		t.Fatalf("expected an empty non-nil items array, got %#v", result.Items)
	}
}

func TestServiceListRejectsInvalidPaginationAndSource(t *testing.T) {
	sourceID := uint64(0)
	tests := []ListInput{
		{Page: 0, PageSize: 20},
		{Page: 1, PageSize: 0},
		{Page: 1, PageSize: MaxPageSize + 1},
		{Page: 1, PageSize: 20, Filter: ListFilter{SourceID: &sourceID}},
	}
	for _, input := range tests {
		_, err := NewService(subscriptionRepositoryStub{}, sourceCatalogStub{get: validSourceGet}).List(
			context.Background(), 42, input,
		)
		if !errors.Is(err, ErrInvalidPagination) && !errors.Is(err, ErrInvalidFilter) {
			t.Fatalf("expected validation failure for %+v, got %v", input, err)
		}
	}
}

func TestServiceGetPreservesOwnershipLookupNotFound(t *testing.T) {
	var gotUserID, gotID uint64
	repository := subscriptionRepositoryStub{get: func(_ context.Context, userID, id uint64) (QueryResult, error) {
		gotUserID, gotID = userID, id
		return QueryResult{}, ErrNotFound
	}}
	_, err := NewService(repository, sourceCatalogStub{get: validSourceGet}).Get(context.Background(), 42, 99)
	if !errors.Is(err, ErrNotFound) || gotUserID != 42 || gotID != 99 {
		t.Fatalf("expected owned lookup not found, got user=%d id=%d err=%v", gotUserID, gotID, err)
	}
}

func TestServiceUpdateNormalizesFieldsAndRulesBeforeAtomicWrite(t *testing.T) {
	name := "  updated papers  "
	objective := "  better agents  "
	disabled := false
	inputRules := RulesInput{
		Categories: []string{"cs.ai"}, IncludeKeywords: []string{"Tool Use"},
	}
	existing := updateQueryResult()
	var gotPatch SubscriptionPatch
	var gotRules []Rule
	repository := subscriptionRepositoryStub{
		get: func(context.Context, uint64, uint64) (QueryResult, error) { return existing, nil },
		update: func(
			_ context.Context,
			userID uint64,
			id uint64,
			version uint32,
			patch SubscriptionPatch,
			rules *[]Rule,
		) (Subscription, []Rule, error) {
			if userID != 42 || id != 9 || version != 3 {
				t.Fatalf("unexpected update scope user=%d id=%d version=%d", userID, id, version)
			}
			gotPatch = patch
			gotRules = append([]Rule(nil), (*rules)...)
			updated := existing.Subscription
			updated.Name = *patch.Name
			updated.Objective = patch.Objective
			updated.Enabled = *patch.Enabled
			updated.Version++
			return updated, gotRules, nil
		},
	}

	updated, err := NewService(repository, sourceCatalogStub{get: validSourceGet}).Update(
		context.Background(),
		42,
		9,
		3,
		UpdateInput{
			Name: &name, ObjectiveSet: true, Objective: &objective, Enabled: &disabled, Rules: &inputRules,
		},
	)
	if err != nil {
		t.Fatalf("update subscription: %v", err)
	}
	if gotPatch.Name == nil || *gotPatch.Name != "updated papers" || gotPatch.Objective == nil ||
		*gotPatch.Objective != "better agents" || gotPatch.Enabled == nil || *gotPatch.Enabled {
		t.Fatalf("unexpected normalized patch %+v", gotPatch)
	}
	if len(gotRules) != 2 || gotRules[0].RuleValue != "cs.AI" ||
		gotRules[1].NormalizedValue != "tool use" {
		t.Fatalf("unexpected normalized replacement %+v", gotRules)
	}
	if updated.Version != 4 || updated.Name != "updated papers" || updated.Rules.Categories[0] != "cs.AI" {
		t.Fatalf("unexpected update response %+v", updated)
	}
}

func TestServiceUpdateSupportsSingleFieldsAndObjectiveClear(t *testing.T) {
	tests := []struct {
		name  string
		input UpdateInput
		check func(*testing.T, SubscriptionPatch)
	}{
		{
			name:  "name only",
			input: func() UpdateInput { value := " renamed "; return UpdateInput{Name: &value} }(),
			check: func(t *testing.T, patch SubscriptionPatch) {
				if patch.Name == nil || *patch.Name != "renamed" || patch.ObjectiveSet || patch.Enabled != nil {
					t.Fatalf("unexpected name patch %+v", patch)
				}
			},
		},
		{
			name:  "objective clear",
			input: UpdateInput{ObjectiveSet: true},
			check: func(t *testing.T, patch SubscriptionPatch) {
				if !patch.ObjectiveSet || patch.Objective != nil || patch.Name != nil || patch.Enabled != nil {
					t.Fatalf("unexpected objective patch %+v", patch)
				}
			},
		},
		{
			name:  "enabled only",
			input: func() UpdateInput { value := false; return UpdateInput{Enabled: &value} }(),
			check: func(t *testing.T, patch SubscriptionPatch) {
				if patch.Enabled == nil || *patch.Enabled || patch.Name != nil || patch.ObjectiveSet {
					t.Fatalf("unexpected enabled patch %+v", patch)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			existing := updateQueryResult()
			repository := subscriptionRepositoryStub{
				get: func(context.Context, uint64, uint64) (QueryResult, error) { return existing, nil },
				update: func(
					_ context.Context, _ uint64, _ uint64, _ uint32, patch SubscriptionPatch, _ *[]Rule,
				) (Subscription, []Rule, error) {
					test.check(t, patch)
					return existing.Subscription, existing.Rules, nil
				},
			}
			if _, err := NewService(repository, sourceCatalogStub{get: validSourceGet}).Update(
				context.Background(), 42, 9, 3, test.input,
			); err != nil {
				t.Fatalf("single-field update: %v", err)
			}
		})
	}
}

func TestServiceUpdateRejectsEmptyAndInvalidInputBeforeAtomicWrite(t *testing.T) {
	tooLong := strings.Repeat("界", maxNameRunes+1)
	invalidRules := RulesInput{Categories: []string{"not.allowed"}}
	for _, input := range []UpdateInput{
		{},
		{Name: &tooLong},
		{Rules: &invalidRules},
	} {
		calls := 0
		repository := subscriptionRepositoryStub{
			get: func(context.Context, uint64, uint64) (QueryResult, error) { return updateQueryResult(), nil },
			update: func(context.Context, uint64, uint64, uint32, SubscriptionPatch, *[]Rule) (Subscription, []Rule, error) {
				calls++
				return Subscription{}, nil, nil
			},
		}
		_, err := NewService(repository, sourceCatalogStub{get: validSourceGet}).Update(
			context.Background(), 42, 9, 3, input,
		)
		if err == nil || calls != 0 {
			t.Fatalf("invalid input %+v must fail before atomic write, err=%v calls=%d", input, err, calls)
		}
	}
}

func TestServiceUpdateAndDeletePreserveConflictAndOwnershipErrors(t *testing.T) {
	existing := updateQueryResult()
	name := "new"
	repository := subscriptionRepositoryStub{
		get: func(context.Context, uint64, uint64) (QueryResult, error) { return existing, nil },
		update: func(context.Context, uint64, uint64, uint32, SubscriptionPatch, *[]Rule) (Subscription, []Rule, error) {
			return Subscription{}, nil, ErrVersionConflict
		},
		delete: func(_ context.Context, userID, id uint64, version uint32) error {
			if userID != 42 || id != 9 || version != 3 {
				t.Fatalf("unexpected delete scope user=%d id=%d version=%d", userID, id, version)
			}
			return ErrNotFound
		},
	}
	service := NewService(repository, sourceCatalogStub{get: validSourceGet})
	if _, err := service.Update(context.Background(), 42, 9, 3, UpdateInput{Name: &name}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected update conflict, got %v", err)
	}
	if err := service.Delete(context.Background(), 42, 9, 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected delete not found, got %v", err)
	}
}

func updateQueryResult() QueryResult {
	createdAt := time.Date(2026, time.September, 4, 1, 2, 3, 0, time.UTC)
	return QueryResult{
		Subscription: Subscription{
			ID: 9, UserID: 42, SourceID: 1, Name: "papers", Enabled: true,
			Version: 3, CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		Source: source.Source{
			ID: 1, SourceKey: "arxiv", Kind: source.KindArXiv, Name: "arXiv", Enabled: true,
			ConfigJSON: json.RawMessage(`{"allowed_categories":["cs.AI","cs.CL"],"rule_types":["category","author","include_keyword","exclude_keyword"]}`),
		},
		Rules: []Rule{{
			ID: 1, SubscriptionID: 9, RuleType: source.RuleTypeCategory,
			RuleValue: "cs.AI", NormalizedValue: "cs.AI",
		}},
	}
}

type subscriptionRepositoryStub struct {
	create func(context.Context, *Subscription, []Rule) error
	count  func(context.Context, uint64, ListFilter) (int64, error)
	list   func(context.Context, uint64, ListFilter, int, int) ([]QueryResult, error)
	get    func(context.Context, uint64, uint64) (QueryResult, error)
	update func(context.Context, uint64, uint64, uint32, SubscriptionPatch, *[]Rule) (Subscription, []Rule, error)
	delete func(context.Context, uint64, uint64, uint32) error
}

func (stub subscriptionRepositoryStub) CreateAtomic(ctx context.Context, item *Subscription, rules []Rule) error {
	if stub.create == nil {
		return nil
	}
	return stub.create(ctx, item, rules)
}

func (stub subscriptionRepositoryStub) Count(ctx context.Context, userID uint64, filter ListFilter) (int64, error) {
	if stub.count == nil {
		return 0, nil
	}
	return stub.count(ctx, userID, filter)
}

func (stub subscriptionRepositoryStub) List(
	ctx context.Context,
	userID uint64,
	filter ListFilter,
	offset int,
	limit int,
) ([]QueryResult, error) {
	if stub.list == nil {
		return nil, nil
	}
	return stub.list(ctx, userID, filter, offset, limit)
}

func (stub subscriptionRepositoryStub) Get(ctx context.Context, userID, id uint64) (QueryResult, error) {
	if stub.get == nil {
		return QueryResult{}, nil
	}
	return stub.get(ctx, userID, id)
}

func (stub subscriptionRepositoryStub) UpdateAtomic(
	ctx context.Context,
	userID uint64,
	id uint64,
	version uint32,
	patch SubscriptionPatch,
	rules *[]Rule,
) (Subscription, []Rule, error) {
	if stub.update == nil {
		return Subscription{}, nil, nil
	}
	return stub.update(ctx, userID, id, version, patch, rules)
}

func (stub subscriptionRepositoryStub) SoftDelete(
	ctx context.Context,
	userID uint64,
	id uint64,
	version uint32,
) error {
	if stub.delete == nil {
		return nil
	}
	return stub.delete(ctx, userID, id, version)
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
