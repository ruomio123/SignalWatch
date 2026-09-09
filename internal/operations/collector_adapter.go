package operations

import (
	"context"
	"time"

	"signalwatch/internal/collector"
)

func (reporter *Reporter) ObserveSource(ctx context.Context, observation collector.SourceObservation) error {
	snapshot := SourceAttempt{
		SourceID: observation.SourceID, State: observation.State, Mode: string(observation.Mode),
		AttemptID: observation.AttemptID, WindowFrom: observation.WindowFrom, WindowTo: observation.WindowTo,
		StartedAt: observation.StartedAt, CompletedAt: observation.CompletedAt, Metrics: observation.Metrics,
	}
	if observation.State == "succeeded" && observation.CompletedAt != nil {
		snapshot.LastSuccessAt = observation.CompletedAt
	}
	if observation.State == "failed" || observation.State == "cancelled" {
		at := reporter.now().UTC()
		if observation.CompletedAt != nil {
			at = observation.CompletedAt.UTC()
		}
		snapshot.Failure = SafeFailure(observation.FailStage, at)
	}
	return reporter.store.SaveSourceAttempt(ctx, snapshot)
}

func (reporter *Reporter) ObserveSchedule(ctx context.Context, observation collector.ScheduleObservation) error {
	snapshot := TaskSnapshot{
		Task: "collector", InstanceID: reporter.instance, State: observation.State,
		Trigger: string(observation.Trigger), AttemptID: observation.AttemptID,
		DueAt: utcValuePointer(observation.DueAt), StartedAt: utcValuePointer(observation.StartedAt),
		UpdatedAt: observation.UpdatedAt.UTC(), NextRetryAt: observation.NextRetryAt, Metrics: observation.Metrics,
	}
	if observation.State == "succeeded" {
		snapshot.LastSuccessAt = utcValuePointer(observation.UpdatedAt)
	}
	if observation.State == "failed" || observation.State == "retry_wait" || observation.State == "cancelled" {
		snapshot.Failure = SafeFailure(observation.FailStage, observation.UpdatedAt)
	}
	return reporter.saveTask(ctx, snapshot)
}

func utcValuePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}
