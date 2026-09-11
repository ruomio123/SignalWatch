package digest

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

type scheduleRepositoryStub struct {
	schedules []Schedule
	err       error
}

func (stub *scheduleRepositoryStub) ListActiveSchedules(context.Context) ([]Schedule, error) {
	return stub.schedules, stub.err
}

type submitterStub struct{ jobs []Job }

func (stub *submitterStub) Submit(_ context.Context, job Job) error {
	stub.jobs = append(stub.jobs, job)
	return nil
}

func TestSchedulerUsesEachUsersLocalDateAndRetriesAfterDueTime(t *testing.T) {
	repository := &scheduleRepositoryStub{schedules: []Schedule{
		{SubscriptionID: 3, UserID: 1, Timezone: "UTC", DigestTime: "08:00:00"},
		{SubscriptionID: 4, UserID: 2, Timezone: "Asia/Shanghai", DigestTime: "16:30:00"},
		{SubscriptionID: 5, UserID: 3, Timezone: "America/Los_Angeles", DigestTime: "02:00:00"},
		{SubscriptionID: 6, UserID: 4, Timezone: "invalid/zone", DigestTime: "08:00:00"},
	}}
	submitter := &submitterStub{}
	now := time.Date(2026, 9, 8, 8, 15, 0, 0, time.UTC)
	scheduler, err := NewScheduler(
		repository, submitter, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	result, err := scheduler.Run(t.Context())
	if err != nil {
		t.Fatalf("run scheduler: %v", err)
	}
	if result.Users != 4 || result.Due != 1 || result.Submitted != 1 || result.Invalid != 1 {
		t.Fatalf("unexpected schedule result: %+v", result)
	}
	if len(submitter.jobs) != 1 || submitter.jobs[0] != (Job{SubscriptionID: 3, UserID: 1, LocalDate: "2026-09-08"}) {
		t.Fatalf("unexpected jobs: %+v", submitter.jobs)
	}

	// A pass later the same day submits again. The processor's Redis completion
	// marker decides whether this is a retry or a cheap no-op.
	now = now.Add(6 * time.Hour)
	result, err = scheduler.Run(t.Context())
	if err != nil {
		t.Fatalf("rerun scheduler: %v", err)
	}
	if result.Due != 3 || result.Submitted != 3 {
		t.Fatalf("expected all valid schedules to be due later that day: %+v", result)
	}
}

func TestDueJobUsesLocalCalendarDateAcrossUTCDateBoundary(t *testing.T) {
	job, due, err := dueJob(Schedule{
		SubscriptionID: 7, UserID: 7, Timezone: "Asia/Shanghai", DigestTime: "00:05:00",
	}, time.Date(2026, 9, 7, 16, 6, 0, 0, time.UTC))
	if err != nil || !due {
		t.Fatalf("expected due local schedule: job=%+v due=%v err=%v", job, due, err)
	}
	if job.LocalDate != "2026-09-08" {
		t.Fatalf("expected Shanghai local date, got %s", job.LocalDate)
	}
}
