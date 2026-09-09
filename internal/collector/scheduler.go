package collector

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

const arXivTimezone = "America/New_York"

type Synchronizer interface {
	Sync(ctx context.Context, request SyncRequest) (SyncResult, error)
}

type SchedulerConfig struct {
	DailySyncTime string
	RetryInterval time.Duration
}

type Scheduler struct {
	synchronizer Synchronizer
	logger       *slog.Logger
	now          func() time.Time
	location     *time.Location
	hour         int
	minute       int
	retry        time.Duration
	observer     Observer
}

func (scheduler *Scheduler) SetObserver(observer Observer) { scheduler.observer = observer }

func NewScheduler(
	synchronizer Synchronizer,
	logger *slog.Logger,
	now func() time.Time,
	config SchedulerConfig,
) (*Scheduler, error) {
	if synchronizer == nil || logger == nil || now == nil || config.RetryInterval <= 0 {
		return nil, errors.New("invalid collector scheduler configuration")
	}
	parts := strings.Split(config.DailySyncTime, ":")
	if len(parts) != 2 {
		return nil, errors.New("invalid collector daily sync time")
	}
	hour, hourErr := strconv.Atoi(parts[0])
	minute, minuteErr := strconv.Atoi(parts[1])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return nil, errors.New("invalid collector daily sync time")
	}
	location, err := time.LoadLocation(arXivTimezone)
	if err != nil {
		return nil, fmt.Errorf("load arxiv timezone: %w", err)
	}
	return &Scheduler{
		synchronizer: synchronizer, logger: logger, now: now, location: location,
		hour: hour, minute: minute, retry: config.RetryInterval,
	}, nil
}

func (scheduler *Scheduler) Run(ctx context.Context) {
	latest := scheduler.latestDue(scheduler.now())
	if !scheduler.retryUntilCurrent(ctx, TriggerStartup, latest) {
		return
	}
	for {
		due := scheduler.nextDue(scheduler.now())
		if !waitUntil(ctx, scheduler.now, due) {
			return
		}
		if !scheduler.retryUntilCurrent(ctx, TriggerDaily, due) {
			return
		}
	}
}

func (scheduler *Scheduler) retryUntilCurrent(ctx context.Context, trigger Trigger, due time.Time) bool {
	for {
		startedAt := scheduler.now().UTC()
		attemptID := rand.Text()
		request := SyncRequest{
			Trigger: trigger, DueAt: due.UTC(), PreviousDueAt: due.AddDate(0, 0, -1).UTC(),
			AttemptID: attemptID,
		}
		scheduler.observe(ctx, ScheduleObservation{
			State: "running", Trigger: trigger, AttemptID: attemptID, DueAt: due.UTC(),
			StartedAt: startedAt, UpdatedAt: startedAt,
		})
		result, err := scheduler.synchronizer.Sync(ctx, request)
		if ctx.Err() != nil {
			now := scheduler.now().UTC()
			statusContext, cancelStatus := context.WithTimeout(context.Background(), 2*time.Second)
			scheduler.observe(statusContext, ScheduleObservation{
				State: "cancelled", Trigger: trigger, AttemptID: attemptID, DueAt: due.UTC(),
				StartedAt: startedAt, UpdatedAt: now, FailStage: "cancelled",
				Metrics: map[string]int{"sources": result.Sources, "synced": result.Synced, "current": result.Current},
			})
			cancelStatus()
			return false
		}
		if err == nil && result.AllCurrent {
			now := scheduler.now().UTC()
			scheduler.observe(ctx, ScheduleObservation{
				State: "succeeded", Trigger: trigger, AttemptID: attemptID, DueAt: due.UTC(),
				StartedAt: startedAt, UpdatedAt: now,
				Metrics: map[string]int{"sources": result.Sources, "synced": result.Synced, "current": result.Current},
			})
			return true
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			scheduler.logger.Error(
				"arxiv sync attempt failed", "module", "collector", "event", "collector_schedule",
				"task", "collector", "attempt_id", attemptID, "trigger", trigger,
				"state", "retry_wait", "error_stage", "sync", "due_at", due.UTC(),
				"duration_ms", scheduler.now().UTC().Sub(startedAt).Milliseconds(),
				"retry_in", scheduler.retry, "error", err,
			)
		}
		now := scheduler.now().UTC()
		nextRetry := now.Add(scheduler.retry)
		stage := "lock_wait"
		if err != nil {
			stage = "sync"
		}
		scheduler.observe(ctx, ScheduleObservation{
			State: "retry_wait", Trigger: trigger, AttemptID: attemptID, DueAt: due.UTC(),
			StartedAt: startedAt, UpdatedAt: now, NextRetryAt: &nextRetry, FailStage: stage,
			Metrics: map[string]int{"sources": result.Sources, "synced": result.Synced, "current": result.Current},
		})
		if !waitDuration(ctx, scheduler.retry) {
			return false
		}
	}
}

func (scheduler *Scheduler) observe(ctx context.Context, observation ScheduleObservation) {
	if scheduler.observer == nil {
		return
	}
	if err := scheduler.observer.ObserveSchedule(ctx, observation); err != nil {
		scheduler.logger.Warn("collector schedule observation failed", "module", "collector",
			"event", "collector_schedule_status_write_failed", "error", err)
	}
}

func (scheduler *Scheduler) latestDue(now time.Time) time.Time {
	local := now.In(scheduler.location)
	due := time.Date(local.Year(), local.Month(), local.Day(), scheduler.hour, scheduler.minute, 0, 0, scheduler.location)
	if local.Before(due) {
		due = due.AddDate(0, 0, -1)
	}
	return due
}

func (scheduler *Scheduler) nextDue(now time.Time) time.Time {
	local := now.In(scheduler.location)
	due := time.Date(local.Year(), local.Month(), local.Day(), scheduler.hour, scheduler.minute, 0, 0, scheduler.location)
	if !local.Before(due) {
		due = due.AddDate(0, 0, 1)
	}
	return due
}

func waitUntil(ctx context.Context, now func() time.Time, due time.Time) bool {
	delay := due.Sub(now())
	if delay < 0 {
		delay = 0
	}
	return waitDuration(ctx, delay)
}

func waitDuration(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
