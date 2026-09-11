package ai

import (
	"context"
	"errors"
	"signalwatch/internal/subscription"
	"signalwatch/internal/user"
	"time"
)

var ErrTaskNotFound = errors.New("AI task not found")

type UsageTotals struct {
	Requests                                  int
	InputTokens, OutputTokens, EstimatedCalls int64
}
type TaskStore interface {
	Ensure(context.Context, *Task) error
	Find(context.Context, Task) (Task, error)
	Retry(context.Context, Task, time.Time) error
	Pending(context.Context, string, string, time.Time, int) ([]Task, error)
	Claim(context.Context, Task, time.Time) (Task, bool, error)
	MarkAttempt(context.Context, Task, time.Time) (bool, error)
	Finish(context.Context, Task, []byte, string, time.Time, bool, time.Time) error
	Requeue(context.Context, Task, time.Time, time.Time, string) error
	ReadyDigest(context.Context, Task) ([]Task, error)
	PaperEligible(context.Context, Task) (bool, error)
	DemandUsers(context.Context, uint64) ([]user.User, error)
	DemandSubscriptions(context.Context, uint64) ([]subscription.Subscription, error)
	UsageTotals(context.Context, time.Time) (UsageTotals, error)
	Cleanup(context.Context, time.Time) error
}
