package digest

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrLeaseLost = errors.New("digest lease lost")

type Delivery struct {
	ID uint64
	Job
	State, LeaseOwner string
	SnapshotJSON      json.RawMessage
	Attempts          int
}
type Snapshot struct {
	Message  Message
	PaperIDs []uint64
}
type CompletionReader interface {
	IsComplete(context.Context, Job) (bool, error)
}
type DeliveryStore interface {
	Claim(context.Context, Job) (Delivery, bool, error)
	Freeze(context.Context, Delivery, Snapshot) error
	Finish(context.Context, Delivery, Snapshot, time.Time) (int64, error)
	Fail(context.Context, Delivery) error
	Cancel(context.Context, Delivery) error
}
