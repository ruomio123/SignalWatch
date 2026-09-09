package digest

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Sender interface {
	Send(ctx context.Context, message Message) error
}

type Processor struct {
	repository  Repository
	coordinator Coordinator
	sender      Sender
	now         func() time.Time
}

func NewProcessor(
	repository Repository,
	coordinator Coordinator,
	sender Sender,
	now func() time.Time,
) (*Processor, error) {
	if repository == nil || coordinator == nil || sender == nil || now == nil {
		return nil, errors.New("invalid digest processor configuration")
	}
	return &Processor{repository: repository, coordinator: coordinator, sender: sender, now: now}, nil
}

func (processor *Processor) Process(ctx context.Context, job Job) (result ProcessResult, err error) {
	if err := validateJob(job); err != nil {
		return ProcessResult{}, err
	}
	release, acquired, err := processor.coordinator.Acquire(ctx, job)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("acquire digest lock: %w", err)
	}
	if !acquired {
		return ProcessResult{Locked: true}, nil
	}
	defer func() {
		releaseContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if releaseErr := release(releaseContext); releaseErr != nil && err == nil {
			err = fmt.Errorf("release digest lock: %w", releaseErr)
		}
	}()

	complete, err := processor.coordinator.IsComplete(ctx, job)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("check digest completion: %w", err)
	}
	if complete {
		return ProcessResult{AlreadyComplete: true}, nil
	}
	user, err := processor.repository.FindActiveUser(ctx, job.UserID)
	if err != nil {
		return ProcessResult{}, err
	}
	location, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("load digest user timezone: %w", err)
	}
	if processor.now().In(location).Format(localDateLayout) != job.LocalDate {
		return ProcessResult{Stale: true}, nil
	}

	items, err := processor.repository.ListCandidates(
		ctx, user.ID, int(user.MaxItemsPerDigest),
	)
	if err != nil {
		return ProcessResult{}, err
	}
	if len(items) == 0 {
		if err := processor.coordinator.MarkComplete(ctx, job); err != nil {
			return ProcessResult{}, fmt.Errorf("mark empty digest complete: %w", err)
		}
		return ProcessResult{Empty: true}, nil
	}

	message, err := RenderMessage(user, job, items)
	if err != nil {
		return ProcessResult{}, err
	}
	if err := processor.sender.Send(ctx, message); err != nil {
		return ProcessResult{}, fmt.Errorf("send digest: %w", err)
	}
	paperIDs := make([]uint64, len(items))
	for index, item := range items {
		paperIDs[index] = item.PaperID
	}
	marked, err := processor.repository.MarkDelivered(
		ctx, user.ID, paperIDs, processor.now().UTC(),
	)
	if err != nil {
		return ProcessResult{}, err
	}
	if err := processor.coordinator.MarkComplete(ctx, job); err != nil {
		return ProcessResult{}, fmt.Errorf("mark sent digest complete: %w", err)
	}
	return ProcessResult{Items: len(items), MarkedRelations: marked}, nil
}
