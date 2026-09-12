package operations

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type memorySnapshotStore struct {
	workers []WorkerSnapshot
	tasks   []TaskSnapshot
	sources []SourceAttempt
	err     error
}

func (store *memorySnapshotStore) SaveWorker(_ context.Context, snapshot WorkerSnapshot) error {
	store.workers = append(store.workers, snapshot)
	return store.err
}

func (store *memorySnapshotStore) SaveTask(_ context.Context, snapshot TaskSnapshot) error {
	store.tasks = append(store.tasks, snapshot)
	return store.err
}

func (store *memorySnapshotStore) SaveSourceAttempt(_ context.Context, snapshot SourceAttempt) error {
	store.sources = append(store.sources, snapshot)
	return store.err
}

func (*memorySnapshotStore) ListWorkers(context.Context, time.Time) ([]WorkerSnapshot, error) {
	return nil, nil
}

func (*memorySnapshotStore) ListTasks(context.Context, time.Time) ([]TaskSnapshot, error) {
	return nil, nil
}

func (*memorySnapshotStore) GetSourceAttempt(context.Context, uint64) (*SourceAttempt, error) {
	return nil, nil
}

func TestReporterWritesSafeBestEffortSnapshots(t *testing.T) {
	now := time.Date(2026, time.September, 9, 8, 0, 0, 0, time.UTC)
	store := &memorySnapshotStore{err: errors.New("redis password=must-not-be-returned")}
	var logs bytes.Buffer
	reporter, err := NewReporter(store, slog.New(slog.NewJSONHandler(&logs, nil)), func() time.Time { return now }, 10*time.Second)
	if err != nil {
		t.Fatalf("create reporter: %v", err)
	}

	// Best-effort status failures must not surface to Worker task execution.
	reporter.Heartbeat(context.Background(), QueueSnapshot{Depth: 1, Capacity: 10}, QueueSnapshot{})
	reporter.RecordTask(context.Background(), TaskSnapshot{Task: "digest", State: "failed"})
	if len(store.workers) != 1 || len(store.tasks) != 1 {
		t.Fatalf("expected attempted snapshots, got workers=%d tasks=%d", len(store.workers), len(store.tasks))
	}
	if store.workers[0].HeartbeatSeconds != 10 || store.workers[0].State != "running" {
		t.Fatalf("unexpected worker snapshot: %+v", store.workers[0])
	}
	if store.tasks[0].InstanceID != reporter.InstanceID() || !store.tasks[0].UpdatedAt.Equal(now) {
		t.Fatalf("unexpected task snapshot: %+v", store.tasks[0])
	}
	if !strings.Contains(logs.String(), "task_status_write_failed") {
		t.Fatalf("expected structured warning, got %s", logs.String())
	}
	store.err = nil
	reporter.RecordTask(context.Background(), TaskSnapshot{Task: "digest", State: "succeeded", LastSuccessAt: &now})
	reporter.RecordTask(context.Background(), TaskSnapshot{Task: "digest", State: "failed"})
	latest := store.tasks[len(store.tasks)-1]
	if latest.LastSuccessAt == nil || !latest.LastSuccessAt.Equal(now) {
		t.Fatalf("expected a later failure to retain last success, got %+v", latest)
	}
}

func TestSafeFailure(t *testing.T) {
	if got := SafeFailure("smtp", time.Now()).Summary; got != "task failed during smtp" {
		t.Fatalf("unexpected summary: %s", got)
	}
}
