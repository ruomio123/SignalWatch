package matcher

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

type Matcher interface {
	Match(ctx context.Context, paperID uint64) (Result, error)
}

type PoolConfig struct {
	Workers       int
	QueueCapacity int
}

type Pool struct {
	matcher Matcher
	logger  *slog.Logger
	config  PoolConfig
	queue   chan uint64
}

func NewPool(matcher Matcher, logger *slog.Logger, config PoolConfig) (*Pool, error) {
	if matcher == nil || logger == nil || config.Workers < 1 || config.QueueCapacity < 1 {
		return nil, errors.New("invalid matcher pool configuration")
	}
	return &Pool{
		matcher: matcher,
		logger:  logger,
		config:  config,
		queue:   make(chan uint64, config.QueueCapacity),
	}, nil
}

// Submit blocks while the bounded queue is full. That backpressure prevents a
// fast Collector from creating an unbounded in-memory matching backlog.
func (pool *Pool) Submit(ctx context.Context, paperID uint64) error {
	if paperID == 0 {
		return ErrInvalidPaperID
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case pool.queue <- paperID:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run owns exactly the configured number of workers and returns after all of
// them observe cancellation. Queued work may be abandoned on shutdown; the
// next stateless 48-hour Collector pass safely resubmits it.
func (pool *Pool) Run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(pool.config.Workers)
	for workerID := 1; workerID <= pool.config.Workers; workerID++ {
		go func(id int) {
			defer workers.Done()
			pool.runWorker(ctx, id)
		}(workerID)
	}
	workers.Wait()
}

func (pool *Pool) runWorker(ctx context.Context, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return
		case paperID := <-pool.queue:
			if ctx.Err() != nil {
				return
			}
			result, err := pool.matcher.Match(ctx, paperID)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					pool.logger.Error(
						"paper match failed", "module", "matcher",
						"worker_id", workerID, "paper_id", paperID, "error", err,
					)
				}
				continue
			}
			pool.logger.Debug(
				"paper matched", "module", "matcher", "worker_id", workerID,
				"paper_id", paperID, "candidates", result.Candidates,
				"matched", result.Matched, "inserted", result.Inserted,
			)
		}
	}
}
