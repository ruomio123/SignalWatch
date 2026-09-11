package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"signalwatch/internal/source"
	"testing"
	"time"
)

type catalogStub struct{}

func (catalogStub) Get(context.Context, uint64) (source.PublicSource, error) {
	return arXivCatalog(), nil
}

type commandStore struct {
	count      int64
	item       Subscription
	enqueued   bool
	enqueueErr error
	events     []string
}

func (s *commandStore) Transact(ctx context.Context, f func(Tx) error) error {
	before := s.item
	if err := f(s); err != nil {
		s.item = before
		return err
	}
	return nil
}
func (s *commandStore) LockUser(context.Context, uint64) (uint16, error) {
	s.events = append(s.events, "user")
	return 12, nil
}
func (s *commandStore) CountEnabled(context.Context, uint64) (int64, error) { return s.count, nil }
func (s *commandStore) Insert(_ context.Context, v *Subscription) error {
	v.ID = 7
	s.item = *v
	return nil
}
func (s *commandStore) Enqueue(context.Context, *Subscription, BackfillWindow) error {
	s.enqueued = true
	return s.enqueueErr
}
func (s *commandStore) LockSubscription(context.Context, uint64, uint64) (Subscription, error) {
	s.events = append(s.events, "subscription")
	return s.item, nil
}
func (s *commandStore) Update(_ context.Context, v *Subscription, p SubscriptionPatch) error {
	if p.Enabled != nil {
		v.Enabled = *p.Enabled
	}
	v.Version++
	s.item = *v
	return nil
}
func (s *commandStore) Count(context.Context, uint64, ListFilter) (int64, error) { return 1, nil }
func (s *commandStore) List(context.Context, uint64, ListFilter, int, int) ([]QueryResult, error) {
	return nil, nil
}
func (s *commandStore) Get(context.Context, uint64, uint64) (QueryResult, error) {
	raw, _ := json.Marshal(map[string]any{"allowed_categories": []string{"cs.AI"}})
	return QueryResult{Subscription: s.item, Source: source.Source{ID: 1, Kind: "arxiv", ConfigJSON: raw}}, nil
}
func (s *commandStore) SoftDelete(context.Context, uint64, uint64, uint32) error { return nil }
func TestCreateKeepsSubscriptionAndTaskAtomic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		store := &commandStore{}
		if fail {
			store.enqueueErr = errors.New("unavailable")
		}
		service := NewService(store, catalogStub{})
		created, err := service.Create(t.Context(), 1, CreateInput{SourceID: 1, Name: " Agents ", Rules: RulesInput{Category: "cs.AI"}})
		if fail {
			if err == nil || store.item.ID != 0 {
				t.Fatal("task failure did not roll back subscription")
			}
			continue
		}
		if err != nil || created.MaxItemsPerDigest != 12 || created.Name != "Agents" || created.Backfill.State != "pending" || !store.enqueued {
			t.Fatalf("%+v %v", created, err)
		}
	}
}
func TestQuotaAndVersionAreCheckedInsideTransaction(t *testing.T) {
	store := &commandStore{count: 20, item: Subscription{ID: 7, UserID: 1, Version: 3, Category: "cs.AI", KeywordsJSON: []byte(`[]`)}}
	service := NewService(store, catalogStub{})
	_, err := service.Create(t.Context(), 1, CreateInput{SourceID: 1, Name: "Agents", Rules: RulesInput{Category: "cs.AI"}})
	if !errors.Is(err, ErrLimitReached) {
		t.Fatal(err)
	}
	enabled := true
	_, err = service.Update(t.Context(), 1, 7, 2, UpdateInput{Enabled: &enabled})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatal(err)
	}
	_, err = service.Update(t.Context(), 1, 7, 3, UpdateInput{Enabled: &enabled})
	if !errors.Is(err, ErrLimitReached) {
		t.Fatal(err)
	}
	if store.item.Enabled || store.item.Version != 3 {
		t.Fatal("failed mutation changed state")
	}
}
func TestPublicMappingKeepsAIAndBackfillFields(t *testing.T) {
	store := &commandStore{item: Subscription{ID: 7, Category: "cs.AI", KeywordsJSON: []byte(`["agent"]`), DigestAIEnabled: true, DigestAILanguage: "en", Backfill: BackfillStatus{State: "processing", Processed: 200, Matched: 12}, CreatedAt: time.Now()}}
	row, _ := store.Get(t.Context(), 1, 7)
	value, err := publicSubscription(row)
	if err != nil || !value.DigestAIEnabled || value.DigestAILanguage != "en" || value.Backfill.Processed != 200 || value.Rules.Keywords[0] != "agent" {
		t.Fatalf("%+v %v", value, err)
	}
}
