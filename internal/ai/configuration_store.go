package ai

import (
	"context"
	"time"
)

// Mutation receives a locked current configuration and a never-reused revision.
// Returning an error rolls back both the envelope and its revision allocation.
type ConfigurationStore interface {
	Read(context.Context, uint64) (Configuration, error)
	Mutate(context.Context, uint64, *uint64, func(Configuration, uint64) (Configuration, error)) (Configuration, error)
	Delete(context.Context, uint64, uint64, time.Time) error
	MarkTested(context.Context, uint64, uint64, time.Time) error
	MarkInvalid(context.Context, uint64, uint64) error
	MarkUsed(context.Context, uint64, uint64, time.Time) error
}
