package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"signalwatch/internal/backfill"
	"signalwatch/internal/digest"
	"signalwatch/internal/paper"
	"signalwatch/internal/source"
	"signalwatch/internal/subscription"
	"signalwatch/internal/user"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type timedProcessor struct {
	processor *digest.Processor
	starts    sync.Map
	waits     chan time.Duration
	done      atomic.Int64
	failed    atomic.Int64
}

func (p *timedProcessor) Process(ctx context.Context, j digest.Job) (digest.ProcessResult, error) {
	started, _ := p.starts.LoadAndDelete(j.SubscriptionID)
	p.waits <- time.Since(started.(time.Time))
	r, err := p.processor.Process(ctx, j)
	if err != nil {
		p.failed.Add(1)
	}
	p.done.Add(1)
	return r, err
}

type discardSender struct{}

func (discardSender) Send(context.Context, digest.Message) error { return nil }

// Capacity is opt-in because it creates 100k papers and 20k subscriptions.
// It validates scheduling and delivery persistence at scale; SMTP is a local fake.
func TestCapacityWorkload(t *testing.T) {
	if os.Getenv("SIGNALWATCH_CAPACITY") != "1" {
		t.Skip("opt-in capacity workload")
	}
	db, sqlDB, _ := openM1TestDatabase(t)
	defer sqlDB.Close()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	tag := fmt.Sprint(now.UnixNano())
	src := source.Source{SourceKey: "capacity-" + tag, Kind: source.KindArXiv, Name: "Capacity", ConfigJSON: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now}
	checkRecovery(t, db.Create(&src).Error)
	users := make([]user.User, 1000)
	subs := make([]subscription.Subscription, 0, 20000)
	defer func() {
		checkRecovery(t, db.Where("source_id=?", src.ID).Delete(&subscription.Subscription{}).Error)
		checkRecovery(t, db.Where("source_id=?", src.ID).Delete(&paper.Paper{}).Error)
		checkRecovery(t, db.Where("email LIKE ?", "capacity-"+tag+"-%").Delete(&user.User{}).Error)
		checkRecovery(t, db.Delete(&src).Error)
	}()
	for i := range users {
		users[i] = user.User{Email: fmt.Sprintf("capacity-%s-%d@example.test", tag, i), PasswordHash: "unused", Timezone: "UTC", DigestTime: "00:00:00", MaxItemsPerDigest: 10, Status: user.StatusActive, Role: user.RoleUser, CreatedAt: now, UpdatedAt: now}
	}
	checkRecovery(t, db.CreateInBatches(&users, 500).Error)
	for _, u := range users {
		for i := range 20 {
			subs = append(subs, subscription.Subscription{UserID: u.ID, SourceID: src.ID, Name: fmt.Sprintf("Capacity %d", i), Category: "cs.AI", KeywordsJSON: json.RawMessage(`["capacity_signal"]`), Enabled: true, Version: 1, MaxItemsPerDigest: 10, DigestAILanguage: "zh", CreatedAt: now, UpdatedAt: now})
		}
	}
	checkRecovery(t, db.CreateInBatches(&subs, 500).Error)
	for batch := 0; batch < 200; batch++ {
		rows := make([]paper.Paper, 500)
		for i := range rows {
			index := batch*500 + i
			title := "unrelated research"
			if index%1000 == 0 {
				title = "capacity_signal"
			}
			rows[i] = paper.Paper{SourceID: src.ID, ArXivID: fmt.Sprintf("capacity-%s-%d", tag, index), Title: title, Abstract: "Benchmark abstract", AuthorsJSON: json.RawMessage(`["Ada"]`), CategoriesJSON: json.RawMessage(`["cs.AI"]`), PublishedAt: now.Add(-time.Hour), ArXivUpdatedAt: now, FirstSeenAt: now.Add(-time.Minute), CreatedAt: now, UpdatedAt: now, ArXivURL: "https://arxiv.org/abs/fixture", PDFURL: "https://arxiv.org/pdf/fixture"}
		}
		checkRecovery(t, db.Create(&rows).Error)
	}
	type lockCount struct {
		VariableName string `gorm:"column:Variable_name"`
		Value        string
	}
	readLocks := func() map[string]int64 {
		var rows []lockCount
		checkRecovery(t, db.Raw("SHOW GLOBAL STATUS WHERE Variable_name IN ('Innodb_row_lock_waits','Innodb_row_lock_time')").Scan(&rows).Error)
		out := map[string]int64{}
		for _, row := range rows {
			n, err := strconv.ParseInt(row.Value, 10, 64)
			checkRecovery(t, err)
			out[row.VariableName] = n
		}
		return out
	}
	var plan []struct {
		Key   string
		Rows  int64
		Extra string
	}
	checkRecovery(t, db.Raw("EXPLAIN SELECT id,title,abstract,categories_json FROM papers USE INDEX (idx_papers_source_cursor) WHERE source_id=? AND id>0 AND published_at BETWEEN ? AND ? AND first_seen_at<=? ORDER BY id LIMIT 200", src.ID, now.Add(-7*24*time.Hour), now, now).Scan(&plan).Error)
	t.Logf("backfill query plan: %+v", plan)
	lockStart := readLocks()
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	var peak atomic.Uint64
	peak.Store(baseline.HeapAlloc)
	sampleCtx, stopSample := context.WithCancel(ctx)
	defer stopSample()
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sampleCtx.Done():
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				for old := peak.Load(); m.HeapAlloc > old; old = peak.Load() {
					if peak.CompareAndSwap(old, m.HeapAlloc) {
						break
					}
				}
			}
		}
	}()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	checkRecovery(t, db.Exec("INSERT INTO subscription_backfills(subscription_id,source_id,category,keywords_json,window_from,window_to,next_retry_at,updated_at) VALUES(?,?,'cs.AI','[\"capacity_signal\"]',?,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))", subs[0].ID, src.ID, now.Add(-7*24*time.Hour), now).Error)
	worker := backfill.New(backfill.NewMySQLStore(db), logger)
	started := time.Now()
	batches := 0
	for {
		worked, err := worker.Step(ctx)
		checkRecovery(t, err)
		if !worked {
			break
		}
		batches++
	}
	backfillTime := time.Since(started)
	var count int64
	checkRecovery(t, db.Table("subscription_papers").Where("subscription_id=?", subs[0].ID).Count(&count).Error)
	if count != 100 {
		t.Fatalf("backfill matches=%d", count)
	}
	started = time.Now()
	schedules, err := digest.NewRepository(db).ListActiveSchedules(ctx)
	checkRecovery(t, err)
	scheduleTime := time.Since(started)
	if len(schedules) < 20000 {
		t.Fatal("missing schedules")
	}
	processor, err := digest.NewProcessor(digest.NewRepository(db), digest.NewMySQLDeliveryStore(db), discardSender{}, func() time.Time { return now })
	checkRecovery(t, err)
	timed := &timedProcessor{processor: processor, waits: make(chan time.Duration, 20000)}
	pool, err := digest.NewPool(timed, logger, digest.PoolConfig{Workers: 2, QueueCapacity: 100})
	checkRecovery(t, err)
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); pool.Run(run) }()
	started = time.Now()
	for _, s := range subs {
		j := digest.Job{UserID: s.UserID, SubscriptionID: s.ID, LocalDate: now.Format("2006-01-02")}
		timed.starts.Store(s.ID, time.Now())
		checkRecovery(t, pool.Submit(ctx, j))
	}
	deadline := time.Now().Add(3 * time.Minute)
	for timed.done.Load() < 20000 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	deliveryTime := time.Since(started)
	if timed.done.Load() != 20000 || timed.failed.Load() != 0 {
		t.Fatalf("processed=%d failed=%d", timed.done.Load(), timed.failed.Load())
	}
	close(timed.waits)
	waits := []time.Duration{}
	for d := range timed.waits {
		waits = append(waits, d)
	}
	sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
	locks := readLocks()
	for name := range locks {
		locks[name] -= lockStart[name]
	}

	t.Logf("CAPACITY users=1000 subscriptions=20000 papers=100000 schedule=%s backfill=%s batches=%d matched=%d deliveries=%s queue_wait_p50=%s queue_wait_p95=%s heap_baseline=%d heap_peak=%d goroutines=%d CPUs=%d Go=%s workload_lock_deltas=%+v", scheduleTime, backfillTime, batches, count, deliveryTime, waits[len(waits)/2], waits[len(waits)*95/100], baseline.HeapAlloc, peak.Load(), runtime.NumGoroutine(), runtime.NumCPU(), runtime.Version(), locks)
}
