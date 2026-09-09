package operations

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

type Reporter struct {
	store       SnapshotStore
	logger      *slog.Logger
	now         func() time.Time
	instance    string
	startedAt   time.Time
	heartbeat   time.Duration
	mu          sync.Mutex
	lastSuccess map[string]time.Time
}

func NewReporter(store SnapshotStore, logger *slog.Logger, now func() time.Time, heartbeat time.Duration) (*Reporter, error) {
	if store == nil || logger == nil || now == nil || heartbeat <= 0 {
		return nil, fmt.Errorf("invalid operations reporter configuration")
	}
	hostname, _ := os.Hostname()
	instance := strings.NewReplacer(":", "-", " ", "-").Replace(hostname)
	instance = fmt.Sprintf("%s-%d-%s", instance, os.Getpid(), strings.ToLower(rand.Text()))
	started := now().UTC()
	return &Reporter{
		store: store, logger: logger, now: now, instance: instance, startedAt: started,
		heartbeat: heartbeat, lastSuccess: make(map[string]time.Time),
	}, nil
}

func (reporter *Reporter) InstanceID() string { return reporter.instance }

func (reporter *Reporter) Heartbeat(ctx context.Context, matcher, mail QueueSnapshot) {
	reporter.saveWorker(ctx, "running", matcher, mail)
}

func (reporter *Reporter) Stop(ctx context.Context, matcher, mail QueueSnapshot) {
	reporter.saveWorker(ctx, "stopped", matcher, mail)
}

func (reporter *Reporter) saveWorker(ctx context.Context, state string, matcher, mail QueueSnapshot) {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	snapshot := WorkerSnapshot{
		InstanceID: reporter.instance, State: state, StartedAt: reporter.startedAt,
		LastHeartbeatAt: reporter.now().UTC(), HeartbeatSeconds: int64(reporter.heartbeat / time.Second),
		MatcherQueue: matcher, MailQueue: mail,
	}
	if err := reporter.store.SaveWorker(ctx, snapshot); err != nil {
		reporter.warn("worker_status_write_failed", "worker", err)
	}
}

func (reporter *Reporter) RecordTask(ctx context.Context, snapshot TaskSnapshot) {
	snapshot.InstanceID = reporter.instance
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = reporter.now().UTC()
	}
	if err := reporter.saveTask(ctx, snapshot); err != nil {
		reporter.warn("task_status_write_failed", snapshot.Task, err)
	}
}

func (reporter *Reporter) saveTask(ctx context.Context, snapshot TaskSnapshot) error {
	reporter.mu.Lock()
	if snapshot.LastSuccessAt != nil {
		reporter.lastSuccess[snapshot.Task] = snapshot.LastSuccessAt.UTC()
	} else if last, ok := reporter.lastSuccess[snapshot.Task]; ok {
		value := last
		snapshot.LastSuccessAt = &value
	}
	reporter.mu.Unlock()
	return reporter.store.SaveTask(ctx, snapshot)
}

func (reporter *Reporter) RecordSource(ctx context.Context, snapshot SourceAttempt) {
	if err := reporter.store.SaveSourceAttempt(ctx, snapshot); err != nil {
		reporter.warn("source_status_write_failed", "collector", err)
	}
}

func (reporter *Reporter) warn(event, task string, err error) {
	reporter.logger.Warn("operational status write failed", "module", "operations", "event", event,
		"worker_instance_id", reporter.instance, "task", task, "error", err)
}

func SafeFailure(stage string, at time.Time) *Failure {
	if stage == "" {
		stage = "unknown"
	}
	return &Failure{Stage: stage, Summary: "task failed during " + stage, At: at.UTC()}
}
