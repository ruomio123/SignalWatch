package digest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"signalwatch/internal/insight"
	"time"
)

type Sender interface {
	Send(ctx context.Context, message Message) error
}

type Enricher interface {
	Lookup(context.Context, User, Job, []Item) (insight.Enrichment, error)
}

type Processor struct {
	publicBaseURL string
	enricher      Enricher
	repository    CandidateRepository
	deliveries    DeliveryStore
	sender        Sender
	now           func() time.Time
}

type Options struct {
	PublicBaseURL string
	Enricher      Enricher
}

func NewProcessor(
	repository CandidateRepository,
	deliveries DeliveryStore,
	sender Sender,
	now func() time.Time,
	options ...Options,
) (*Processor, error) {
	if repository == nil || deliveries == nil || sender == nil || now == nil {
		return nil, errors.New("invalid digest processor configuration")
	}
	p := &Processor{repository: repository, deliveries: deliveries, sender: sender, now: now}
	if len(options) > 0 {
		p.publicBaseURL = options[0].PublicBaseURL
		p.enricher = options[0].Enricher
	}
	return p, nil
}

func (processor *Processor) Process(ctx context.Context, job Job) (result ProcessResult, err error) {
	if err := validateJob(job); err != nil {
		return ProcessResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	delivery, acquired, err := processor.deliveries.Claim(ctx, job)
	if err != nil {
		return ProcessResult{}, err
	}
	if delivery.State == "sent" || delivery.State == "empty" || delivery.State == "cancelled" {
		return ProcessResult{AlreadyComplete: true}, nil
	}
	if !acquired {
		return ProcessResult{Locked: true}, nil
	}
	defer func() {
		if err != nil {
			save, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if failure := processor.deliveries.Fail(save, delivery); failure != nil {
				err = errors.Join(err, failure)
			}
		}
	}()
	user, err := processor.repository.FindActiveUser(ctx, job.UserID, job.SubscriptionID)
	if errors.Is(err, ErrUserNotFound) {
		return ProcessResult{Stale: true}, processor.deliveries.Cancel(ctx, delivery)
	}
	if err != nil {
		return ProcessResult{}, err
	}
	if len(delivery.SnapshotJSON) > 0 {
		var snapshot Snapshot
		if err = json.Unmarshal(delivery.SnapshotJSON, &snapshot); err != nil {
			return ProcessResult{}, err
		}
		return processor.send(ctx, delivery, snapshot)
	}
	location, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("load digest user timezone: %w", err)
	}
	if processor.now().In(location).Format(localDateLayout) != job.LocalDate {
		return ProcessResult{Stale: true}, processor.deliveries.Cancel(ctx, delivery)
	}

	items, err := processor.repository.ListCandidates(
		ctx, user.ID, job.SubscriptionID, int(user.MaxItemsPerDigest),
	)
	if err != nil {
		return ProcessResult{}, err
	}
	if len(items) == 0 {
		snapshot := Snapshot{PaperIDs: []uint64{}}
		if err := processor.deliveries.Freeze(ctx, delivery, snapshot); err != nil {
			return ProcessResult{}, err
		}
		_, err := processor.deliveries.Finish(ctx, delivery, snapshot, processor.now().UTC())
		return ProcessResult{Empty: true}, err
	}

	paperIDs := make([]uint64, len(items))
	for i, item := range items {
		paperIDs[i] = item.PaperID
	}
	options := RenderOptions{PublicBaseURL: processor.publicBaseURL}
	countCtx, cancelCount := context.WithTimeout(ctx, 200*time.Millisecond)
	remaining, countErr := processor.repository.CountRemaining(countCtx, user.ID, job.SubscriptionID, paperIDs)
	if countErr == nil && countCtx.Err() == nil {
		options.Remaining = remaining
	}
	cancelCount() // Footer failure must not prevent delivery.
	var enrichment insight.Enrichment
	if processor.enricher != nil && user.DigestAIEnabled && len(items) >= 2 {
		lookup, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		value, lookupErr := processor.enricher.Lookup(lookup, user, job, items)
		if lookupErr == nil && lookup.Err() == nil {
			enrichment = value
		}
		cancel()
	}
	message, err := RenderEnrichedMessage(user, job, items, enrichment, options)
	if err != nil {
		message, err = RenderMessage(user, job, items, options)
	}
	if err != nil {
		return ProcessResult{}, err
	}
	message.ID = fmt.Sprintf("digest-%d@signalwatch", delivery.ID)
	snapshot := Snapshot{Message: message, PaperIDs: paperIDs}
	if err := processor.deliveries.Freeze(ctx, delivery, snapshot); err != nil {
		return ProcessResult{}, err
	}
	return processor.send(ctx, delivery, snapshot)
}
func (processor *Processor) send(ctx context.Context, d Delivery, snapshot Snapshot) (ProcessResult, error) {
	if len(snapshot.PaperIDs) == 0 {
		_, err := processor.deliveries.Finish(ctx, d, snapshot, processor.now().UTC())
		return ProcessResult{Empty: true}, err
	}
	if err := processor.sender.Send(ctx, snapshot.Message); err != nil {
		return ProcessResult{}, fmt.Errorf("send digest: %w", err)
	}
	marked, err := processor.deliveries.Finish(ctx, d, snapshot, processor.now().UTC())
	return ProcessResult{Items: len(snapshot.PaperIDs), MarkedRelations: marked, Retried: d.Attempts > 1}, err
}
