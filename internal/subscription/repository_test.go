package subscription

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"signalwatch/internal/paper"
	"signalwatch/internal/source"
)

func TestRepositoryCreateAtomicLocksChecksAndCommitsSubscriptionWithRules(t *testing.T) {
	manager := &fakeTransactionManager{store: &fakeTransactionStore{count: 19}}
	repository := &repository{transactions: manager}
	item := Subscription{UserID: 42, Enabled: true}
	rules := []Rule{{RuleType: "category", RuleValue: "cs.AI", NormalizedValue: "cs.AI"}}

	if err := repository.CreateAtomic(context.Background(), &item, rules, testBackfillWindow()); err != nil {
		t.Fatalf("create atomic: %v", err)
	}
	if !manager.committed || manager.persistedSubscriptions != 1 || manager.persistedRules != 0 {
		t.Fatalf("expected one flattened subscription write, got %+v", manager)
	}
	wantOrder := []string{"lock", "count", "subscription", "backfill", "matches"}
	for index, want := range wantOrder {
		if manager.store.order[index] != want {
			t.Fatalf("expected operation %d to be %q, got %v", index, want, manager.store.order)
		}
	}
	if item.ID != 77 || item.Category != "cs.AI" || string(item.KeywordsJSON) != "[]" {
		t.Fatalf("expected flattened category and keywords, got item=%+v", item)
	}
}

func TestRepositoryCreateAtomicRejectsUnsupportedFlatRules(t *testing.T) {
	manager := &fakeTransactionManager{store: &fakeTransactionStore{}}
	repository := &repository{transactions: manager}
	item := Subscription{UserID: 42, Enabled: true}

	err := repository.CreateAtomic(context.Background(), &item, []Rule{{
		RuleType: "author", RuleValue: "Jane Doe", NormalizedValue: "jane doe",
	}}, testBackfillWindow())
	if !errors.Is(err, ErrInvalidStoredRule) {
		t.Fatalf("expected unsupported flat rule failure, got %v", err)
	}
	if manager.committed || manager.persistedSubscriptions != 0 {
		t.Fatalf("invalid flat rules must not start a transaction, got %+v", manager)
	}
}

func TestRepositoryCreateAtomicRejectsInvalidEnabledBackfillWindow(t *testing.T) {
	manager := &fakeTransactionManager{store: &fakeTransactionStore{}}
	err := (&repository{transactions: manager}).CreateAtomic(
		context.Background(), &Subscription{UserID: 42, Enabled: true}, categoryRules("cs.AI"), BackfillWindow{},
	)
	if !errors.Is(err, ErrInvalidBackfillWindow) || manager.committed {
		t.Fatalf("invalid window must fail before transaction: error=%v committed=%v", err, manager.committed)
	}
}

func TestRepositoryCreateAtomicEnforcesEnabledLimitUnderUserLock(t *testing.T) {
	manager := &fakeTransactionManager{store: &fakeTransactionStore{count: maxEnabledSubscriptions}}
	err := (&repository{transactions: manager}).CreateAtomic(
		context.Background(), &Subscription{UserID: 42, Enabled: true}, categoryRules("cs.AI"), testBackfillWindow(),
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
		context.Background(), &Subscription{UserID: 42, Enabled: false}, categoryRules("cs.AI"), testBackfillWindow(),
	)
	if err != nil {
		t.Fatalf("create paused subscription: %v", err)
	}
	if manager.store.countCalls != 0 || !manager.committed {
		t.Fatalf("paused subscription should skip enabled count and commit, got %+v", manager)
	}
}

func TestRepositoryCreateAtomicBackfillsRecentMatchingPapers(t *testing.T) {
	window := testBackfillWindow()
	manager := &fakeTransactionManager{store: &fakeTransactionStore{
		backfillPapers: []paper.Paper{
			{ID: 10, CategoriesJSON: []byte(`["cs.AI"]`), Title: "Agent planning", Abstract: "tools"},
			{ID: 11, CategoriesJSON: []byte(`["cs.AI"]`), Title: "unrelated", Abstract: "paper"},
		},
	}}
	item := Subscription{UserID: 42, SourceID: 3, Enabled: true}
	err := (&repository{transactions: manager}).CreateAtomic(
		context.Background(), &item, categoryRules("cs.AI", "agent"), window,
	)
	if err != nil {
		t.Fatalf("create with backfill: %v", err)
	}
	if manager.store.backfillSourceID != 3 || !manager.store.backfillFrom.Equal(window.From) ||
		!manager.store.backfillTo.Equal(window.To) || len(manager.store.matches) != 1 {
		t.Fatalf("unexpected backfill query or matches: %+v", manager.store)
	}
	match := manager.store.matches[0]
	if match.SubscriptionID != 77 || match.PaperID != 10 || !match.MatchedAt.Equal(window.MatchedAt) ||
		string(match.MatchedKeywordsJSON) != `["agent"]` {
		t.Fatalf("unexpected durable backfill match: %+v", match)
	}
}

func TestRepositoryCreateAtomicRollsBackWhenBackfillWriteFails(t *testing.T) {
	writeError := errors.New("backfill write failed")
	manager := &fakeTransactionManager{store: &fakeTransactionStore{
		backfillPapers: []paper.Paper{{
			ID: 10, CategoriesJSON: []byte(`["cs.AI"]`), Title: "Agent planning",
		}},
		matchesError: writeError,
	}}
	err := (&repository{transactions: manager}).CreateAtomic(
		context.Background(), &Subscription{UserID: 42, SourceID: 3, Enabled: true},
		categoryRules("cs.AI"), testBackfillWindow(),
	)
	if !errors.Is(err, writeError) || manager.committed {
		t.Fatalf("backfill failure must roll back subscription: error=%v committed=%v", err, manager.committed)
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
				categoryRules("cs.AI"),
				testBackfillWindow(),
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

func TestRepositoryGetSQLCombinesIDOwnershipAndSoftDelete(t *testing.T) {
	database := newDryRunDatabase(t)
	sql := database.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var row subscriptionQueryRow
		return ownedSubscriptionQuery(selectSubscriptionsWithSource(tx), 42, 99).Take(&row)
	})
	normalized := strings.Join(strings.Fields(sql), " ")
	for _, clause := range []string{
		"subscriptions.id = 99",
		"subscriptions.user_id = 42",
		"subscriptions.deleted_at IS NULL",
		"JOIN sources ON sources.id = subscriptions.source_id",
	} {
		if !strings.Contains(normalized, clause) {
			t.Fatalf("expected SQL clause %q in %s", clause, normalized)
		}
	}
}

func TestRepositoryListSQLUsesSameFiltersAndStableOrder(t *testing.T) {
	database := newDryRunDatabase(t)
	enabled := false
	sourceID := uint64(7)
	filter := ListFilter{Enabled: &enabled, SourceID: &sourceID}

	countSQL := database.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var count int64
		return applyListFilter(tx.Model(&Subscription{}), 42, filter).Count(&count)
	})
	listSQL := database.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var rows []subscriptionQueryRow
		return applyListFilter(selectSubscriptionsWithSource(tx), 42, filter).
			Order("subscriptions.created_at DESC, subscriptions.id DESC").
			Offset(20).
			Limit(20).
			Scan(&rows)
	})

	for _, clause := range []string{
		"subscriptions.user_id = 42",
		"subscriptions.deleted_at IS NULL",
		"subscriptions.enabled = false",
		"subscriptions.source_id = 7",
	} {
		if !strings.Contains(strings.Join(strings.Fields(countSQL), " "), clause) {
			t.Fatalf("count SQL is missing %q: %s", clause, countSQL)
		}
		if !strings.Contains(strings.Join(strings.Fields(listSQL), " "), clause) {
			t.Fatalf("list SQL is missing %q: %s", clause, listSQL)
		}
	}
	if !strings.Contains(listSQL, "ORDER BY subscriptions.created_at DESC, subscriptions.id DESC") ||
		!strings.Contains(listSQL, "LIMIT 20 OFFSET 20") {
		t.Fatalf("list SQL lacks stable pagination: %s", listSQL)
	}
}

func TestRepositoryAttachRulesUsesFlattenedSubscriptionRowsWithoutQueries(t *testing.T) {
	empty, err := (&repository{}).attachRules(context.Background(), nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty page should skip database and return empty slice, got %#v err=%v", empty, err)
	}

	database := newDryRunDatabase(t)
	queries := 0
	if err := database.Callback().Query().Before("gorm:query").Register("count_rule_queries", func(*gorm.DB) {
		queries++
	}); err != nil {
		t.Fatalf("register query counter: %v", err)
	}
	rows := make([]subscriptionQueryRow, 20)
	for index := range rows {
		rows[index].SubscriptionID = uint64(index + 1)
		rows[index].SubscriptionCategory = "cs.AI"
		rows[index].SubscriptionKeywords = []byte(`["agent"]`)
	}
	results, err := (&repository{db: database}).attachRules(context.Background(), rows)
	if err != nil {
		t.Fatalf("attach rules: %v", err)
	}
	if queries != 0 || len(results) != 20 || len(results[0].Rules) != 2 {
		t.Fatalf("expected zero rule-table queries for flattened rows, got queries=%d results=%d", queries, len(results))
	}
}

func TestRepositoryUpdateAtomicChangesFieldsAndReplacesRulesInOneTransaction(t *testing.T) {
	oldObjective := "old objective"
	newName := "new name"
	enabled := true
	manager := &fakeTransactionManager{store: &fakeTransactionStore{
		lockedSubscription: Subscription{
			ID: 9, UserID: 42, Name: "old name", Objective: &oldObjective,
			Category: "cs.AI", KeywordsJSON: []byte(`[]`), Enabled: false, Version: 3,
		},
		count: 19,
	}}
	replacement := []Rule{{RuleType: "category", RuleValue: "cs.CL", NormalizedValue: "cs.CL"}}

	updated, rules, err := (&repository{transactions: manager}).UpdateAtomic(
		context.Background(),
		42,
		9,
		3,
		SubscriptionPatch{Name: &newName, ObjectiveSet: true, Objective: nil, Enabled: &enabled},
		&replacement,
	)
	if err != nil {
		t.Fatalf("update atomic: %v", err)
	}
	if !manager.committed || updated.Name != "new name" || updated.Objective != nil ||
		!updated.Enabled || updated.Version != 4 {
		t.Fatalf("unexpected committed update %+v manager=%+v", updated, manager)
	}
	if len(rules) != 1 || rules[0].SubscriptionID != 9 || rules[0].RuleValue != "cs.CL" {
		t.Fatalf("unexpected replacement rules %+v", rules)
	}
	wantOrder := []string{"subscription_lock", "lock", "count", "update"}
	if strings.Join(manager.store.order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("unexpected update order %v", manager.store.order)
	}
}

func TestRepositoryUpdateAtomicIdenticalContentDoesNotIncrementOrReplace(t *testing.T) {
	name := "same"
	manager := &fakeTransactionManager{store: &fakeTransactionStore{
		lockedSubscription: Subscription{
			ID: 9, UserID: 42, Name: name, Category: "cs.AI",
			KeywordsJSON: []byte(`[]`), Enabled: true, Version: 3,
		},
	}}
	replacement := []Rule{{RuleType: "category", RuleValue: "cs.AI", NormalizedValue: "cs.AI"}}

	updated, _, err := (&repository{transactions: manager}).UpdateAtomic(
		context.Background(), 42, 9, 3, SubscriptionPatch{Name: &name}, &replacement,
	)
	if err != nil {
		t.Fatalf("idempotent update: %v", err)
	}
	if updated.Version != 3 || manager.store.updateCalls != 0 {
		t.Fatalf("identical content must not write or increment: updated=%+v store=%+v", updated, manager.store)
	}
}

func TestRepositoryUpdateAtomicRollsBackFlattenedReplacementOnUpdateFailure(t *testing.T) {
	updateErr := errors.New("subscription update failed")
	newName := "new"
	manager := &fakeTransactionManager{store: &fakeTransactionStore{
		lockedSubscription: Subscription{
			ID: 9, UserID: 42, Name: "old", Category: "cs.AI",
			KeywordsJSON: []byte(`[]`), Version: 3,
		},
		updateError: updateErr,
	}}
	replacement := []Rule{{RuleType: "category", RuleValue: "cs.CL", NormalizedValue: "cs.CL"}}

	_, _, err := (&repository{transactions: manager}).UpdateAtomic(
		context.Background(), 42, 9, 3, SubscriptionPatch{Name: &newName}, &replacement,
	)
	if !errors.Is(err, updateErr) || manager.committed {
		t.Fatalf("expected rolled-back update error, got err=%v committed=%v", err, manager.committed)
	}
	if manager.store.updateCalls != 1 {
		t.Fatalf("expected one flattened update attempt inside transaction, got %+v", manager.store)
	}
}

func TestRepositoryUpdateAtomicDistinguishesVersionConflictWithoutLeakingOwnership(t *testing.T) {
	for _, test := range []struct {
		name   string
		exists bool
		want   error
	}{
		{name: "owned stale version", exists: true, want: ErrVersionConflict},
		{name: "missing foreign or deleted", exists: false, want: ErrNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &fakeTransactionManager{store: &fakeTransactionStore{
				lockSubscriptionError:    ErrNotFound,
				activeSubscriptionExists: test.exists,
			}}
			_, _, err := (&repository{transactions: manager}).UpdateAtomic(
				context.Background(), 42, 9, 2, SubscriptionPatch{}, nil,
			)
			if !errors.Is(err, test.want) || manager.committed {
				t.Fatalf("expected %v without commit, got %v", test.want, err)
			}
			if strings.Join(manager.store.order, ",") != "subscription_lock,exists" {
				t.Fatalf("unexpected protected conflict check order %v", manager.store.order)
			}
		})
	}
}

func TestRepositoryUpdateAtomicLimitLeavesPausedSubscriptionUntouched(t *testing.T) {
	enabled := true
	manager := &fakeTransactionManager{store: &fakeTransactionStore{
		lockedSubscription: Subscription{
			ID: 9, UserID: 42, Name: "paused", Category: "cs.AI",
			KeywordsJSON: []byte(`[]`), Enabled: false, Version: 3,
		},
		count: maxEnabledSubscriptions,
	}}
	_, _, err := (&repository{transactions: manager}).UpdateAtomic(
		context.Background(), 42, 9, 3, SubscriptionPatch{Enabled: &enabled}, nil,
	)
	if !errors.Is(err, ErrLimitReached) || manager.committed || manager.store.updateCalls != 0 {
		t.Fatalf("quota failure must not update or commit, got err=%v manager=%+v", err, manager)
	}
}

func TestRepositoryUpdateAtomicConcurrentEnablesStopAtLimit(t *testing.T) {
	manager := &serializedEnableManager{enabled: maxEnabledSubscriptions - 1}
	repository := &repository{transactions: manager}
	enabled := true

	const attempts = 8
	var wait sync.WaitGroup
	wait.Add(attempts)
	errorsFound := make(chan error, attempts)
	for index := range attempts {
		go func(id uint64) {
			defer wait.Done()
			_, _, err := repository.UpdateAtomic(
				context.Background(), 42, id, 1, SubscriptionPatch{Enabled: &enabled}, nil,
			)
			errorsFound <- err
		}(uint64(index + 1))
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
			t.Fatalf("unexpected enable error: %v", err)
		}
	}
	if succeeded != 1 || limited != attempts-1 || manager.enabled != maxEnabledSubscriptions {
		t.Fatalf("expected one enable success at quota boundary, successes=%d limited=%d enabled=%d", succeeded, limited, manager.enabled)
	}
}

func TestRepositoryUpdateLockSQLIncludesVersionOwnershipSoftDeleteAndForUpdate(t *testing.T) {
	database := newDryRunDatabase(t)
	sql := database.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var item Subscription
		return lockOwnedSubscriptionQuery(tx, 42, 9, 3, &item)
	})
	normalized := strings.Join(strings.Fields(sql), " ")
	for _, clause := range []string{
		"id = 9 AND user_id = 42 AND version = 3 AND deleted_at IS NULL",
		"FOR UPDATE",
	} {
		if !strings.Contains(normalized, clause) {
			t.Fatalf("expected update-lock SQL clause %q in %s", clause, normalized)
		}
	}
}

func TestRepositorySoftDeleteClassifiesSuccessConflictAndNotFound(t *testing.T) {
	for _, test := range []struct {
		name    string
		deleted bool
		exists  bool
		want    error
	}{
		{name: "deleted", deleted: true},
		{name: "stale", exists: true, want: ErrVersionConflict},
		{name: "missing foreign or repeated", want: ErrNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeDeletionStore{deleted: test.deleted, exists: test.exists}
			err := (&repository{deletions: store}).SoftDelete(context.Background(), 42, 9, 3)
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
			if store.gotUserID != 42 || store.gotID != 9 || store.gotVersion != 3 {
				t.Fatalf("soft delete lost ownership/version scope: %+v", store)
			}
		})
	}
}

func TestRepositorySoftDeleteSQLIsVersionedOwnedAndDoesNotDeleteRules(t *testing.T) {
	database := newDryRunDatabase(t)
	sql := database.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return softDeleteQuery(tx, 42, 9, 3)
	})
	normalized := strings.Join(strings.Fields(sql), " ")
	for _, clause := range []string{
		"UPDATE `subscriptions`",
		"`deleted_at`=UTC_TIMESTAMP(6)",
		"`updated_at`=UTC_TIMESTAMP(6)",
		"`version`=version + 1",
		"id = 9 AND user_id = 42 AND version = 3 AND deleted_at IS NULL",
	} {
		if !strings.Contains(normalized, clause) {
			t.Fatalf("expected soft-delete SQL clause %q in %s", clause, normalized)
		}
	}
	if strings.Contains(normalized, "subscription_rules") {
		t.Fatalf("soft delete must not touch rules: %s", normalized)
	}
}

func categoryRules(category string, keywords ...string) []Rule {
	rules := []Rule{{
		RuleType: source.RuleTypeCategory, RuleValue: category, NormalizedValue: category,
	}}
	for _, keyword := range keywords {
		rules = append(rules, Rule{
			RuleType:  source.RuleTypeIncludeKeyword,
			RuleValue: keyword, NormalizedValue: strings.ToLower(keyword),
		})
	}
	return rules
}

func testBackfillWindow() BackfillWindow {
	now := time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC)
	return BackfillWindow{From: now.Add(-7 * 24 * time.Hour), To: now, MatchedAt: now}
}

type fakeDeletionStore struct {
	deleted    bool
	exists     bool
	gotUserID  uint64
	gotID      uint64
	gotVersion uint32
}

type serializedEnableManager struct {
	mu      sync.Mutex
	enabled int64
}

func (manager *serializedEnableManager) WithinTransaction(
	_ context.Context,
	work func(transactionStore) error,
) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	store := &serializedEnableStore{manager: manager}
	if err := work(store); err != nil {
		return err
	}
	if store.enabledSubscription {
		manager.enabled++
	}
	return nil
}

type serializedEnableStore struct {
	manager             *serializedEnableManager
	enabledSubscription bool
}

func (*serializedEnableStore) LockActiveUser(context.Context, uint64) error { return nil }

func (store *serializedEnableStore) CountEnabled(context.Context, uint64) (int64, error) {
	return store.manager.enabled, nil
}

func (*serializedEnableStore) CreateSubscription(context.Context, *Subscription) error { return nil }

func (*serializedEnableStore) ListBackfillPapers(context.Context, uint64, time.Time, time.Time) ([]paper.Paper, error) {
	return nil, nil
}

func (*serializedEnableStore) InsertBackfillMatches(context.Context, []paper.SubscriptionPaper) error {
	return nil
}

func (*serializedEnableStore) CreateRules(context.Context, []Rule) error { return nil }

func (*serializedEnableStore) LockOwnedSubscription(
	_ context.Context,
	userID uint64,
	id uint64,
	version uint32,
) (Subscription, error) {
	return Subscription{
		ID: id, UserID: userID, Category: "cs.AI", KeywordsJSON: []byte(`[]`),
		Enabled: false, Version: version,
	}, nil
}

func (*serializedEnableStore) OwnedActiveSubscriptionExists(context.Context, uint64, uint64) (bool, error) {
	return true, nil
}

func (*serializedEnableStore) ListRules(context.Context, uint64) ([]Rule, error) { return nil, nil }

func (store *serializedEnableStore) UpdateSubscription(
	_ context.Context,
	item *Subscription,
	patch SubscriptionPatch,
) error {
	item.Enabled = *patch.Enabled
	item.Version++
	store.enabledSubscription = item.Enabled
	return nil
}

func (*serializedEnableStore) DeleteRules(context.Context, uint64) error { return nil }

func (store *fakeDeletionStore) SoftDelete(
	_ context.Context,
	userID uint64,
	id uint64,
	version uint32,
) (bool, error) {
	store.gotUserID, store.gotID, store.gotVersion = userID, id, version
	return store.deleted, nil
}

func (store *fakeDeletionStore) OwnedActiveSubscriptionExists(
	_ context.Context,
	userID uint64,
	id uint64,
) (bool, error) {
	store.gotUserID, store.gotID = userID, id
	return store.exists, nil
}

func newDryRunDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "signalwatch:signalwatch@tcp(127.0.0.1:3306)/signalwatch?parseTime=true",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	return database
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
	count                    int64
	ruleError                error
	deleteRulesError         error
	updateError              error
	lockSubscriptionError    error
	lockedSubscription       Subscription
	activeSubscriptionExists bool
	existingRules            []Rule
	order                    []string
	countCalls               int
	createSubscriptionCalls  int
	createdRules             int
	updateCalls              int
	deleteRulesCalls         int
	backfillPapers           []paper.Paper
	backfillSourceID         uint64
	backfillFrom             time.Time
	backfillTo               time.Time
	matches                  []paper.SubscriptionPaper
	matchesError             error
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

func (store *fakeTransactionStore) ListBackfillPapers(_ context.Context, sourceID uint64, from time.Time, to time.Time) ([]paper.Paper, error) {
	store.order = append(store.order, "backfill")
	store.backfillSourceID, store.backfillFrom, store.backfillTo = sourceID, from, to
	return store.backfillPapers, nil
}

func (store *fakeTransactionStore) InsertBackfillMatches(_ context.Context, matches []paper.SubscriptionPaper) error {
	store.order = append(store.order, "matches")
	store.matches = append([]paper.SubscriptionPaper(nil), matches...)
	return store.matchesError
}

func (store *fakeTransactionStore) CreateRules(_ context.Context, rules []Rule) error {
	store.order = append(store.order, "rules")
	store.createdRules += len(rules)
	return store.ruleError
}

func (store *fakeTransactionStore) LockOwnedSubscription(context.Context, uint64, uint64, uint32) (Subscription, error) {
	store.order = append(store.order, "subscription_lock")
	if store.lockSubscriptionError != nil {
		return Subscription{}, store.lockSubscriptionError
	}
	return store.lockedSubscription, nil
}

func (store *fakeTransactionStore) OwnedActiveSubscriptionExists(context.Context, uint64, uint64) (bool, error) {
	store.order = append(store.order, "exists")
	return store.activeSubscriptionExists, nil
}

func (store *fakeTransactionStore) ListRules(context.Context, uint64) ([]Rule, error) {
	store.order = append(store.order, "list_rules")
	return append([]Rule(nil), store.existingRules...), nil
}

func (store *fakeTransactionStore) UpdateSubscription(_ context.Context, item *Subscription, patch SubscriptionPatch) error {
	store.order = append(store.order, "update")
	store.updateCalls++
	if store.updateError != nil {
		return store.updateError
	}
	if patch.Name != nil {
		item.Name = *patch.Name
	}
	if patch.ObjectiveSet {
		item.Objective = patch.Objective
	}
	if patch.Enabled != nil {
		item.Enabled = *patch.Enabled
	}
	if patch.Category != nil {
		item.Category = *patch.Category
	}
	if patch.KeywordsJSON != nil {
		item.KeywordsJSON = append(item.KeywordsJSON[:0], (*patch.KeywordsJSON)...)
	}
	item.Version++
	return nil
}

func (store *fakeTransactionStore) DeleteRules(context.Context, uint64) error {
	store.order = append(store.order, "delete_rules")
	store.deleteRulesCalls++
	return store.deleteRulesError
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

func (*serializedQuotaStore) ListBackfillPapers(context.Context, uint64, time.Time, time.Time) ([]paper.Paper, error) {
	return nil, nil
}

func (*serializedQuotaStore) InsertBackfillMatches(context.Context, []paper.SubscriptionPaper) error {
	return nil
}

func (*serializedQuotaStore) CreateRules(context.Context, []Rule) error { return nil }

func (*serializedQuotaStore) LockOwnedSubscription(context.Context, uint64, uint64, uint32) (Subscription, error) {
	return Subscription{}, ErrNotFound
}

func (*serializedQuotaStore) OwnedActiveSubscriptionExists(context.Context, uint64, uint64) (bool, error) {
	return false, nil
}

func (*serializedQuotaStore) ListRules(context.Context, uint64) ([]Rule, error) { return nil, nil }

func (*serializedQuotaStore) UpdateSubscription(context.Context, *Subscription, SubscriptionPatch) error {
	return nil
}

func (*serializedQuotaStore) DeleteRules(context.Context, uint64) error { return nil }
