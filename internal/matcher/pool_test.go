package matcher

import (
	"context"
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
