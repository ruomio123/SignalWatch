package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"signalwatch/internal/paper"
	"signalwatch/internal/source"
	"signalwatch/internal/source/arxiv"
)

type sourceStub struct {
	active      []ActiveSource
	listErr     error
	updates     []time.Time
	updateIDs   []uint64
	updateError error
}

func (stub *sourceStub) ListEnabledArXivSources(context.Context) ([]ActiveSource, error) {
	return stub.active, stub.listErr
}

func (stub *sourceStub) UpdateLastSuccessfulSyncAt(_ context.Context, id uint64, at time.Time) error {
	stub.updateIDs = append(stub.updateIDs, id)
	stub.updates = append(stub.updates, at)
	return stub.updateError
}

type paperStub struct {
	calls   int
	records [][]paper.Record
	err     error
}

func (stub *paperStub) Upsert(
	_ context.Context,
	_ uint64,
	records []paper.Record,
	_ time.Time,
) (paper.UpsertResult, error) {
	stub.calls++
	stub.records = append(stub.records, append([]paper.Record(nil), records...))
	if stub.err != nil {
		return paper.UpsertResult{}, stub.err
	}
	stored := make([]paper.Paper, len(records))
	for index := range records {
		stored[index].ID = uint64(stub.calls*100 + index + 1)
	}
	return paper.UpsertResult{Inserted: len(records), Papers: stored}, nil
}

type submitterStub struct {
	ids []uint64
	err error
}

func (stub *submitterStub) Submit(_ context.Context, id uint64) error {
	stub.ids = append(stub.ids, id)
	return stub.err
}

type lockStub struct{ acquired bool }

func (stub lockStub) Acquire(context.Context, time.Duration) (ReleaseFunc, bool, error) {
	return func(context.Context) error { return nil }, stub.acquired, nil
}

type searchStub struct {
	pageRequests []arxiv.FetchPageRequest
	idRequests   [][]string
	page         func(arxiv.FetchPageRequest) (arxiv.FetchPageResult, error)
	ids          func([]string) (arxiv.FetchPageResult, error)
}

func (stub *searchStub) FetchPage(_ context.Context, request arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
	stub.pageRequests = append(stub.pageRequests, request)
	return stub.page(request)
}

func (stub *searchStub) FetchIDs(_ context.Context, ids []string) (arxiv.FetchPageResult, error) {
	stub.idRequests = append(stub.idRequests, append([]string(nil), ids...))
	return stub.ids(ids)
}

type feedStub struct {
	categories []string
	results    map[string]arxiv.FeedResult
	err        error
}

func (stub *feedStub) FetchIDs(_ context.Context, category string) (arxiv.FeedResult, error) {
	stub.categories = append(stub.categories, category)
	if stub.err != nil {
		return arxiv.FeedResult{}, stub.err
	}
	return stub.results[category], nil
}

type factoryStub struct {
	search SearchClient
	feed   FeedClient
}

func (stub factoryStub) CreateSearch(string) (SearchClient, error) { return stub.search, nil }
func (stub factoryStub) CreateFeed() (FeedClient, error)           { return stub.feed, nil }

func TestServiceBootstrapUsesConfiguredCategoriesWithoutSubscriptionDemand(t *testing.T) {
	now := time.Date(2026, 9, 9, 6, 0, 0, 0, time.UTC)
	endpoint := "https://export.arxiv.org/api/query"
	sources := &sourceStub{active: []ActiveSource{{
		Source: source.Source{ID: 1, Endpoint: &endpoint}, Categories: []string{"cs.AI", "cs.CL"},
	}}}
	search := &searchStub{page: func(request arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
		return arxiv.FetchPageResult{
			Records: []paper.Record{
				validRecordAt(request.Category, now.Add(-time.Hour)),
				validRecordAt("too-old", now.Add(-8*24*time.Hour)),
			}, Requests: 1, TotalResults: 20,
		}, nil
	}}
	papers := &paperStub{}
	submitter := &submitterStub{}
	service := newTestService(t, sources, papers, submitter, search, &feedStub{}, now, 100)

	result, err := service.Sync(context.Background(), startupRequest(now.Add(-time.Hour)))
	if err != nil || !result.AllCurrent || result.Synced != 1 {
		t.Fatalf("bootstrap sync: result=%+v error=%v", result, err)
	}
	if len(search.pageRequests) != 2 || search.pageRequests[0].Category != "cs.AI" ||
		search.pageRequests[1].Category != "cs.CL" {
		t.Fatalf("configured categories were not fetched sequentially: %+v", search.pageRequests)
	}
	if papers.calls != 2 || len(submitter.ids) != 2 || len(sources.updates) != 1 || !sources.updates[0].Equal(now) {
		t.Fatalf("bootstrap did not persist, submit, and checkpoint atomically by cycle: papers=%d submitted=%v checkpoints=%v", papers.calls, submitter.ids, sources.updates)
	}
}

func TestServiceRecoveryUsesCheckpointOverlapAndDoesNotAdvanceOnPartialFailure(t *testing.T) {
	now := time.Date(2026, 9, 9, 6, 0, 0, 0, time.UTC)
	checkpoint := now.Add(-48 * time.Hour)
	windowStart := checkpoint.Add(-24 * time.Hour)
	endpoint := "https://export.arxiv.org/api/query"
	sources := &sourceStub{active: []ActiveSource{{
		Source:     source.Source{ID: 1, Endpoint: &endpoint, LastSuccessfulSyncAt: &checkpoint},
		Categories: []string{"cs.AI", "cs.CL"},
	}}}
	search := &searchStub{page: func(request arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
		if request.Category == "cs.CL" {
			return arxiv.FetchPageResult{Requests: 1}, errors.New("unavailable")
		}
		return arxiv.FetchPageResult{Records: []paper.Record{
			validRecordAt("boundary", windowStart), validRecordAt("old", windowStart.Add(-time.Nanosecond)),
		}, Requests: 1, TotalResults: 50}, nil
	}}
	papers := &paperStub{}
	service := newTestService(t, sources, papers, &submitterStub{}, search, &feedStub{}, now, 100)

	result, err := service.Sync(context.Background(), startupRequest(now.Add(-time.Hour)))
	if err == nil || result.AllCurrent || len(sources.updates) != 0 {
		t.Fatalf("partial recovery must fail without checkpoint: result=%+v error=%v updates=%v", result, err, sources.updates)
	}
	if papers.calls != 1 || len(papers.records[0]) != 1 || papers.records[0][0].ArXivID != "boundary" {
		t.Fatalf("recovery did not use inclusive overlap boundary: %+v", papers.records)
	}
}

func TestServiceDailyDiscoversPerCategoryDeduplicatesAndHydratesInBatches(t *testing.T) {
	now := time.Date(2026, 9, 9, 4, 31, 0, 0, time.UTC)
	due := now.Add(-time.Minute)
	previousDue := due.AddDate(0, 0, -1)
	checkpoint := previousDue
	endpoint := "https://export.arxiv.org/api/query"
	sources := &sourceStub{active: []ActiveSource{{
		Source:     source.Source{ID: 1, Endpoint: &endpoint, LastSuccessfulSyncAt: &checkpoint},
		Categories: []string{"cs.AI", "cs.CL"},
	}}}
	feed := &feedStub{results: map[string]arxiv.FeedResult{
		"cs.AI": {IDs: []string{"2609.00001v1", "2609.00002v2"}, Requests: 1},
		"cs.CL": {IDs: []string{"2609.00002v2", "2609.00003v1"}, Requests: 1},
	}}
	search := &searchStub{ids: func(ids []string) (arxiv.FetchPageResult, error) {
		records := make([]paper.Record, 0, len(ids))
		for _, id := range ids {
			stable, _ := arxiv.StableID(id)
			records = append(records, validRecordAt(stable, now))
		}
		return arxiv.FetchPageResult{Records: records, Requests: 1}, nil
	}, page: func(arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
		t.Fatal("daily sync must not discover through Search API")
		return arxiv.FetchPageResult{}, nil
	}}
	papers := &paperStub{}
	service := newTestService(t, sources, papers, &submitterStub{}, search, feed, now, 2)

	result, err := service.Sync(context.Background(), SyncRequest{
		Trigger: TriggerDaily, DueAt: due, PreviousDueAt: previousDue,
	})
	if err != nil || !result.AllCurrent || len(sources.updates) != 1 {
		t.Fatalf("daily sync: result=%+v error=%v", result, err)
	}
	if len(feed.categories) != 2 || feed.categories[0] != "cs.AI" || feed.categories[1] != "cs.CL" {
		t.Fatalf("Feed categories not fetched sequentially: %v", feed.categories)
	}
	if len(search.idRequests) != 2 || len(search.idRequests[0]) != 2 || len(search.idRequests[1]) != 1 || papers.calls != 2 {
		t.Fatalf("unexpected deduped hydration batches: ids=%v paper_calls=%d", search.idRequests, papers.calls)
	}
}

func TestServiceDailyRejectsIncompleteHydrationWithoutCheckpoint(t *testing.T) {
	now := time.Date(2026, 9, 9, 4, 31, 0, 0, time.UTC)
	previousDue := now.Add(-24 * time.Hour)
	endpoint := "https://export.arxiv.org/api/query"
	sources := &sourceStub{active: []ActiveSource{{
		Source:     source.Source{ID: 1, Endpoint: &endpoint, LastSuccessfulSyncAt: &previousDue},
		Categories: []string{"cs.AI"},
	}}}
	feed := &feedStub{results: map[string]arxiv.FeedResult{"cs.AI": {IDs: []string{"2609.00001v1"}}}}
	search := &searchStub{ids: func([]string) (arxiv.FetchPageResult, error) {
		return arxiv.FetchPageResult{}, nil
	}, page: func(arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) { return arxiv.FetchPageResult{}, nil }}
	service := newTestService(t, sources, &paperStub{}, &submitterStub{}, search, feed, now, 100)

	result, err := service.Sync(context.Background(), SyncRequest{
		Trigger: TriggerDaily, DueAt: now.Add(-time.Minute), PreviousDueAt: previousDue,
	})
	if err == nil || result.AllCurrent || len(sources.updates) != 0 {
		t.Fatalf("missing hydration must prevent checkpoint: result=%+v error=%v", result, err)
	}
}

func TestServiceDailyEmptyFeedAdvancesCheckpointWithoutHydration(t *testing.T) {
	now := time.Date(2026, 9, 9, 4, 31, 0, 0, time.UTC)
	previousDue := now.Add(-24 * time.Hour)
	endpoint := "https://export.arxiv.org/api/query"
	sources := &sourceStub{active: []ActiveSource{{
		Source:     source.Source{ID: 1, Endpoint: &endpoint, LastSuccessfulSyncAt: &previousDue},
		Categories: []string{"cs.AI"},
	}}}
	search := &searchStub{
		ids: func([]string) (arxiv.FetchPageResult, error) {
			t.Fatal("empty Feed must not call hydration")
			return arxiv.FetchPageResult{}, nil
		},
		page: func(arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
			t.Fatal("daily sync must not call Search discovery")
			return arxiv.FetchPageResult{}, nil
		},
	}
	feed := &feedStub{results: map[string]arxiv.FeedResult{"cs.AI": {IDs: []string{}}}}
	service := newTestService(t, sources, &paperStub{}, &submitterStub{}, search, feed, now, 100)

	result, err := service.Sync(context.Background(), SyncRequest{
		Trigger: TriggerDaily, DueAt: now.Add(-time.Minute), PreviousDueAt: previousDue,
	})
	if err != nil || !result.AllCurrent || result.Synced != 1 || len(sources.updates) != 1 {
		t.Fatalf("empty Feed should complete the source cycle: result=%+v error=%v", result, err)
	}
}

func TestServiceCheckpointWriteFailureLeavesCyclePending(t *testing.T) {
	now := time.Date(2026, 9, 9, 6, 0, 0, 0, time.UTC)
	endpoint := "https://export.arxiv.org/api/query"
	sources := &sourceStub{
		active:      []ActiveSource{{Source: source.Source{ID: 1, Endpoint: &endpoint}, Categories: []string{"cs.AI"}}},
		updateError: errors.New("checkpoint unavailable"),
	}
	search := &searchStub{page: func(arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
		return arxiv.FetchPageResult{Requests: 1}, nil
	}}
	service := newTestService(t, sources, &paperStub{}, &submitterStub{}, search, &feedStub{}, now, 100)

	result, err := service.Sync(context.Background(), startupRequest(now.Add(-time.Hour)))
	if err == nil || result.AllCurrent || result.Synced != 0 || len(sources.updates) != 1 {
		t.Fatalf("checkpoint write failure must keep the source pending: result=%+v error=%v", result, err)
	}
}

func TestServiceSkipsSourceWhoseCheckpointCoversDueBoundary(t *testing.T) {
	due := time.Date(2026, 9, 9, 4, 30, 0, 0, time.UTC)
	checkpoint := due
	sources := &sourceStub{active: []ActiveSource{{
		Source: source.Source{ID: 1, LastSuccessfulSyncAt: &checkpoint}, Categories: []string{"cs.AI"},
	}}}
	search := &searchStub{
		page: func(arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
			t.Fatal("current checkpoint must skip Search")
			return arxiv.FetchPageResult{}, nil
		},
		ids: func([]string) (arxiv.FetchPageResult, error) {
			t.Fatal("current checkpoint must skip hydration")
			return arxiv.FetchPageResult{}, nil
		},
	}
	service := newTestService(t, sources, &paperStub{}, &submitterStub{}, search, &feedStub{}, due, 100)

	result, err := service.Sync(context.Background(), SyncRequest{
		Trigger: TriggerDaily, DueAt: due, PreviousDueAt: due.AddDate(0, 0, -1),
	})
	if err != nil || !result.AllCurrent || result.Current != 1 || result.Synced != 0 || len(sources.updates) != 0 {
		t.Fatalf("covered due boundary should be skipped: result=%+v error=%v", result, err)
	}
}

func TestServiceCancellationNeverAdvancesCheckpoint(t *testing.T) {
	now := time.Date(2026, 9, 9, 6, 0, 0, 0, time.UTC)
	endpoint := "https://export.arxiv.org/api/query"
	sources := &sourceStub{active: []ActiveSource{{
		Source: source.Source{ID: 1, Endpoint: &endpoint}, Categories: []string{"cs.AI"},
	}}}
	search := &searchStub{page: func(arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
		return arxiv.FetchPageResult{Requests: 1}, nil
	}}
	service := newTestService(t, sources, &paperStub{}, &submitterStub{}, search, &feedStub{}, now, 100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.Sync(ctx, startupRequest(now.Add(-time.Hour)))
	if !errors.Is(err, context.Canceled) || result.AllCurrent || len(sources.updates) != 0 {
		t.Fatalf("cancelled cycle must not checkpoint: result=%+v error=%v", result, err)
	}
}

func TestServiceRetriesWhenLockIsNotAcquired(t *testing.T) {
	service := newTestServiceWithLock(t, &sourceStub{}, &paperStub{}, &submitterStub{},
		&searchStub{}, &feedStub{}, time.Now().UTC(), 100, lockStub{acquired: false})
	result, err := service.Sync(context.Background(), startupRequest(time.Now().Add(-time.Hour)))
	if err != nil || result.Acquired || result.AllCurrent {
		t.Fatalf("lock miss must remain pending: result=%+v error=%v", result, err)
	}
}

func newTestService(
	t *testing.T,
	sources SourceRepository,
	papers PaperRepository,
	submitter PaperSubmitter,
	search SearchClient,
	feed FeedClient,
	now time.Time,
	pageSize int,
) *Service {
	return newTestServiceWithLock(t, sources, papers, submitter, search, feed, now, pageSize, lockStub{acquired: true})
}

func newTestServiceWithLock(
	t *testing.T,
	sources SourceRepository,
	papers PaperRepository,
	submitter PaperSubmitter,
	search SearchClient,
	feed FeedClient,
	now time.Time,
	pageSize int,
	lock LockManager,
) *Service {
	t.Helper()
	service, err := NewService(
		sources, papers, submitter, lock, factoryStub{search: search, feed: feed},
		slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now },
		Config{BootstrapLookback: 7 * 24 * time.Hour, RecoveryOverlap: 24 * time.Hour, LockTTL: time.Hour, PageSize: pageSize, MaxPages: 10},
	)
	if err != nil {
		t.Fatalf("new collector service: %v", err)
	}
	return service
}

func startupRequest(due time.Time) SyncRequest {
	return SyncRequest{Trigger: TriggerStartup, DueAt: due, PreviousDueAt: due.AddDate(0, 0, -1)}
}

func validRecordAt(id string, updatedAt time.Time) paper.Record {
	return paper.Record{
		ArXivID: id, Title: "title", Abstract: "abstract", Authors: []string{"Author"},
		Categories: []string{"cs.AI"}, PublishedAt: updatedAt.Add(-time.Hour),
		ArXivUpdatedAt: updatedAt, ArXivURL: "https://arxiv.org/abs/" + id,
		PDFURL: "https://arxiv.org/pdf/" + id,
	}
}
