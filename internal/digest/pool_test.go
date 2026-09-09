package digest

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type blockingProcessor struct {
	mu        sync.Mutex
	active    int
	maxActive int
	started   chan struct{}
	release   chan struct{}
}

func (processor *blockingProcessor) Process(ctx context.Context, _ Job) (ProcessResult, error) {
	processor.mu.Lock()
	processor.active++
	if processor.active > processor.maxActive {
		processor.maxActive = processor.active
	}
	processor.mu.Unlock()
	processor.started <- struct{}{}
	select {
	case <-processor.release:
	case <-ctx.Done():
		processor.mu.Lock()
		processor.active--
		processor.mu.Unlock()
		return ProcessResult{}, ctx.Err()
	}
	processor.mu.Lock()
	processor.active--
	processor.mu.Unlock()
	return ProcessResult{}, nil
}

func digestTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMailPoolAppliesBoundedBackpressure(t *testing.T) {
	pool, err := NewPool(&blockingProcessor{}, digestTestLogger(), PoolConfig{Workers: 1, QueueCapacity: 1})
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	if err := pool.Submit(t.Context(), Job{UserID: 1, LocalDate: "2026-09-08"}); err != nil {
		t.Fatalf("fill queue: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := pool.Submit(ctx, Job{UserID: 2, LocalDate: "2026-09-08"}); err != context.DeadlineExceeded {
		t.Fatalf("expected queue backpressure deadline, got %v", err)
	}
}

func TestMailPoolUsesConfiguredWorkerConcurrency(t *testing.T) {
	processor := &blockingProcessor{started: make(chan struct{}, 4), release: make(chan struct{}, 4)}
	pool, _ := NewPool(processor, digestTestLogger(), PoolConfig{Workers: 2, QueueCapacity: 4})
	for userID := uint64(1); userID <= 4; userID++ {
		if err := pool.Submit(t.Context(), Job{UserID: userID, LocalDate: "2026-09-08"}); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { pool.Run(ctx); close(done) }()
	for index := 0; index < 2; index++ {
		select {
		case <-processor.started:
		case <-time.After(time.Second):
			t.Fatal("mail workers did not start")
		}
	}
	processor.mu.Lock()
	maxActive := processor.maxActive
	processor.mu.Unlock()
	if maxActive != 2 {
		t.Fatalf("expected two active mail workers, got %d", maxActive)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("mail pool did not stop")
	}
}
