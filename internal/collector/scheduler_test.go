package collector

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

type synchronizerStub struct {
	requests []SyncRequest
	results  []SyncResult
	errors   []error
}

func (stub *synchronizerStub) Sync(_ context.Context, request SyncRequest) (SyncResult, error) {
	stub.requests = append(stub.requests, request)
	index := len(stub.requests) - 1
	if index >= len(stub.results) {
		index = len(stub.results) - 1
	}
	var err error
	if index < len(stub.errors) {
		err = stub.errors[index]
	}
	return stub.results[index], err
}

func TestSchedulerUsesNewYorkCalendarAcrossDST(t *testing.T) {
	scheduler := newTestScheduler(t, &synchronizerStub{}, time.Now)
	for _, test := range []struct {
		name       string
		now        time.Time
		latestWant time.Time
		nextWant   time.Time
	}{
		{
			name:       "spring daylight time",
			now:        time.Date(2026, 3, 9, 5, 0, 0, 0, time.UTC),
			latestWant: time.Date(2026, 3, 9, 4, 30, 0, 0, time.UTC),
			nextWant:   time.Date(2026, 3, 10, 4, 30, 0, 0, time.UTC),
		},
		{
			name:       "autumn standard time",
			now:        time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC),
			latestWant: time.Date(2026, 11, 2, 5, 30, 0, 0, time.UTC),
			nextWant:   time.Date(2026, 11, 3, 5, 30, 0, 0, time.UTC),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := scheduler.latestDue(test.now).UTC(); !got.Equal(test.latestWant) {
				t.Fatalf("latest due=%s want=%s", got, test.latestWant)
			}
			if got := scheduler.nextDue(test.now).UTC(); !got.Equal(test.nextWant) {
				t.Fatalf("next due=%s want=%s", got, test.nextWant)
			}
		})
	}
}

func TestSchedulerRetriesSameTriggerUntilCheckpointIsCurrent(t *testing.T) {
	syncer := &synchronizerStub{results: []SyncResult{{Acquired: false}, {Acquired: true, AllCurrent: true}}}
	scheduler, err := NewScheduler(syncer, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now, SchedulerConfig{
		DailySyncTime: "00:30", RetryInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	due := time.Date(2026, 9, 9, 4, 30, 0, 0, time.UTC)
	if !scheduler.retryUntilCurrent(context.Background(), TriggerDaily, due) {
		t.Fatal("scheduler stopped before sync became current")
	}
	if len(syncer.requests) != 2 || syncer.requests[0].Trigger != TriggerDaily ||
		syncer.requests[1].Trigger != TriggerDaily || !syncer.requests[0].DueAt.Equal(due) {
		t.Fatalf("retry changed sync mode or due boundary: %+v", syncer.requests)
	}
}

func TestSchedulerReportsLockRetryAndCancellation(t *testing.T) {
	syncer := &synchronizerStub{results: []SyncResult{{Acquired: false}, {Acquired: true, AllCurrent: true}}}
	scheduler, err := NewScheduler(syncer, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now, SchedulerConfig{
		DailySyncTime: "00:30", RetryInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	observer := &observerStub{}
	scheduler.SetObserver(observer)
	due := time.Date(2026, 9, 9, 4, 30, 0, 0, time.UTC)
	if !scheduler.retryUntilCurrent(context.Background(), TriggerDaily, due) {
		t.Fatal("scheduler stopped unexpectedly")
	}
	if len(observer.schedules) != 4 || observer.schedules[1].State != "retry_wait" ||
		observer.schedules[1].FailStage != "lock_wait" || observer.schedules[3].State != "succeeded" {
		t.Fatalf("unexpected scheduler observations: %+v", observer.schedules)
	}

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := &synchronizerStub{results: []SyncResult{{}}, errors: []error{context.Canceled}}
	cancelledScheduler, err := NewScheduler(cancelled, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now, SchedulerConfig{
		DailySyncTime: "00:30", RetryInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("new cancelled scheduler: %v", err)
	}
	cancelObserver := &observerStub{}
	cancelledScheduler.SetObserver(cancelObserver)
	if cancelledScheduler.retryUntilCurrent(cancelledContext, TriggerStartup, due) {
		t.Fatal("cancelled scheduler should stop")
	}
	if len(cancelObserver.schedules) != 2 || cancelObserver.schedules[1].State != "cancelled" {
		t.Fatalf("expected terminal cancellation status: %+v", cancelObserver.schedules)
	}
}

func newTestScheduler(t *testing.T, syncer Synchronizer, now func() time.Time) *Scheduler {
	t.Helper()
	scheduler, err := NewScheduler(syncer, slog.New(slog.NewTextHandler(io.Discard, nil)), now, SchedulerConfig{
		DailySyncTime: "00:30", RetryInterval: time.Minute,
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	return scheduler
}
