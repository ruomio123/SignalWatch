package subscription

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestRepositoryCreateAtomicLocksChecksAndCommitsSubscriptionWithRules(t *testing.T) {
	manager := &fakeTransactionManager{store: &fakeTransactionStore{count: 19}}
	repository := &repository{transactions: manager}
	item := Subscription{UserID: 42, Enabled: true}
	rules := []Rule{{RuleType: "category", RuleValue: "cs.AI", NormalizedValue: "cs.AI"}}

	if err := repository.CreateAtomic(context.Background(), &item, rules); err != nil {
		t.Fatalf("create atomic: %v", err)
	}
	if !manager.committed || manager.persistedSubscriptions != 1 || manager.persistedRules != 1 {
		t.Fatalf("expected committed subscription and rule, got %+v", manager)
	}
	wantOrder := []string{"lock", "count", "subscription", "rules"}
	for index, want := range wantOrder {
		if manager.store.order[index] != want {
			t.Fatalf("expected operation %d to be %q, got %v", index, want, manager.store.order)
		}
	}
	if item.ID != 77 || rules[0].SubscriptionID != 77 {
		t.Fatalf("expected generated subscription ID on rule, got item=%+v rules=%+v", item, rules)
	}
}

func TestRepositoryCreateAtomicRollsBackWhenRuleInsertFails(t *testing.T) {
	ruleError := errors.New("rule insert failed")
	manager := &fakeTransactionManager{store: &fakeTransactionStore{ruleError: ruleError}}
	repository := &repository{transactions: manager}
	item := Subscription{UserID: 42, Enabled: true}

	err := repository.CreateAtomic(context.Background(), &item, []Rule{{RuleType: "category"}})
	if !errors.Is(err, ruleError) {
		t.Fatalf("expected rule insert failure, got %v", err)
	}
	if manager.committed || manager.persistedSubscriptions != 0 || manager.persistedRules != 0 {
		t.Fatalf("failed rule insert must leave no committed subscription, got %+v", manager)
	}
}

func TestRepositoryCreateAtomicEnforcesEnabledLimitUnderUserLock(t *testing.T) {
	manager := &fakeTransactionManager{store: &fakeTransactionStore{count: maxEnabledSubscriptions}}
	err := (&repository{transactions: manager}).CreateAtomic(
		context.Background(), &Subscription{UserID: 42, Enabled: true}, nil,
	)
	if !errors.Is(err, ErrLimitReached) {
		t.Fatalf("expected subscription limit, got %v", err)
	}
	if manager.committed || manager.store.createSubscriptionCalls != 0 {
		t.Fatal("limit failure must not create or commit a subscription")
	}
	if len(manager.store.order) != 2 || manager.store.order[0] != "lock" || manager.store.order[1] != "count" {
		t.Fatalf("expected lock before count, got %v", manager.store.order)
	}
}

func TestRepositoryCreateAtomicPausedSubscriptionDoesNotConsumeEnabledQuota(t *testing.T) {
	manager := &fakeTransactionManager{store: &fakeTransactionStore{count: maxEnabledSubscriptions}}
	err := (&repository{transactions: manager}).CreateAtomic(
		context.Background(), &Subscription{UserID: 42, Enabled: false}, nil,
	)
	if err != nil {
		t.Fatalf("create paused subscription: %v", err)
	}
	if manager.store.countCalls != 0 || !manager.committed {
		t.Fatalf("paused subscription should skip enabled count and commit, got %+v", manager)
	}
}

func TestRepositoryCreateAtomicConcurrentEnabledCreatesStopAtLimit(t *testing.T) {
	manager := &serializedQuotaManager{enabled: maxEnabledSubscriptions - 1}
	repository := &repository{transactions: manager}

	const attempts = 8
	var wait sync.WaitGroup
	wait.Add(attempts)
	errorsFound := make(chan error, attempts)
	for range attempts {
		go func() {
			defer wait.Done()
			errorsFound <- repository.CreateAtomic(
				context.Background(),
				&Subscription{UserID: 42, Enabled: true},
				nil,
			)
		}()
	}
	wait.Wait()
	close(errorsFound)

	succeeded, limited := 0, 0
	for err := range errorsFound {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrLimitReached):
			limited++
		default:
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	if succeeded != 1 || limited != attempts-1 || manager.enabled != maxEnabledSubscriptions {
		t.Fatalf("expected one success and a final count of 20, got successes=%d limited=%d count=%d",
			succeeded, limited, manager.enabled)
	}
}

type fakeTransactionManager struct {
	store                  *fakeTransactionStore
	committed              bool
	persistedSubscriptions int
	persistedRules         int
}

func (manager *fakeTransactionManager) WithinTransaction(
	ctx context.Context,
	work func(transactionStore) error,
) error {
	err := work(manager.store)
	if err != nil {
		return err
	}
	manager.committed = true
	manager.persistedSubscriptions = manager.store.createSubscriptionCalls
	manager.persistedRules = manager.store.createdRules
	return nil
}

type fakeTransactionStore struct {
	count                   int64
	ruleError               error
	order                   []string
	countCalls              int
	createSubscriptionCalls int
	createdRules            int
}

func (store *fakeTransactionStore) LockActiveUser(context.Context, uint64) error {
	store.order = append(store.order, "lock")
	return nil
}

func (store *fakeTransactionStore) CountEnabled(context.Context, uint64) (int64, error) {
	store.order = append(store.order, "count")
	store.countCalls++
	return store.count, nil
}

func (store *fakeTransactionStore) CreateSubscription(_ context.Context, item *Subscription) error {
	store.order = append(store.order, "subscription")
	store.createSubscriptionCalls++
	item.ID = 77
	return nil
}

func (store *fakeTransactionStore) CreateRules(_ context.Context, rules []Rule) error {
	store.order = append(store.order, "rules")
	store.createdRules += len(rules)
	return store.ruleError
}

// serializedQuotaManager models the user-row lock being held until commit.
// The operation-order test above separately verifies that the repository asks
// for that lock before reading the count.
type serializedQuotaManager struct {
	mu      sync.Mutex
	enabled int64
}

func (manager *serializedQuotaManager) WithinTransaction(
	_ context.Context,
	work func(transactionStore) error,
) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	store := &serializedQuotaStore{manager: manager}
	if err := work(store); err != nil {
		return err
	}
	if store.createdEnabled {
		manager.enabled++
	}
	return nil
}

type serializedQuotaStore struct {
	manager        *serializedQuotaManager
	createdEnabled bool
}

func (*serializedQuotaStore) LockActiveUser(context.Context, uint64) error { return nil }

func (store *serializedQuotaStore) CountEnabled(context.Context, uint64) (int64, error) {
	return store.manager.enabled, nil
}

func (store *serializedQuotaStore) CreateSubscription(_ context.Context, item *Subscription) error {
	store.createdEnabled = item.Enabled
	return nil
}

func (*serializedQuotaStore) CreateRules(context.Context, []Rule) error { return nil }
