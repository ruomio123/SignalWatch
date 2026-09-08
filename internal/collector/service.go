package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"signalwatch/internal/paper"
	"signalwatch/internal/source/arxiv"
)

type ArXivClient interface {
	FetchPage(ctx context.Context, input arxiv.FetchPageRequest) (arxiv.FetchPageResult, error)
}

type ClientFactory interface {
	Create(endpoint string) (ArXivClient, error)
}

type PaperRepository interface {
	Upsert(ctx context.Context, sourceID uint64, records []paper.Record, observedAt time.Time) (paper.UpsertResult, error)
}

type PaperSubmitter interface {
	Submit(ctx context.Context, paperID uint64) error
}

type Config struct {
	Lookback time.Duration
	LockTTL  time.Duration
	MaxPages int
}

type categoryStats struct {
	requests  int
	pages     int
	fetched   int
	inserted  int
	updated   int
	submitted int
}

type Service struct {
	demands   DemandRepository
	papers    PaperRepository
	submitter PaperSubmitter
	lock      LockManager
	clients   ClientFactory
	logger    *slog.Logger
	now       func() time.Time
	config    Config
}

func NewService(
	demands DemandRepository,
	papers PaperRepository,
	submitter PaperSubmitter,
	lock LockManager,
	clients ClientFactory,
	logger *slog.Logger,
	now func() time.Time,
	config Config,
) (*Service, error) {
	if demands == nil || papers == nil || submitter == nil || lock == nil || clients == nil ||
		logger == nil || now == nil || config.Lookback <= 0 || config.LockTTL <= 0 ||
		config.MaxPages < 1 {
		return nil, errors.New("invalid collector service configuration")
	}
	return &Service{
		demands: demands, papers: papers, submitter: submitter, lock: lock, clients: clients,
		logger: logger, now: now, config: config,
	}, nil
}

func (service *Service) Run(ctx context.Context) error {
	release, acquired, err := service.lock.Acquire(ctx, service.config.LockTTL)
	if err != nil {
		return fmt.Errorf("acquire collector lock: %w", err)
	}
	if !acquired {
		service.logger.Debug("collector cycle skipped because another worker owns the lock", "module", "collector")
		return nil
	}
	defer func() {
		releaseContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := release(releaseContext); err != nil {
			service.logger.Warn("release collector lock failed", "module", "collector", "error", err)
		}
	}()

	activeSources, err := service.demands.ListActiveArXivSources(ctx)
	if err != nil {
		return err
	}
	if len(activeSources) == 0 {
		service.logger.Info("collector cycle skipped because there are no active categories", "module", "collector")
		return nil
	}

	windowEnd := service.now().UTC()
	windowStart := windowEnd.Add(-service.config.Lookback)
	var cycleErrors []error
	totalCategories := 0
	totalRequests := 0
	totalPages := 0
	totalFetched := 0
	totalInserted := 0
	totalUpdated := 0
	totalSubmitted := 0
	for _, active := range activeSources {
		if active.Source.Endpoint == nil {
			cycleErrors = append(cycleErrors, fmt.Errorf("source %d has no endpoint", active.Source.ID))
			continue
		}
		client, err := service.clients.Create(*active.Source.Endpoint)
		if err != nil {
			cycleErrors = append(cycleErrors, fmt.Errorf("create arxiv client for source %d: %w", active.Source.ID, err))
			continue
		}
		for _, category := range active.Categories {
			totalCategories++
			stats, err := service.collectCategory(
				ctx, client, active.Source.ID, category, windowStart, windowEnd,
			)
			totalRequests += stats.requests
			totalPages += stats.pages
			totalFetched += stats.fetched
			totalInserted += stats.inserted
			totalUpdated += stats.updated
			totalSubmitted += stats.submitted
			if err != nil {
				service.logger.Warn(
					"arxiv category fetch failed",
					"module", "collector", "source_id", active.Source.ID,
					"category", category, "requests", stats.requests,
					"pages", stats.pages, "fetched", stats.fetched,
					"inserted", stats.inserted, "updated", stats.updated,
					"submitted", stats.submitted,
					"error", err,
				)
				cycleErrors = append(cycleErrors, fmt.Errorf("collect %s: %w", category, err))
				continue
			}
			service.logger.Info(
				"arxiv category collected",
				"module", "collector", "source_id", active.Source.ID,
				"category", category, "requests", stats.requests,
				"pages", stats.pages, "fetched", stats.fetched,
				"inserted", stats.inserted, "updated", stats.updated,
				"submitted", stats.submitted,
			)
		}
	}
	service.logger.Info(
		"collector cycle finished",
		"module", "collector", "categories", totalCategories,
		"requests", totalRequests, "pages", totalPages,
		"fetched", totalFetched, "inserted", totalInserted,
		"updated", totalUpdated, "submitted", totalSubmitted,
		"errors", len(cycleErrors),
		"window_start", windowStart, "window_end", windowEnd,
	)
	return errors.Join(cycleErrors...)
}

func (service *Service) collectCategory(
	ctx context.Context,
	client ArXivClient,
	sourceID uint64,
	category string,
	windowStart time.Time,
	windowEnd time.Time,
) (categoryStats, error) {
	var stats categoryStats
	startIndex := 0
	for page := 0; page < service.config.MaxPages; page++ {
		fetched, err := client.FetchPage(ctx, arxiv.FetchPageRequest{
			Category: category,
			Start:    startIndex,
		})
		stats.requests += fetched.Requests
		if err != nil {
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
				continue
			default:
				pageRecords = append(pageRecords, record)
			}
		}

		if len(pageRecords) > 0 {
			upserted, err := service.papers.Upsert(
				ctx, sourceID, pageRecords, service.now().UTC(),
			)
			if err != nil {
				return stats, fmt.Errorf("upsert page %d: %w", page+1, err)
			}
			stats.fetched += len(pageRecords)
			stats.inserted += upserted.Inserted
			stats.updated += upserted.Updated
			for _, stored := range upserted.Papers {
				if err := service.submitter.Submit(ctx, stored.ID); err != nil {
					return stats, fmt.Errorf("submit paper %d from page %d: %w", stored.ID, page+1, err)
				}
				stats.submitted++
			}
		}

		startIndex += len(fetched.Records)
		if len(fetched.Records) == 0 || startIndex >= fetched.TotalResults || reachedWindowStart {
			return stats, nil
		}
	}
	return stats, fmt.Errorf("result requires more than %d pages", service.config.MaxPages)
}

type ArXivClientFactory struct {
	HTTPClient interface {
		Do(request *http.Request) (*http.Response, error)
	}
	Limiter arxiv.RateLimiter
	Config  arxiv.Config
}

func (factory ArXivClientFactory) Create(endpoint string) (ArXivClient, error) {
	config := factory.Config
	config.Endpoint = endpoint
	return arxiv.NewClient(factory.HTTPClient, factory.Limiter, config)
}
