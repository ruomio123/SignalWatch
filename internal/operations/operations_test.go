package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"signalwatch/internal/platform/httpx"
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

func TestWorkerClassificationAndDegradedRules(t *testing.T) {
	now := time.Date(2026, time.September, 9, 8, 0, 0, 0, time.UTC)
	workers := classifyWorkers([]WorkerSnapshot{
		{InstanceID: "online", State: "running", HeartbeatSeconds: 5, LastHeartbeatAt: now.Add(-29 * time.Second)},
		{InstanceID: "stale", State: "running", HeartbeatSeconds: 10, LastHeartbeatAt: now.Add(-31 * time.Second)},
		{InstanceID: "stopped", State: "stopped", HeartbeatSeconds: 10, LastHeartbeatAt: now},
	}, now)
	states := map[string]string{}
	for _, worker := range workers {
		states[worker.InstanceID] = worker.OnlineState
	}
	if states["online"] != "online" || states["stale"] != "stale" || states["stopped"] != "stopped" {
		t.Fatalf("unexpected online states: %v", states)
	}

	healthyWorker := []WorkerSnapshot{{InstanceID: "worker", OnlineState: "online", MatcherQueue: QueueSnapshot{Depth: 7, Capacity: 10}}}
	healthyTasks := []TaskSnapshot{{Task: "collector", InstanceID: "worker"}, {Task: "matcher", InstanceID: "worker"}, {Task: "digest", InstanceID: "worker"}, {Task: "mail", InstanceID: "worker"}}
	if isDegraded(healthyWorker, healthyTasks) {
		t.Fatal("expected complete low-pressure state to be healthy")
	}
	healthyWorker[0].MatcherQueue.Depth = 8
	if !isDegraded(healthyWorker, healthyTasks) {
		t.Fatal("expected 80 percent queue utilization to be degraded")
	}
	if !isDegraded([]WorkerSnapshot{{InstanceID: "worker", OnlineState: "online"}}, healthyTasks[:3]) {
		t.Fatal("expected missing task state to be degraded")
	}
}

func TestLatestDailyBoundaryUsesNewYorkDST(t *testing.T) {
	beforeDaily := time.Date(2026, time.March, 9, 4, 0, 0, 0, time.UTC)
	due, err := latestDailyBoundary(beforeDaily, "00:30")
	if err != nil {
		t.Fatalf("calculate boundary: %v", err)
	}
	want := time.Date(2026, time.March, 8, 5, 30, 0, 0, time.UTC)
	if !due.Equal(want) {
		t.Fatalf("expected DST-aware boundary %s, got %s", want, due)
	}

	afterDaily := time.Date(2026, time.March, 9, 6, 0, 0, 0, time.UTC)
	due, err = latestDailyBoundary(afterDaily, "00:30")
	want = time.Date(2026, time.March, 9, 4, 30, 0, 0, time.UTC)
	if err != nil || !due.Equal(want) {
		t.Fatalf("expected current DST boundary %s, got %s (%v)", want, due, err)
	}
}

func TestSafeFailureAndAuditDoNotExposeAuthorization(t *testing.T) {
	failure := SafeFailure("smtp", time.Now())
	if failure.Summary != "task failed during smtp" {
		t.Fatalf("unexpected safe summary %q", failure.Summary)
	}

	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware(), func(c *gin.Context) {
		httpx.SetCurrentUserID(c, 88)
		c.Next()
	}, AuditMiddleware(logger))
	router.GET("/api/v1/ops/status", func(c *gin.Context) { c.Status(http.StatusOK) })
	request := httptest.NewRequest(http.MethodGet, "/api/v1/ops/status", nil)
	request.Header.Set("Authorization", "Bearer SECRET_TOKEN_MUST_NOT_LEAK")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}
	logged := logs.String()
	for _, required := range []string{"operator_api_access", "operator_user_id", "request_id", "duration_ms"} {
		if !strings.Contains(logged, required) {
			t.Fatalf("audit log is missing %q: %s", required, logged)
		}
	}
	if strings.Contains(logged, "SECRET_TOKEN_MUST_NOT_LEAK") || strings.Contains(logged, "Bearer") {
		t.Fatalf("audit log leaked bearer token: %s", logged)
	}
}

func TestStatusHandlerReturnsComponentOutageAsHTTP200(t *testing.T) {
	now := time.Date(2026, time.September, 9, 8, 0, 0, 0, time.UTC)
	service, err := NewService(
		&gorm.DB{}, &memorySnapshotStore{},
		func(context.Context) error { return errors.New("mysql unavailable") },
		func(context.Context) error { return nil },
		func() time.Time { return now }, "00:30",
	)
	if err != nil {
		t.Fatalf("create operations service: %v", err)
	}
	handler, err := NewHandler(service, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("create operations handler: %v", err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/ops/status", handler.Status)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/ops/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected component outage status 200, got %d: %s", response.Code, response.Body.String())
	}
	var body StatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if body.Status != "unavailable" || body.Dependencies["mysql"].Status != "unavailable" {
		t.Fatalf("expected unavailable component in body: %+v", body)
	}
}
