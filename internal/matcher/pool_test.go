package matcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type blockingMatcher struct {
	mu        sync.Mutex
	active    int
	maxActive int
	started   chan struct{}
	release   chan struct{}
}

func (matcher *blockingMatcher) Match(ctx context.Context, _ uint64) (Result, error) {
	matcher.mu.Lock()
	matcher.active++
	if matcher.active > matcher.maxActive {
		matcher.maxActive = matcher.active
	}
	matcher.mu.Unlock()
	matcher.started <- struct{}{}
	select {
	case <-matcher.release:
	case <-ctx.Done():
		matcher.mu.Lock()
		matcher.active--
		matcher.mu.Unlock()
		return Result{}, ctx.Err()
	}
	matcher.mu.Lock()
	matcher.active--
	matcher.mu.Unlock()
	return Result{}, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPoolAppliesBoundedQueueBackpressure(t *testing.T) {
	pool, err := NewPool(&blockingMatcher{}, discardLogger(), PoolConfig{Workers: 1, QueueCapacity: 1})
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	if err := pool.Submit(context.Background(), 1); err != nil {
		t.Fatalf("fill queue: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := pool.Submit(ctx, 2); err != context.DeadlineExceeded {
		t.Fatalf("full queue should block until context deadline, got %v", err)
	}
}

func TestPoolRejectsSubmissionAfterCancellation(t *testing.T) {
	pool, err := NewPool(&blockingMatcher{}, discardLogger(), PoolConfig{Workers: 1, QueueCapacity: 1})
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pool.Submit(ctx, 1); err != context.Canceled {
		t.Fatalf("canceled submission should fail, got %v", err)
	}
}

func TestPoolUsesFixedConcurrencyAndStopsOnCancellation(t *testing.T) {
	matcher := &blockingMatcher{
		started: make(chan struct{}, 4),
		release: make(chan struct{}, 4),
	}
	pool, err := NewPool(matcher, discardLogger(), PoolConfig{Workers: 2, QueueCapacity: 4})
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	for paperID := uint64(1); paperID <= 4; paperID++ {
		if err := pool.Submit(context.Background(), paperID); err != nil {
			t.Fatalf("submit %d: %v", paperID, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pool.Run(ctx)
		close(done)
	}()
	for started := 0; started < 2; started++ {
		select {
		case <-matcher.started:
		case <-time.After(time.Second):
			t.Fatal("configured workers did not start")
		}
	}
	matcher.mu.Lock()
	maxActive := matcher.maxActive
	matcher.mu.Unlock()
	if maxActive != 2 {
		t.Fatalf("expected exactly two concurrent workers, got %d", maxActive)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pool did not stop after cancellation")
	}
}

type matcherFunc func(context.Context, uint64) (Result, error)

func (f matcherFunc) Match(ctx context.Context, id uint64) (Result, error) { return f(ctx, id) }

func TestBatchWaitsForMatchingAndPropagatesFailure(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	failure := errors.New("temporary database failure")
	pool, err := NewPool(matcherFunc(func(ctx context.Context, id uint64) (Result, error) {
		if id == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
			return Result{}, failure
		}
		return Result{}, nil
	}), discardLogger(), PoolConfig{Workers: 2, QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); pool.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-workerDone })
	done := make(chan error, 1)
	go func() { done <- pool.SubmitBatch(ctx, []uint64{1, 2, 3}) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("batch never started")
	}
	select {
	case err := <-done:
		t.Fatalf("batch returned before matching finished: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, failure) {
			t.Fatalf("lost match error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("batch did not finish")
	}
	if err := pool.SubmitBatch(ctx, []uint64{2, 3}); err != nil {
		t.Fatalf("next batch retained old failure: %v", err)
	}
}

func TestBatchCancellationDoesNotBlockConsumers(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	pool, _ := NewPool(matcherFunc(func(ctx context.Context, _ uint64) (Result, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return Result{}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}), discardLogger(), PoolConfig{Workers: 1, QueueCapacity: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); pool.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-workerDone })
	batchCtx, cancelBatch := context.WithCancel(ctx)
	defer cancelBatch()
	done := make(chan error, 1)
	go func() { done <- pool.SubmitBatch(batchCtx, []uint64{1, 2}) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("batch never started")
	}
	cancelBatch()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("batch cancellation blocked")
	}
	close(release)
	if err := pool.SubmitBatch(ctx, []uint64{3}); err != nil {
		t.Fatalf("abandoned batch blocked worker: %v", err)
	}
}
