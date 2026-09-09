package matcher

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type Matcher interface {
	Match(ctx context.Context, paperID uint64) (Result, error)
}

type PoolConfig struct {
	Workers       int
	QueueCapacity int
}

type matchJob struct {
	paperID   uint64
	completed chan<- error
}

type Pool struct {
	matcher     Matcher
	logger      *slog.Logger
	config      PoolConfig
	queue       chan matchJob
	active      atomic.Int64
	success     atomic.Uint64
	failed      atomic.Uint64
	lastSuccess atomic.Int64
	lastFailure atomic.Int64
}

type PoolStats struct {
	Depth         int
	Capacity      int
	Workers       int
	Processing    int64
	Succeeded     uint64
	Failed        uint64
	LastSuccessAt time.Time
	LastFailureAt time.Time
}

func (pool *Pool) Stats() PoolStats {
	return PoolStats{
		Depth: len(pool.queue), Capacity: cap(pool.queue), Workers: pool.config.Workers,
		Processing: pool.active.Load(), Succeeded: pool.success.Load(), Failed: pool.failed.Load(),
		LastSuccessAt: atomicTime(pool.lastSuccess.Load()), LastFailureAt: atomicTime(pool.lastFailure.Load()),
	}
}

func NewPool(matcher Matcher, logger *slog.Logger, config PoolConfig) (*Pool, error) {
	if matcher == nil || logger == nil || config.Workers < 1 || config.QueueCapacity < 1 {
		return nil, errors.New("invalid matcher pool configuration")
	}
	return &Pool{
		matcher: matcher,
		logger:  logger,
		config:  config,
		queue:   make(chan matchJob, config.QueueCapacity),
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
	case pool.queue <- matchJob{paperID: paperID}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SubmitBatch waits for every accepted paper to finish matching. Collector must
// receive this acknowledgement before advancing its durable source checkpoint.
// The completion buffer is bounded by the Collector page size; consumers never
// block if the submitting context is cancelled.
func (pool *Pool) SubmitBatch(ctx context.Context, paperIDs []uint64) error {
	for _, id := range paperIDs {
		if id == 0 {
			return ErrInvalidPaperID
		}
	}
	completed := make(chan error, len(paperIDs))
	for _, id := range paperIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case pool.queue <- matchJob{paperID: id, completed: completed}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var firstError error
	for range paperIDs {
		select {
		case err := <-completed:
			if firstError == nil {
				firstError = err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return firstError
}

// Run owns the configured workers. Cancelled or abandoned batches cannot be
// acknowledged, so Collector leaves their checkpoint pending for a retry.
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
		case job := <-pool.queue:
			paperID := job.paperID
			if ctx.Err() != nil {
				if job.completed != nil {
					job.completed <- ctx.Err()
				}
				return
			}
			pool.active.Add(1)
			startedAt := time.Now()
			result, err := pool.matcher.Match(ctx, paperID)
			pool.active.Add(-1)
			if job.completed != nil {
				job.completed <- err
			}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					pool.failed.Add(1)
					pool.lastFailure.Store(time.Now().UTC().UnixNano())
					pool.logger.Error(
						"paper match failed", "module", "matcher",
						"event", "matcher_job", "task", "matcher", "state", "failed",
						"error_stage", "match", "duration_ms", time.Since(startedAt).Milliseconds(),
						"worker_id", workerID, "paper_id", paperID, "error", err,
					)
				}
				continue
			}
			pool.success.Add(1)
			pool.lastSuccess.Store(time.Now().UTC().UnixNano())
			pool.logger.Debug(
				"paper matched", "module", "matcher", "event", "matcher_job",
				"task", "matcher", "state", "succeeded", "duration_ms", time.Since(startedAt).Milliseconds(),
				"worker_id", workerID,
				"paper_id", paperID, "candidates", result.Candidates,
				"matched", result.Matched, "inserted", result.Inserted,
			)
		}
	}
}

func atomicTime(nanoseconds int64) time.Time {
	if nanoseconds == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanoseconds).UTC()
}
