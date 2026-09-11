package collector

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"signalwatch/internal/paper"
	"signalwatch/internal/source/arxiv"
)

type SearchClient interface {
	FetchPage(ctx context.Context, input arxiv.FetchPageRequest) (arxiv.FetchPageResult, error)
	FetchIDs(ctx context.Context, identifiers []string) (arxiv.FetchPageResult, error)
}

type FeedClient interface {
	FetchIDs(ctx context.Context, category string) (arxiv.FeedResult, error)
}

type ClientFactory interface {
	CreateSearch(endpoint string) (SearchClient, error)
	CreateFeed() (FeedClient, error)
}

type PaperRepository interface {
	Upsert(ctx context.Context, sourceID uint64, records []paper.Record, observedAt time.Time) (paper.UpsertResult, error)
}

type PaperSubmitter interface {
	// SubmitBatch returns only after all papers have finished matching.
	SubmitBatch(ctx context.Context, paperIDs []uint64) error
}

type Trigger string

const (
	TriggerStartup Trigger = "startup"
	TriggerDaily   Trigger = "daily"
)

type SyncMode string

const (
	ModeBootstrap SyncMode = "bootstrap"
	ModeRecovery  SyncMode = "recovery"
	ModeDaily     SyncMode = "daily"
)

type SyncRequest struct {
	Trigger       Trigger
	DueAt         time.Time
	PreviousDueAt time.Time
	AttemptID     string
}

type SyncResult struct {
	Acquired   bool
	Sources    int
	Synced     int
	Current    int
	AllCurrent bool
}

type Config struct {
	BootstrapLookback time.Duration
	RecoveryOverlap   time.Duration
	LockTTL           time.Duration
	PageSize          int
	MaxPages          int
}

type syncStats struct {
	categories int
	requests   int
	pages      int
	discovered int
	hydrated   int
	inserted   int
	updated    int
	submitted  int
	windowFrom *time.Time
	windowTo   *time.Time
	failStage  string
}

type Service struct {
	sources   SourceRepository
	papers    PaperRepository
	submitter PaperSubmitter
	lock      LockManager
	clients   ClientFactory
	logger    *slog.Logger
	now       func() time.Time
	config    Config
	observer  Observer
}

func (service *Service) SetObserver(observer Observer) { service.observer = observer }

func NewService(
	sources SourceRepository,
	papers PaperRepository,
	submitter PaperSubmitter,
	lock LockManager,
	clients ClientFactory,
	logger *slog.Logger,
	now func() time.Time,
	config Config,
) (*Service, error) {
	if sources == nil || papers == nil || submitter == nil || lock == nil || clients == nil ||
		logger == nil || now == nil || config.BootstrapLookback <= 0 ||
		config.RecoveryOverlap <= 0 || config.LockTTL <= 0 || config.PageSize < 1 ||
		config.MaxPages < 1 {
		return nil, errors.New("invalid collector service configuration")
	}
	return &Service{
		sources: sources, papers: papers, submitter: submitter, lock: lock,
		clients: clients, logger: logger, now: now, config: config,
	}, nil
}

func (service *Service) Sync(ctx context.Context, request SyncRequest) (SyncResult, error) {
	if request.DueAt.IsZero() || (request.Trigger != TriggerStartup && request.Trigger != TriggerDaily) ||
		(request.Trigger == TriggerDaily && request.PreviousDueAt.IsZero()) {
		return SyncResult{}, errors.New("invalid sync request")
	}
	ctx, release, acquired, err := service.lock.Acquire(ctx, service.config.LockTTL)
	if err != nil {
		return SyncResult{}, fmt.Errorf("acquire collector lock: %w", err)
	}
	if !acquired {
		service.logger.Debug("arxiv sync deferred because another worker owns the lock", "module", "collector")
		return SyncResult{Acquired: false}, nil
	}
	defer service.release(release)

	sources, err := service.sources.ListEnabledArXivSources(ctx)
	if err != nil {
		return SyncResult{Acquired: true}, err
	}
	result := SyncResult{Acquired: true, Sources: len(sources), AllCurrent: true}
	var syncErrors []error
	for _, active := range sources {
		checkpoint := active.Source.LastSuccessfulSyncAt
		if checkpoint != nil && !checkpoint.UTC().Before(request.DueAt.UTC()) {
			result.Current++
			continue
		}
		mode := service.modeFor(checkpoint, request)
		attemptID := request.AttemptID
		if attemptID == "" {
			attemptID = rand.Text()
		}
		windowFrom, windowTo := service.observationWindow(active, mode, request)
		startedAt := service.now().UTC()
		service.observeSource(ctx, SourceObservation{
			SourceID: active.Source.ID, State: "running", Mode: mode, AttemptID: attemptID,
			WindowFrom: windowFrom, WindowTo: windowTo, StartedAt: startedAt,
		})
		stats, err := service.syncSource(ctx, active, mode, request)
		if stats.windowFrom == nil {
			stats.windowFrom = windowFrom
		}
		if stats.windowTo == nil {
			stats.windowTo = windowTo
		}
		if err != nil {
			result.AllCurrent = false
			syncErrors = append(syncErrors, fmt.Errorf("sync source %d in %s mode: %w", active.Source.ID, mode, err))
			service.logSource(active, mode, attemptID, startedAt, checkpoint, nil, stats, err)
			completed := service.now().UTC()
			service.observeSource(ctx, sourceObservation(active.Source.ID, "failed", mode, attemptID, startedAt, &completed, stats))
			continue
		}
		if err := ctx.Err(); err != nil {
			stats.failStage = "cancelled"
			result.AllCurrent = false
			syncErrors = append(syncErrors, err)
			service.logSource(active, mode, attemptID, startedAt, checkpoint, nil, stats, err)
			completed := service.now().UTC()
			statusContext, cancelStatus := context.WithTimeout(context.Background(), 2*time.Second)
			service.observeSource(statusContext, sourceObservation(active.Source.ID, "cancelled", mode, attemptID, startedAt, &completed, stats))
			cancelStatus()
			continue
		}
		completedAt := service.now().UTC()
		if err := service.sources.UpdateLastSuccessfulSyncAt(ctx, active.Source.ID, completedAt); err != nil {
			stats.failStage = "checkpoint_update"
			result.AllCurrent = false
			syncErrors = append(syncErrors, err)
			service.logSource(active, mode, attemptID, startedAt, checkpoint, nil, stats, err)
			completed := service.now().UTC()
			service.observeSource(ctx, sourceObservation(active.Source.ID, "failed", mode, attemptID, startedAt, &completed, stats))
			continue
		}
		result.Synced++
		service.logSource(active, mode, attemptID, startedAt, checkpoint, &completedAt, stats, nil)
		service.observeSource(ctx, sourceObservation(active.Source.ID, "succeeded", mode, attemptID, startedAt, &completedAt, stats))
	}
	return result, errors.Join(syncErrors...)
}

func (service *Service) observationWindow(active ActiveSource, mode SyncMode, request SyncRequest) (*time.Time, *time.Time) {
	to := service.now().UTC()
	var from time.Time
	switch mode {
	case ModeBootstrap:
		from = to.Add(-service.config.BootstrapLookback)
	case ModeRecovery:
		from = active.Source.LastSuccessfulSyncAt.UTC().Add(-service.config.RecoveryOverlap)
	default:
		from, to = request.PreviousDueAt.UTC(), request.DueAt.UTC()
	}
	return &from, &to
}

func sourceObservation(
	sourceID uint64,
	state string,
	mode SyncMode,
	attemptID string,
	started time.Time,
	completed *time.Time,
	stats syncStats,
) SourceObservation {
	return SourceObservation{
		SourceID: sourceID, State: state, Mode: mode, AttemptID: attemptID,
		WindowFrom: stats.windowFrom, WindowTo: stats.windowTo, StartedAt: started,
		CompletedAt: completed, FailStage: stats.failStage,
		Metrics: map[string]int{
			"categories": stats.categories, "requests": stats.requests, "pages": stats.pages,
			"feed_ids": stats.discovered, "hydrated": stats.hydrated,
			"inserted": stats.inserted, "updated": stats.updated, "submitted": stats.submitted,
		},
	}
}

func (service *Service) observeSource(ctx context.Context, observation SourceObservation) {
	if service.observer == nil {
		return
	}
	if err := service.observer.ObserveSource(ctx, observation); err != nil {
		service.logger.Warn("collector status observation failed", "module", "collector",
			"event", "collector_status_write_failed", "source_id", observation.SourceID, "error", err)
	}
}

func (service *Service) modeFor(checkpoint *time.Time, request SyncRequest) SyncMode {
	if checkpoint == nil {
		return ModeBootstrap
	}
	if request.Trigger == TriggerStartup || checkpoint.UTC().Before(request.PreviousDueAt.UTC()) {
		return ModeRecovery
	}
	return ModeDaily
}

func (service *Service) syncSource(
	ctx context.Context,
	active ActiveSource,
	mode SyncMode,
	request SyncRequest,
) (syncStats, error) {
	if active.Source.Endpoint == nil {
		return syncStats{failStage: "source_config"}, errors.New("source has no Search API endpoint")
	}
	search, err := service.clients.CreateSearch(*active.Source.Endpoint)
	if err != nil {
		return syncStats{failStage: "search_client"}, fmt.Errorf("create Search client: %w", err)
	}
	if mode == ModeDaily {
		feed, err := service.clients.CreateFeed()
		if err != nil {
			return syncStats{failStage: "feed_client"}, fmt.Errorf("create Feed client: %w", err)
		}
		stats, err := service.syncDaily(ctx, active, feed, search)
		from, to := request.PreviousDueAt.UTC(), request.DueAt.UTC()
		stats.windowFrom, stats.windowTo = &from, &to
		return stats, err
	}
	windowEnd := service.now().UTC()
	windowStart := windowEnd.Add(-service.config.BootstrapLookback)
	if mode == ModeRecovery {
		windowStart = active.Source.LastSuccessfulSyncAt.UTC().Add(-service.config.RecoveryOverlap)
	}
	return service.syncSearch(ctx, active, search, windowStart, windowEnd)
}

func (service *Service) syncSearch(
	ctx context.Context,
	active ActiveSource,
	client SearchClient,
	windowStart time.Time,
	windowEnd time.Time,
) (syncStats, error) {
	from, to := windowStart.UTC(), windowEnd.UTC()
	total := syncStats{windowFrom: &from, windowTo: &to}
	var categoryErrors []error
	for _, category := range active.Categories {
		total.categories++
		stats, err := service.collectSearchCategory(ctx, client, active.Source.ID, category, windowStart, windowEnd)
		total.add(stats)
		if err != nil {
			categoryErrors = append(categoryErrors, fmt.Errorf("collect %s: %w", category, err))
		}
	}
	return total, errors.Join(categoryErrors...)
}

func (service *Service) collectSearchCategory(
	ctx context.Context,
	client SearchClient,
	sourceID uint64,
	category string,
	windowStart time.Time,
	windowEnd time.Time,
) (syncStats, error) {
	var stats syncStats
	startIndex := 0
	for pageNumber := 0; pageNumber < service.config.MaxPages; pageNumber++ {
		fetched, err := client.FetchPage(ctx, arxiv.FetchPageRequest{Category: category, Start: startIndex})
		stats.requests += fetched.Requests
		if err != nil {
			stats.failStage = "search_fetch"
			return stats, err
		}
		stats.pages++
		pageRecords := make([]paper.Record, 0, len(fetched.Records))
		reachedWindowStart := false
		for _, record := range fetched.Records {
			switch {
			case record.ArXivUpdatedAt.After(windowEnd):
				continue
			case record.ArXivUpdatedAt.Before(windowStart):
				reachedWindowStart = true
			default:
				pageRecords = append(pageRecords, record)
			}
		}
		if err := service.persistAndSubmit(ctx, sourceID, pageRecords, &stats); err != nil {
			return stats, fmt.Errorf("process page %d: %w", pageNumber+1, err)
		}
		startIndex += len(fetched.Records)
		if len(fetched.Records) == 0 || startIndex >= fetched.TotalResults || reachedWindowStart {
			return stats, nil
		}
	}
	stats.failStage = "search_page_limit"
	return stats, fmt.Errorf("result requires more than %d pages", service.config.MaxPages)
}

func (service *Service) syncDaily(
	ctx context.Context,
	active ActiveSource,
	feed FeedClient,
	search SearchClient,
) (syncStats, error) {
	var stats syncStats
	versionedByStable := make(map[string]string)
	orderedStable := make([]string, 0)
	for _, category := range active.Categories {
		stats.categories++
		result, err := feed.FetchIDs(ctx, category)
		stats.requests += result.Requests
		if err != nil {
			stats.failStage = "feed_fetch"
			return stats, fmt.Errorf("fetch Feed category %s: %w", category, err)
		}
		stats.discovered += len(result.IDs)
		for _, identifier := range result.IDs {
			stable, err := arxiv.StableID(identifier)
			if err != nil {
				stats.failStage = "feed_validate"
				return stats, fmt.Errorf("normalize Feed ID: %w", err)
			}
			if previous, exists := versionedByStable[stable]; exists {
				if previous != identifier {
					stats.failStage = "feed_validate"
					return stats, fmt.Errorf("Feed returned conflicting versions for %s", stable)
				}
				continue
			}
			versionedByStable[stable] = identifier
			orderedStable = append(orderedStable, stable)
		}
	}

	for start := 0; start < len(orderedStable); start += service.config.PageSize {
		end := min(start+service.config.PageSize, len(orderedStable))
		requested := make([]string, 0, end-start)
		for _, stable := range orderedStable[start:end] {
			requested = append(requested, versionedByStable[stable])
		}
		result, err := search.FetchIDs(ctx, requested)
		stats.requests += result.Requests
		if err != nil {
			stats.failStage = "hydration_fetch"
			return stats, fmt.Errorf("hydrate Feed IDs: %w", err)
		}
		seen := make(map[string]struct{}, len(result.Records))
		for _, record := range result.Records {
			if _, expected := versionedByStable[record.ArXivID]; !expected {
				stats.failStage = "hydration_validate"
				return stats, fmt.Errorf("hydration returned unexpected ID %s", record.ArXivID)
			}
			if _, duplicate := seen[record.ArXivID]; duplicate {
				stats.failStage = "hydration_validate"
				return stats, fmt.Errorf("hydration returned duplicate ID %s", record.ArXivID)
			}
			seen[record.ArXivID] = struct{}{}
		}
		for _, stable := range orderedStable[start:end] {
			if _, ok := seen[stable]; !ok {
				stats.failStage = "hydration_validate"
				return stats, fmt.Errorf("hydration omitted ID %s", stable)
			}
		}
		stats.hydrated += len(result.Records)
		if err := service.persistAndSubmit(ctx, active.Source.ID, result.Records, &stats); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func (service *Service) persistAndSubmit(
	ctx context.Context,
	sourceID uint64,
	records []paper.Record,
	stats *syncStats,
) error {
	if len(records) == 0 {
		return nil
	}
	upserted, err := service.papers.Upsert(ctx, sourceID, records, service.now().UTC())
	if err != nil {
		stats.failStage = "paper_upsert"
		return fmt.Errorf("upsert papers: %w", err)
	}
	stats.inserted += upserted.Inserted
	stats.updated += upserted.Updated
	ids := make([]uint64, len(upserted.Papers))
	for index, stored := range upserted.Papers {
		ids[index] = stored.ID
	}
	if err := service.submitter.SubmitBatch(ctx, ids); err != nil {
		stats.failStage = "matcher_batch"
		return fmt.Errorf("match paper batch: %w", err)
	}
	stats.submitted += len(ids)
	return nil
}

func (stats *syncStats) add(other syncStats) {
	stats.requests += other.requests
	stats.pages += other.pages
	stats.discovered += other.discovered
	stats.hydrated += other.hydrated
	stats.inserted += other.inserted
	stats.updated += other.updated
	stats.submitted += other.submitted
	if stats.windowFrom == nil {
		stats.windowFrom = other.windowFrom
	}
	if stats.windowTo == nil {
		stats.windowTo = other.windowTo
	}
	if other.failStage != "" {
		stats.failStage = other.failStage
	}
}

func (service *Service) logSource(
	active ActiveSource,
	mode SyncMode,
	attemptID string,
	startedAt time.Time,
	oldCheckpoint *time.Time,
	newCheckpoint *time.Time,
	stats syncStats,
	err error,
) {
	arguments := []any{
		"module", "collector", "task", "collector", "mode", mode,
		"attempt_id", attemptID, "source_id", active.Source.ID,
		"allowed_categories", active.Categories, "categories_attempted", stats.categories,
		"requests", stats.requests, "pages", stats.pages,
		"feed_ids", stats.discovered, "hydrated", stats.hydrated,
		"inserted", stats.inserted, "updated", stats.updated, "submitted", stats.submitted,
		"window_from", stats.windowFrom, "window_to", stats.windowTo,
		"error_stage", stats.failStage, "duration_ms", max(service.now().UTC().Sub(startedAt).Milliseconds(), int64(0)),
		"old_checkpoint", oldCheckpoint, "new_checkpoint", newCheckpoint,
	}
	if err != nil {
		service.logger.Error("arxiv source sync failed", append(arguments,
			"event", "collector_source_sync", "state", "failed", "error", err)...)
		return
	}
	service.logger.Info("arxiv source sync finished", append(arguments,
		"event", "collector_source_sync", "state", "succeeded")...)
}

func (service *Service) release(release ReleaseFunc) {
	releaseContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := release(releaseContext); err != nil {
		service.logger.Warn("release collector lock failed", "module", "collector", "error", err)
	}
}

type ArXivClientFactory struct {
	HTTPClient interface {
		Do(request *http.Request) (*http.Response, error)
	}
	Limiter      arxiv.RateLimiter
	SearchConfig arxiv.Config
	FeedConfig   arxiv.FeedConfig
}

func (factory ArXivClientFactory) CreateSearch(endpoint string) (SearchClient, error) {
	config := factory.SearchConfig
	config.Endpoint = endpoint
	return arxiv.NewClient(factory.HTTPClient, factory.Limiter, config)
}

func (factory ArXivClientFactory) CreateFeed() (FeedClient, error) {
	return arxiv.NewFeedClient(factory.HTTPClient, factory.Limiter, factory.FeedConfig)
}
