package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"

	"signalwatch/internal/collector"
	"signalwatch/internal/paper"
	"signalwatch/internal/source"
	"signalwatch/internal/source/arxiv"
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

func (client m2PageClient) FetchIDs(context.Context, []string) (arxiv.FetchPageResult, error) {
	return arxiv.FetchPageResult{Records: []paper.Record{client.record}, Requests: 1, TotalResults: 1}, nil
}

type m2ClientFactory struct {
	client collector.SearchClient
}

func (factory m2ClientFactory) CreateSearch(string) (collector.SearchClient, error) {
	return factory.client, nil
}

func (m2ClientFactory) CreateFeed() (collector.FeedClient, error) {
	return nil, errors.New("Feed client is not used by bootstrap")
}

type m2Lock struct{}

func (m2Lock) Acquire(context.Context, time.Duration) (collector.ReleaseFunc, bool, error) {
	return func(context.Context) error { return nil }, true, nil
}

type m2Submitter struct{}

func (m2Submitter) SubmitBatch(context.Context, []uint64) error { return nil }

type m2SourceRepository struct {
	db     *gorm.DB
	active collector.ActiveSource
}

func (repository m2SourceRepository) ListEnabledArXivSources(context.Context) ([]collector.ActiveSource, error) {
	return []collector.ActiveSource{repository.active}, nil
}

func (repository m2SourceRepository) UpdateLastSuccessfulSyncAt(_ context.Context, id uint64, at time.Time) error {
	return repository.db.Model(&source.Source{}).Where("id = ?", id).
		Update("last_successful_sync_at", at.UTC()).Error
}

func TestV2SystemIngestionCheckpointAndSingleRowPaperUpsert(t *testing.T) {
	database, sqlDB, _ := openM1TestDatabase(t)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated M2 test connection pool: %v", err)
		}
	})

	if !database.Migrator().HasTable(&paper.Paper{}) {
		t.Fatal("papers table is missing; apply all migrations")
	}
	if !database.Migrator().HasColumn(&source.Source{}, "last_successful_sync_at") {
		t.Fatal("source sync checkpoint is missing; apply all migrations")
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
	t.Cleanup(func() {
		_ = database.Where("source_id = ?", sourceRecord.ID).Delete(&paper.Paper{}).Error
		_ = database.Delete(&sourceRecord).Error
	})

	sources := collector.NewRepository(database)
	active, err := sources.ListEnabledArXivSources(t.Context())
	if err != nil {
		t.Fatalf("list active arxiv sources: %v", err)
	}
	var fixture *collector.ActiveSource
	for index := range active {
		if active[index].Source.ID == sourceRecord.ID {
			fixture = &active[index]
			break
		}
	}
	if fixture == nil || len(fixture.Categories) != 2 ||
		fixture.Categories[0] != "cs.AI" || fixture.Categories[1] != "cs.CV" {
		t.Fatalf("source categories must come directly from source configuration: %+v", active)
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
		m2SourceRepository{db: database, active: *fixture},
		papers,
		m2Submitter{},
		m2Lock{},
		m2ClientFactory{client: m2PageClient{record: record}},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() time.Time { return collectorNow },
		collector.Config{
			BootstrapLookback: 7 * 24 * time.Hour, RecoveryOverlap: 24 * time.Hour,
			LockTTL: time.Minute, PageSize: 100, MaxPages: 2,
		},
	)
	if err != nil {
		t.Fatalf("create collector service: %v", err)
	}
	due := collectorNow.Add(-time.Hour)
	for run := 1; run <= 2; run++ {
		result, err := collectorService.Sync(t.Context(), collector.SyncRequest{
			Trigger: collector.TriggerStartup, DueAt: due, PreviousDueAt: due.AddDate(0, 0, -1),
		})
		if err != nil || !result.AllCurrent {
			t.Fatalf("collector rerun %d: result=%+v error=%v", run, result, err)
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
	var checkpointed source.Source
	if err := database.Where("id = ?", sourceRecord.ID).Take(&checkpointed).Error; err != nil {
		t.Fatalf("load source checkpoint: %v", err)
	}
	if checkpointed.LastSuccessfulSyncAt == nil || !checkpointed.LastSuccessfulSyncAt.Equal(collectorNow) {
		t.Fatalf("successful system ingestion did not persist checkpoint: %+v", checkpointed.LastSuccessfulSyncAt)
	}
}
