package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"net/http"
	"os"
	"signalwatch/internal/backfill"
	"signalwatch/internal/collector"
	"signalwatch/internal/digest"
	"signalwatch/internal/paper"
	"signalwatch/internal/platform/fence"
	"signalwatch/internal/source"
	"signalwatch/internal/subscription"
	"signalwatch/internal/user"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recoveryFixture struct {
	db     *gorm.DB
	u      user.User
	sub    subscription.Subscription
	src    source.Source
	papers []paper.Paper
	now    time.Time
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	db, sqlDB, _ := openM1TestDatabase(t)
	t.Cleanup(func() { sqlDB.Close() })
	now := time.Now().UTC().Truncate(time.Microsecond)
	tag := fmt.Sprint(now.UnixNano())
	f := &recoveryFixture{db: db, now: now}
	f.src = source.Source{SourceKey: "recovery-" + tag, Kind: source.KindArXiv, Name: "Recovery", Enabled: true, ConfigJSON: json.RawMessage(`{"allowed_categories":["cs.AI"],"rule_types":["category","include_keyword"]}`), CreatedAt: now, UpdatedAt: now}
	checkRecovery(t, db.Create(&f.src).Error)
	f.u = user.User{Email: "recovery-" + tag + "@example.test", PasswordHash: "unused", Timezone: "UTC", DigestTime: "00:00:00", MaxItemsPerDigest: 1, Status: user.StatusActive, CreatedAt: now, UpdatedAt: now}
	checkRecovery(t, db.Create(&f.u).Error)
	f.sub = subscription.Subscription{UserID: f.u.ID, SourceID: f.src.ID, Name: "Recovery", Category: "cs.AI", KeywordsJSON: json.RawMessage(`[]`), Enabled: true, Version: 1, MaxItemsPerDigest: 1, DigestAILanguage: "en", CreatedAt: now, UpdatedAt: now}
	checkRecovery(t, db.Create(&f.sub).Error)
	t.Cleanup(func() {
		checkRecovery(t, db.Where("user_id=?", f.u.ID).Delete(&subscription.Subscription{}).Error)
		checkRecovery(t, db.Where("source_id=?", f.src.ID).Delete(&paper.Paper{}).Error)
		checkRecovery(t, db.Delete(&f.u).Error)
		checkRecovery(t, db.Delete(&f.src).Error)
	})
	records := []paper.Record{}
	for i := range 3 {
		records = append(records, paper.Record{ArXivID: fmt.Sprintf("recovery-%s-%d", tag, i), Title: "Agent", Abstract: "Planning", Authors: []string{"Ada"}, Categories: []string{"cs.AI"}, PublishedAt: now.Add(-time.Hour), ArXivUpdatedAt: now, ArXivURL: "https://arxiv.org/abs/test", PDFURL: "https://arxiv.org/pdf/test"})
	}
	result, err := paper.NewRepository(db).Upsert(t.Context(), f.src.ID, records, now.Add(-time.Minute))
	checkRecovery(t, err)
	f.papers = result.Papers
	for _, p := range f.papers {
		checkRecovery(t, db.Create(&paper.SubscriptionPaper{SubscriptionID: f.sub.ID, PaperID: p.ID, MatchedKeywordsJSON: json.RawMessage(`[]`), MatchedAt: now}).Error)
	}
	return f
}
func checkRecovery(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type recordingSender struct{ messages []digest.Message }

func (s *recordingSender) Send(_ context.Context, m digest.Message) error {
	s.messages = append(s.messages, m)
	return nil
}

type failFinishOnce struct {
	digest.DeliveryStore
	fail bool
}

func (s *failFinishOnce) Finish(ctx context.Context, d digest.Delivery, snap digest.Snapshot, now time.Time) (int64, error) {
	if s.fail {
		s.fail = false
		return 0, errors.New("injected database acknowledgement failure")
	}
	return s.DeliveryStore.Finish(ctx, d, snap, now)
}
func TestDigestDailyIdentityFrozenRetryAndExhaustion(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := t.Context()
	store := digest.NewMySQLDeliveryStore(f.db)
	job := digest.Job{UserID: f.u.ID, SubscriptionID: f.sub.ID, LocalDate: f.now.Format("2006-01-02")}
	var wins atomic.Int32
	var claimed digest.Delivery
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			d, ok, err := store.Claim(ctx, job)
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
				mu.Lock()
				claimed = d
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("claims=%d", wins.Load())
	}
	checkRecovery(t, store.Fail(ctx, claimed))
	checkRecovery(t, f.db.Exec("UPDATE digest_deliveries SET next_retry_at=UTC_TIMESTAMP(6) WHERE id=?", claimed.ID).Error)
	sender := &recordingSender{}
	fault := &failFinishOnce{store, true}
	processor, err := digest.NewProcessor(digest.NewRepository(f.db), fault, sender, func() time.Time { return f.now })
	checkRecovery(t, err)
	if _, err = processor.Process(ctx, job); err == nil {
		t.Fatal("expected acknowledgement failure")
	}
	checkRecovery(t, f.db.Exec("UPDATE digest_deliveries SET next_retry_at=UTC_TIMESTAMP(6) WHERE id=?", claimed.ID).Error)
	_, err = processor.Process(ctx, job)
	checkRecovery(t, err)
	if len(sender.messages) != 2 || sender.messages[0] != sender.messages[1] || sender.messages[0].ID == "" {
		t.Fatal("retry changed frozen message or stable Message-ID")
	}
	// A Redis cache loss cannot remove the database's completion fact.
	address := os.Getenv("TEST_REDIS_ADDR")
	if address == "" {
		t.Fatal("TEST_REDIS_ADDR must point to isolated Redis")
	}
	cache := redis.NewClient(&redis.Options{Addr: address})
	defer cache.Close()
	key := fmt.Sprintf("signalwatch-test:%d:done", f.sub.ID)
	checkRecovery(t, cache.Set(ctx, key, "1", time.Minute).Err())
	checkRecovery(t, cache.Del(ctx, key).Err())
	// A fresh store has no Redis state, yet the database prevents the next batch.
	next, _ := digest.NewProcessor(digest.NewRepository(f.db), digest.NewMySQLDeliveryStore(f.db), sender, func() time.Time { return f.now })
	r, err := next.Process(ctx, job)
	checkRecovery(t, err)
	if !r.AlreadyComplete || len(sender.messages) != 2 {
		t.Fatal("completed day sent again")
	}
	var delivered int64
	checkRecovery(t, f.db.Table("subscription_papers").Where("subscription_id=? AND delivered_at IS NOT NULL", f.sub.ID).Count(&delivered).Error)
	if delivered != 1 {
		t.Fatalf("daily cap breached: %d", delivered)
	}
	job.LocalDate = f.now.AddDate(0, 0, 1).Format("2006-01-02")
	d, ok, err := store.Claim(ctx, job)
	checkRecovery(t, err)
	if !ok {
		t.Fatal("next day not claimed")
	}
	checkRecovery(t, f.db.Exec("UPDATE digest_deliveries SET attempts=5,lease_until=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE id=?", d.ID).Error)
	_, ok, err = store.Claim(ctx, job)
	checkRecovery(t, err)
	if ok {
		t.Fatal("exhausted task reclaimed")
	}
	var state string
	checkRecovery(t, f.db.Table("digest_deliveries").Select("state").Where("id=?", d.ID).Scan(&state).Error)
	if state != "failed" {
		t.Fatalf("state=%s", state)
	}
	checkRecovery(t, store.Retry(ctx, d.ID))
}
func TestCollectorFencingAndMonotonicVersions(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := t.Context()
	name := fmt.Sprintf("test-source-%d", f.src.ID)
	defer f.db.Exec("DELETE FROM collection_leases WHERE name=?", name)
	manager, err := collector.NewMySQLLockManager(f.db, name)
	checkRecovery(t, err)
	old, release, ok, err := manager.Acquire(ctx, time.Minute)
	checkRecovery(t, err)
	if !ok {
		t.Fatal("first claim failed")
	}
	defer release(context.Background())
	checkRecovery(t, f.db.Exec("UPDATE collection_leases SET expires_at=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE name=?", name).Error)
	fresh, releaseFresh, ok, err := manager.Acquire(ctx, time.Minute)
	checkRecovery(t, err)
	if !ok {
		t.Fatal("recovery failed")
	}
	defer releaseFresh(context.Background())
	repo := collector.NewRepository(f.db)
	if err := repo.UpdateLastSuccessfulSyncAt(old, f.src.ID, f.now.Add(time.Hour)); !errors.Is(err, fence.ErrLost) {
		t.Fatalf("old execution committed: %v", err)
	}
	checkRecovery(t, repo.UpdateLastSuccessfulSyncAt(fresh, f.src.ID, f.now))
	checkRecovery(t, repo.UpdateLastSuccessfulSyncAt(fresh, f.src.ID, f.now.Add(-time.Hour)))
	var saved source.Source
	checkRecovery(t, f.db.First(&saved, f.src.ID).Error)
	if !saved.LastSuccessfulSyncAt.Equal(f.now) {
		t.Fatal("checkpoint moved backwards")
	}
	p := f.papers[0]
	record := paper.Record{ArXivID: p.ArXivID, Title: "obsolete", Abstract: "obsolete", Authors: []string{"Ada"}, ArXivURL: p.ArXivURL, PDFURL: p.PDFURL, Categories: []string{"cs.CL"}, PublishedAt: f.now.Add(-time.Hour), ArXivUpdatedAt: f.now.Add(-time.Hour)}
	_, err = paper.NewRepository(f.db).Upsert(old, f.src.ID, []paper.Record{record}, f.now)
	if !errors.Is(err, fence.ErrLost) {
		t.Fatalf("old writer committed: %v", err)
	}
	_, err = paper.NewRepository(f.db).Upsert(fresh, f.src.ID, []paper.Record{record}, f.now)
	checkRecovery(t, err)
	var stored paper.Paper
	checkRecovery(t, f.db.First(&stored, p.ID).Error)
	if stored.Title != "Agent" {
		t.Fatal("older paper overwrote latest version")
	}
}
func TestBackfillLeaseRecoveryAndRealtimeOverlap(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := t.Context()
	checkRecovery(t, f.db.Exec("INSERT INTO subscription_backfills(subscription_id,source_id,category,keywords_json,window_from,window_to,next_retry_at,updated_at) VALUES(?,?,?,'[]',?,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))", f.sub.ID, f.src.ID, "cs.AI", f.now.Add(-7*24*time.Hour), f.now).Error)
	store := backfill.NewMySQLStore(f.db)
	old, ok, err := store.Claim(ctx)
	checkRecovery(t, err)
	if !ok {
		t.Fatal("no backfill")
	}
	checkRecovery(t, f.db.Exec("UPDATE subscription_backfills SET lease_until=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE subscription_id=?", f.sub.ID).Error)
	fresh, ok, err := store.Claim(ctx)
	checkRecovery(t, err)
	if !ok || fresh.LeaseOwner == old.LeaseOwner {
		t.Fatal("backfill not recovered")
	}
	if err := store.Commit(ctx, old, 0, 0, nil, true); !errors.Is(err, backfill.ErrLeaseLost) {
		t.Fatalf("stale lease=%v", err)
	}
	papers, err := store.Papers(ctx, fresh, 200)
	checkRecovery(t, err)
	if len(papers) != 3 {
		t.Fatalf("papers=%d", len(papers))
	}
	matches := []backfill.Match{}
	for _, p := range papers {
		matches = append(matches, backfill.Match{PaperID: p.ID, Keywords: []string{}})
	}
	checkRecovery(t, store.Commit(ctx, fresh, papers[0].ID, 1, matches[:1], false))
	resumed, ok, err := store.Claim(ctx)
	checkRecovery(t, err)
	if !ok || resumed.CursorID != papers[0].ID {
		t.Fatal("committed cursor not recovered")
	}
	remainder, err := store.Papers(ctx, resumed, 200)
	checkRecovery(t, err)
	if len(remainder) != 2 || remainder[0].ID != papers[1].ID {
		t.Fatal("resume repeated completed batch")
	}
	checkRecovery(t, store.Commit(ctx, resumed, papers[2].ID, 2, matches[1:], true))
	var count int64
	checkRecovery(t, f.db.Table("subscription_papers").Where("subscription_id=?", f.sub.ID).Count(&count).Error)
	if count != 3 {
		t.Fatal("realtime overlap created duplicates")
	}
	sub, err := subscription.NewRepository(f.db).Get(ctx, f.u.ID, f.sub.ID)
	checkRecovery(t, err)
	if sub.Subscription.Backfill.State != "complete" || sub.Subscription.Backfill.Processed != 3 {
		t.Fatalf("backfill invisible: %+v", sub.Subscription.Backfill)
	}
}

type availableConfiguration struct{}

func (availableConfiguration) Active(context.Context, uint64) (bool, error) { return true, nil }
func TestSubscriptionAIFieldsRoundTripOverHTTP(t *testing.T) {
	f := newRecoveryFixture(t)
	sqlDB, _ := f.db.DB()
	var logs bytes.Buffer
	api := newM1TestAPI(t, f.db, sqlDB.PingContext, &logs, availableConfiguration{})
	email := fmt.Sprintf("roundtrip-%d@example.test", time.Now().UnixNano())
	t.Cleanup(func() { cleanupM1TestData(t, f.db, []string{email}, "") })
	api.do(t, http.MethodPost, "/api/v2/auth/register", "", "", map[string]any{"email": email, "password": testPassword}, 201)
	login := decodeResponse[loginResponse](t, api.do(t, http.MethodPost, "/api/v2/auth/login", "", "", map[string]any{"email": email, "password": testPassword}, 200))
	var uid uint64
	checkRecovery(t, f.db.Table("users").Select("id").Where("email=?", email).Scan(&uid).Error)
	checkRecovery(t, f.db.Exec("INSERT INTO user_ai_configurations(user_id,provider_id,model_id,status,config_version,generation,key_hint,secret_ciphertext,secret_nonce,master_key_version,created_at,updated_at) VALUES(?,'glm','glm-4.7-flash','active',1,'test','1234',X'00',X'00','test',UTC_TIMESTAMP(),UTC_TIMESTAMP())", uid).Error)
	body := map[string]any{"source_id": f.src.ID, "name": "AI round trip", "digest_ai_enabled": true, "digest_ai_language": "en", "rules": map[string]any{"category": "cs.AI", "keywords": []string{"agent"}}}
	created := decodeResponse[subscription.PublicSubscription](t, api.do(t, http.MethodPost, "/api/v2/subscriptions", login.AccessToken, "", body, 201))
	assert := func(s subscription.PublicSubscription) {
		t.Helper()
		if !s.DigestAIEnabled || s.DigestAILanguage != "en" || s.Backfill.State != "pending" {
			t.Fatalf("lost fields: %+v", s)
		}
	}
	assert(created)
	path := fmt.Sprintf("/api/v2/subscriptions/%d", created.ID)
	assert(decodeResponse[subscription.PublicSubscription](t, api.do(t, http.MethodGet, path, login.AccessToken, "", nil, 200)))
	page := decodeResponse[subscription.ListResult](t, api.do(t, http.MethodGet, "/api/v2/subscriptions", login.AccessToken, "", nil, 200))
	if len(page.Items) != 1 {
		t.Fatal("unexpected subscriptions")
	}
	assert(page.Items[0])
	assert(decodeResponse[subscription.PublicSubscription](t, api.do(t, http.MethodPatch, path, login.AccessToken, `"1"`, map[string]any{"name": "renamed"}, 200)))
}

func TestSubscriptionConcurrentQuotaAndOptimisticVersion(t *testing.T) {
	for _, mode := range []string{"create", "enable"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			ctx := t.Context()
			rows := make([]subscription.Subscription, 18)
			for i := range rows {
				rows[i] = f.sub
				rows[i].ID = 0
				rows[i].Name = fmt.Sprintf("Existing %d", i)
			}
			checkRecovery(t, f.db.Create(&rows).Error)
			service := subscription.NewService(subscription.NewRepository(f.db), source.NewService(source.NewRepository(f.db)))
			targets := make([]subscription.Subscription, 8)
			for i := range targets {
				targets[i] = f.sub
				targets[i].ID = 0
				targets[i].Enabled = false
				targets[i].Name = fmt.Sprintf("Paused %d", i)
			}
			if mode == "enable" {
				checkRecovery(t, f.db.Create(&targets).Error)
			}
			results := make(chan error, 8)
			var wg sync.WaitGroup
			for i := range 8 {
				wg.Go(func() {
					var err error
					if mode == "create" {
						_, err = service.Create(ctx, f.u.ID, subscription.CreateInput{SourceID: f.src.ID, Name: fmt.Sprintf("Created %d", i), Rules: subscription.RulesInput{Category: "cs.AI"}})
					} else {
						on := true
						_, err = service.Update(ctx, f.u.ID, targets[i].ID, 1, subscription.UpdateInput{Enabled: &on})
					}
					results <- err
				})
			}
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				} else if !errors.Is(err, subscription.ErrLimitReached) {
					t.Fatal(err)
				}
			}
			if success != 1 {
				t.Fatalf("quota admitted %d competing changes", success)
			}
			var enabled int64
			checkRecovery(t, f.db.Table("subscriptions").Where("user_id=? AND enabled=1 AND deleted_at IS NULL", f.u.ID).Count(&enabled).Error)
			if enabled != 20 {
				t.Fatalf("enabled=%d", enabled)
			}
			name := f.sub.Name
			unchanged, err := service.Update(ctx, f.u.ID, f.sub.ID, 1, subscription.UpdateInput{Name: &name})
			checkRecovery(t, err)
			if unchanged.Version != 1 {
				t.Fatal("no-op incremented version")
			}
			name = "renamed"
			updated, err := service.Update(ctx, f.u.ID, f.sub.ID, 1, subscription.UpdateInput{Name: &name})
			checkRecovery(t, err)
			if updated.Version != 2 {
				t.Fatal("change did not increment version")
			}
			if err := service.Delete(ctx, f.u.ID, f.sub.ID, 1); !errors.Is(err, subscription.ErrVersionConflict) {
				t.Fatalf("stale delete=%v", err)
			}
		})
	}
}

type enqueueFaultRepository struct{ subscription.Repository }
type enqueueFaultTx struct{ subscription.Tx }

func (enqueueFaultTx) Enqueue(context.Context, *subscription.Subscription, subscription.BackfillWindow) error {
	return errors.New("injected task write failure")
}
func (r enqueueFaultRepository) Transact(ctx context.Context, fn func(subscription.Tx) error) error {
	return r.Repository.Transact(ctx, func(tx subscription.Tx) error { return fn(enqueueFaultTx{tx}) })
}
func TestSubscriptionTaskFailureRollsBackRealTransaction(t *testing.T) {
	f := newRecoveryFixture(t)
	service := subscription.NewService(enqueueFaultRepository{subscription.NewRepository(f.db)}, source.NewService(source.NewRepository(f.db)))
	_, err := service.Create(t.Context(), f.u.ID, subscription.CreateInput{SourceID: f.src.ID, Name: "must rollback", Rules: subscription.RulesInput{Category: "cs.AI"}})
	if err == nil {
		t.Fatal("injected failure ignored")
	}
	var count int64
	checkRecovery(t, f.db.Table("subscriptions").Where("user_id=? AND name='must rollback'", f.u.ID).Count(&count).Error)
	if count != 0 {
		t.Fatal("subscription survived failed task write")
	}
}

func TestBackfillFailureIsVisibleAndRetryRetainsProgress(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := t.Context()
	checkRecovery(t, f.db.Exec("INSERT INTO subscription_backfills(subscription_id,source_id,category,keywords_json,window_from,window_to,cursor_id,processed,matched,next_retry_at,updated_at) VALUES(?,?,'cs.AI','[]',?,?,?,1,1,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))", f.sub.ID, f.src.ID, f.now.Add(-7*24*time.Hour), f.now, f.papers[0].ID).Error)
	store := backfill.NewMySQLStore(f.db)
	for range 5 {
		task, ok, err := store.Claim(ctx)
		checkRecovery(t, err)
		if !ok {
			t.Fatal("task not claimable")
		}
		checkRecovery(t, store.Fail(ctx, task))
		checkRecovery(t, f.db.Exec("UPDATE subscription_backfills SET next_retry_at=UTC_TIMESTAMP(6) WHERE subscription_id=?", f.sub.ID).Error)
	}
	row, err := subscription.NewRepository(f.db).Get(ctx, f.u.ID, f.sub.ID)
	checkRecovery(t, err)
	if row.Subscription.Backfill.State != "failed" || row.Subscription.Backfill.Processed != 1 {
		t.Fatal("failure or prior progress invisible")
	}
	checkRecovery(t, store.Retry(ctx, f.sub.ID))
	task, ok, err := store.Claim(ctx)
	checkRecovery(t, err)
	if !ok || task.CursorID != f.papers[0].ID {
		t.Fatal("manual retry reset progress")
	}
	checkRecovery(t, f.db.Table("subscriptions").Where("id=?", f.sub.ID).Update("category", "cs.CL").Error)
	checkRecovery(t, store.Commit(ctx, task, f.papers[1].ID, 1, []backfill.Match{{PaperID: f.papers[1].ID}}, false))
	row, err = subscription.NewRepository(f.db).Get(ctx, f.u.ID, f.sub.ID)
	checkRecovery(t, err)
	if row.Subscription.Backfill.State != "cancelled" {
		t.Fatal("obsolete rules continued backfill")
	}
}
