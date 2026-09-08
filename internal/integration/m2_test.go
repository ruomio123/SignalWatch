package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"signalwatch/internal/collector"
	"signalwatch/internal/paper"
	"signalwatch/internal/source"
	"signalwatch/internal/source/arxiv"
	"signalwatch/internal/subscription"
	"signalwatch/internal/user"
)

type m2PageClient struct {
	record paper.Record
}

func (client m2PageClient) FetchPage(
	context.Context,
	arxiv.FetchPageRequest,
) (arxiv.FetchPageResult, error) {
	return arxiv.FetchPageResult{
		Records: []paper.Record{client.record}, Requests: 1, TotalResults: 1,
	}, nil
}

type m2ClientFactory struct {
	client collector.ArXivClient
}

func (factory m2ClientFactory) Create(string) (collector.ArXivClient, error) {
	return factory.client, nil
}

type m2Lock struct{}

func (m2Lock) Acquire(context.Context, time.Duration) (collector.ReleaseFunc, bool, error) {
	return func(context.Context) error { return nil }, true, nil
}

type m2Submitter struct{}

func (m2Submitter) Submit(context.Context, uint64) error { return nil }

func TestM2StatelessDemandAndSingleRowPaperUpsert(t *testing.T) {
	database, sqlDB, _ := openM1TestDatabase(t)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated M2 test connection pool: %v", err)
		}
	})

	if !database.Migrator().HasTable(&paper.Paper{}) {
		t.Fatal("papers table is missing; apply all migrations")
	}
	for _, removed := range []string{
		"source_categories", "fetch_targets", "fetch_runs", "contents", "content_revisions",
	} {
		if database.Migrator().HasTable(removed) {
			t.Fatalf("obsolete M2 table %s must not exist", removed)
		}
	}
	var seededArXiv source.Source
	if err := database.Where("source_key = ?", "arxiv").Take(&seededArXiv).Error; err != nil {
		t.Fatalf("load seeded arxiv source: %v", err)
	}
	publicArXiv, err := seededArXiv.Public()
	if err != nil {
		t.Fatalf("read seeded arxiv capabilities: %v", err)
	}
	allowed := make(map[string]bool, len(publicArXiv.AllowedCategories))
	for _, category := range publicArXiv.AllowedCategories {
		allowed[category] = true
	}
	for _, required := range []string{"cs.AI", "cs.CL", "cs.CV", "cs.IR", "cs.LG", "cs.RO", "cs.SE"} {
		if !allowed[required] {
			t.Fatalf("seeded arxiv source is missing allowed category %s", required)
		}
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	nonce := strconv.FormatInt(now.UnixNano(), 36)
	endpoint := "https://export.arxiv.org/api/query"
	sourceRecord := source.Source{
		SourceKey: "m2-arxiv-" + nonce, Kind: source.KindArXiv,
		Name: "M2 arXiv fixture", Endpoint: &endpoint, Enabled: true,
		ConfigJSON: json.RawMessage(
			`{"allowed_categories":["cs.AI","cs.CV"],"rule_types":["category","include_keyword"]}`,
		),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&sourceRecord).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	userRecord := user.User{
		Email: "m2-" + nonce + "@example.test", PasswordHash: "not-used-in-this-test",
		Timezone: "UTC", DigestTime: "08:00:00", MaxItemsPerDigest: 50,
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&userRecord).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	var subscriptionIDs []uint64
	t.Cleanup(func() {
		_ = database.Where("source_id = ?", sourceRecord.ID).Delete(&paper.Paper{}).Error
		if len(subscriptionIDs) > 0 {
			_ = database.Unscoped().Where("id IN ?", subscriptionIDs).Delete(&subscription.Subscription{}).Error
		}
		_ = database.Delete(&sourceRecord).Error
		_ = database.Delete(&userRecord).Error
	})

	createSubscription := func(name string, enabled bool, category string) uint64 {
		subscriptionRecord := subscription.Subscription{
			UserID: userRecord.ID, SourceID: sourceRecord.ID, Name: name,
			Category: category, KeywordsJSON: json.RawMessage(`[]`),
			Enabled: enabled, Version: subscription.InitialVersion,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := database.Create(&subscriptionRecord).Error; err != nil {
			t.Fatalf("create subscription: %v", err)
		}
		subscriptionIDs = append(subscriptionIDs, subscriptionRecord.ID)
		return subscriptionRecord.ID
	}

	firstID := createSubscription("first", true, "cs.CV")
	createSubscription("second", true, "cs.AI")
	createSubscription("disabled", false, "cs.CV")

	demands := collector.NewRepository(database)
	active, err := demands.ListActiveArXivSources(t.Context())
	if err != nil {
		t.Fatalf("list active arxiv sources: %v", err)
	}
	if len(active) != 1 || active[0].Source.ID != sourceRecord.ID ||
		len(active[0].Categories) != 2 ||
		active[0].Categories[0] != "cs.AI" || active[0].Categories[1] != "cs.CV" {
		t.Fatalf("active categories must be shared and deduplicated: %+v", active)
	}

	if err := database.Model(&subscription.Subscription{}).
		Where("id = ?", firstID).
		Update("deleted_at", now).Error; err != nil {
		t.Fatalf("soft delete first subscription: %v", err)
	}
	active, err = demands.ListActiveArXivSources(t.Context())
	if err != nil {
		t.Fatalf("list demand after soft delete: %v", err)
	}
	if len(active) != 1 || len(active[0].Categories) != 1 || active[0].Categories[0] != "cs.AI" {
		t.Fatalf("disabled and deleted subscriptions must not create demand: %+v", active)
	}

	published := now.Add(-time.Hour)
	record := paper.Record{
		ArXivID: "2609." + nonce,
		Title:   "M2 integration paper", Abstract: "A normalized abstract.",
		Authors: []string{"Integration Author"}, Categories: []string{"cs.AI"},
		PublishedAt: published, ArXivUpdatedAt: published,
		ArXivURL: "https://arxiv.org/abs/2609." + nonce,
		PDFURL:   "https://arxiv.org/pdf/2609." + nonce,
	}
	papers := paper.NewRepository(database)
	first, err := papers.Upsert(t.Context(), sourceRecord.ID, []paper.Record{record}, now)
	if err != nil {
		t.Fatalf("insert paper: %v", err)
	}
	if first.Inserted != 1 || first.Updated != 0 || len(first.Papers) != 1 {
		t.Fatalf("unexpected first upsert: %+v", first)
	}

	record.Title = "M2 integration paper, revised"
	record.Abstract = "The latest arXiv metadata overwrites the same row."
	record.ArXivUpdatedAt = now.Add(time.Hour)
	second, err := papers.Upsert(
		t.Context(), sourceRecord.ID, []paper.Record{record}, now.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("update paper: %v", err)
	}
	if second.Inserted != 0 || second.Updated != 1 || len(second.Papers) != 1 {
		t.Fatalf("unexpected update result: %+v", second)
	}

	var stored paper.Paper
	if err := database.Where(
		"source_id = ? AND arxiv_id = ?", sourceRecord.ID, record.ArXivID,
	).Take(&stored).Error; err != nil {
		t.Fatalf("load paper: %v", err)
	}
	var count int64
	if err := database.Model(&paper.Paper{}).
		Where("source_id = ? AND arxiv_id = ?", sourceRecord.ID, record.ArXivID).
		Count(&count).Error; err != nil {
		t.Fatalf("count papers: %v", err)
	}
	if count != 1 || stored.Title != record.Title || stored.Abstract != record.Abstract ||
		!stored.FirstSeenAt.Equal(now) ||
		!stored.ArXivUpdatedAt.Equal(record.ArXivUpdatedAt) {
		t.Fatalf("arxiv updates must overwrite one row and retain first_seen_at: count=%d paper=%+v", count, stored)
	}

	collectorNow := now.Add(2 * time.Hour)
	collectorService, err := collector.NewService(
		demands,
		papers,
		m2Submitter{},
		m2Lock{},
		m2ClientFactory{client: m2PageClient{record: record}},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() time.Time { return collectorNow },
		collector.Config{Lookback: 48 * time.Hour, LockTTL: time.Minute, MaxPages: 2},
	)
	if err != nil {
		t.Fatalf("create collector service: %v", err)
	}
	for run := 1; run <= 2; run++ {
		if err := collectorService.Run(t.Context()); err != nil {
			t.Fatalf("collector rerun %d: %v", run, err)
		}
	}
	if err := database.Model(&paper.Paper{}).
		Where("source_id = ? AND arxiv_id = ?", sourceRecord.ID, record.ArXivID).
		Count(&count).Error; err != nil {
		t.Fatalf("count papers after collector reruns: %v", err)
	}
	if err := database.Where(
		"source_id = ? AND arxiv_id = ?", sourceRecord.ID, record.ArXivID,
	).Take(&stored).Error; err != nil {
		t.Fatalf("load paper after collector reruns: %v", err)
	}
	if count != 1 || !stored.FirstSeenAt.Equal(now) || stored.Title != record.Title {
		t.Fatalf("collector reruns must remain idempotent: count=%d paper=%+v", count, stored)
	}
}
