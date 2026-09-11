package digest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type ScheduleRepository interface {
	ListActiveSchedules(ctx context.Context) ([]Schedule, error)
}

type JobSubmitter interface {
	Submit(ctx context.Context, job Job) error
}

type Scheduler struct {
	repository ScheduleRepository
	submitter  JobSubmitter
	logger     *slog.Logger
	now        func() time.Time
}

type ScheduleResult struct {
	Users         int
	Subscriptions int
	Due           int
	Submitted     int
	Invalid       int
}

func NewScheduler(
	repository ScheduleRepository,
	submitter JobSubmitter,
	logger *slog.Logger,
	now func() time.Time,
) (*Scheduler, error) {
	if repository == nil || submitter == nil || logger == nil || now == nil {
		return nil, errors.New("invalid digest scheduler configuration")
	}
	return &Scheduler{repository: repository, submitter: submitter, logger: logger, now: now}, nil
}

// Run submits every enabled subscription whose local digest time has passed today.
// MySQL owns the per-day identity; due retries retain their original date and
// snapshot even when the next local day has begun.
func (scheduler *Scheduler) Run(ctx context.Context) (ScheduleResult, error) {
	schedules, err := scheduler.repository.ListActiveSchedules(ctx)
	if err != nil {
		return ScheduleResult{}, fmt.Errorf("list digest schedules: %w", err)
	}
	result := ScheduleResult{Subscriptions: len(schedules)}
	users := map[uint64]bool{}
	for _, schedule := range schedules {
		users[schedule.UserID] = true
	}
	result.Users = len(users)
	now := scheduler.now()
	for _, schedule := range schedules {
		job, due, err := dueJob(schedule, now)
		if err != nil {
			result.Invalid++
			scheduler.logger.Error(
				"invalid digest schedule", "module", "digest", "user_id", schedule.UserID, "error", err,
			)
			continue
		}
		if !due {
			continue
		}
		result.Due++
		if err := scheduler.submitter.Submit(ctx, job); err != nil {
			return result, fmt.Errorf("submit digest for user %d: %w", schedule.UserID, err)
		}
		result.Submitted++
	}
	return result, nil
}

func dueJob(schedule Schedule, now time.Time) (Job, bool, error) {
	if schedule.UserID == 0 || schedule.SubscriptionID == 0 {
		return Job{}, false, errors.New("invalid user id")
	}
	if schedule.LocalDate != "" {
		job := Job{UserID: schedule.UserID, SubscriptionID: schedule.SubscriptionID, LocalDate: schedule.LocalDate}
		return job, true, validateJob(job)
	}
	location, err := time.LoadLocation(strings.TrimSpace(schedule.Timezone))
	if err != nil {
		return Job{}, false, fmt.Errorf("load timezone: %w", err)
	}
	digestClock, err := time.Parse("15:04:05", schedule.DigestTime)
	if err != nil {
		return Job{}, false, fmt.Errorf("parse digest time: %w", err)
	}
	localNow := now.In(location)
	due := localNow.Hour() > digestClock.Hour() ||
		localNow.Hour() == digestClock.Hour() && localNow.Minute() >= digestClock.Minute()
	return Job{SubscriptionID: schedule.SubscriptionID, UserID: schedule.UserID, LocalDate: localNow.Format(localDateLayout)}, due, nil
}
