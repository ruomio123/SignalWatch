package digest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

type JobProcessor interface {
	Process(ctx context.Context, job Job) (ProcessResult, error)
}

type PoolConfig struct {
	Workers       int
	QueueCapacity int
}

type Pool struct {
	processor JobProcessor
	logger    *slog.Logger
	config    PoolConfig
	queue     chan Job
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
			result, err := pool.processor.Process(ctx, job)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					pool.logger.Error(
						"digest processing failed", "module", "digest", "worker_id", workerID,
						"user_id", job.UserID, "local_date", job.LocalDate, "error", err,
					)
				}
				continue
			}
			pool.logger.Info(
				"digest processed", "module", "digest", "worker_id", workerID,
				"user_id", job.UserID, "local_date", job.LocalDate,
				"items", result.Items, "marked_relations", result.MarkedRelations,
				"locked", result.Locked, "already_complete", result.AlreadyComplete,
				"empty", result.Empty, "stale", result.Stale,
			)
		}
	}
}
