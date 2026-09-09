package collector

import (
	"context"
	"time"
)

type SourceObservation struct {
	SourceID    uint64
	State       string
	Mode        SyncMode
	AttemptID   string
	WindowFrom  *time.Time
	WindowTo    *time.Time
	StartedAt   time.Time
	CompletedAt *time.Time
	Metrics     map[string]int
	FailStage   string
}

type ScheduleObservation struct {
	State       string
	Trigger     Trigger
	AttemptID   string
	DueAt       time.Time
	StartedAt   time.Time
	UpdatedAt   time.Time
	NextRetryAt *time.Time
	Metrics     map[string]int
	FailStage   string
}

type Observer interface {
	ObserveSource(ctx context.Context, observation SourceObservation) error
	ObserveSchedule(ctx context.Context, observation ScheduleObservation) error
}
