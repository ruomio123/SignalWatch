package digest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type JobProcessor interface {
	Process(ctx context.Context, job Job) (ProcessResult, error)
}

type PoolConfig struct {
	Workers       int
	QueueCapacity int
}

type Pool struct {
	processor                JobProcessor
	logger                   *slog.Logger
	config                   PoolConfig
	queue                    chan Job
	active                   atomic.Int64
	skipped, locked, retried atomic.Uint64
	success                  atomic.Uint64
	failed                   atomic.Uint64
	lastSuccess              atomic.Int64
	lastFailure              atomic.Int64
}

type PoolStats struct {
	Depth                    int
	Capacity                 int
	Workers                  int
	Processing               int64
	Skipped, Locked, Retried uint64
	Succeeded                uint64
	Failed                   uint64
	LastSuccessAt            time.Time
	LastFailureAt            time.Time
}

func (pool *Pool) Stats() PoolStats {
	return PoolStats{
		Skipped: pool.skipped.Load(), Locked: pool.locked.Load(), Retried: pool.retried.Load(),
		Depth: len(pool.queue), Capacity: cap(pool.queue), Workers: pool.config.Workers,
		Processing: pool.active.Load(), Succeeded: pool.success.Load(), Failed: pool.failed.Load(),
		LastSuccessAt: poolTime(pool.lastSuccess.Load()), LastFailureAt: poolTime(pool.lastFailure.Load()),
	}
}

func NewPool(processor JobProcessor, logger *slog.Logger, config PoolConfig) (*Pool, error) {
	if processor == nil || logger == nil || config.Workers < 1 || config.QueueCapacity < 1 {
		return nil, errors.New("invalid mail pool configuration")
	}
	return &Pool{
		processor: processor, logger: logger, config: config,
		queue: make(chan Job, config.QueueCapacity),
	}, nil
}

func (pool *Pool) Submit(ctx context.Context, job Job) error {
	if err := validateJob(job); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case pool.queue <- job:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

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
			if ctx.Err() != nil {
				return
			}
			pool.active.Add(1)
			startedAt := time.Now()
			result, err := pool.processor.Process(ctx, job)
			pool.active.Add(-1)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					pool.failed.Add(1)
					pool.lastFailure.Store(time.Now().UTC().UnixNano())
					pool.logger.Error(
						"digest processing failed", "module", "digest", "event", "mail_job",
						"task", "mail", "state", "failed", "error_stage", "process",
						"duration_ms", time.Since(startedAt).Milliseconds(), "worker_id", workerID,
						"subscription_id", job.SubscriptionID, "user_id", job.UserID, "local_date", job.LocalDate, "error", err,
					)
				}
				continue
			}
			state := "sent"
			if result.Locked {
				pool.locked.Add(1)
				state = "lease_conflict"
			} else if result.AlreadyComplete || result.Stale || result.Empty {
				pool.skipped.Add(1)
				state = "completed_skip"
			} else {
				pool.success.Add(1)
				pool.lastSuccess.Store(time.Now().UTC().UnixNano())
			}
			if result.Retried {
				pool.retried.Add(1)
			}
			pool.logger.Info(
				"digest processed", "module", "digest", "event", "mail_job",
				"task", "mail", "state", state, "duration_ms", time.Since(startedAt).Milliseconds(),
				"worker_id", workerID,
				"subscription_id", job.SubscriptionID, "user_id", job.UserID, "local_date", job.LocalDate,
				"items", result.Items, "marked_relations", result.MarkedRelations,
				"locked", result.Locked, "already_complete", result.AlreadyComplete,
				"empty", result.Empty, "stale", result.Stale,
			)
		}
	}
}

func poolTime(nanoseconds int64) time.Time {
	if nanoseconds == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanoseconds).UTC()
}
