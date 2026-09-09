package main

import (
	"context"
	"io"
	"log/slog"
	"signalwatch/internal/digest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeartbeatContinuesWhileDigestSubmissionIsBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := make(chan struct{})
	beats := make(chan struct{}, 10)
	done := make(chan struct{})
	var calls atomic.Int32
	go func() {
		defer close(done)
		run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), 5*time.Millisecond, time.Millisecond,
			func(context.Context) {
				select {
				case beats <- struct{}{}:
				default:
				}
			},
			func(ctx context.Context) (digest.ScheduleResult, error) {
				if calls.Add(1) == 1 {
					close(started)
				}
				<-ctx.Done()
				return digest.ScheduleResult{}, ctx.Err()
			})
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("digest did not start")
	}
	for i := 0; i < 3; i++ {
		select {
		case <-beats:
		case <-ctx.Done():
			t.Fatal("digest backpressure blocked heartbeat")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("digest cycles must not overlap")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}
