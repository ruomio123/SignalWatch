package digest

import (
	"context"
	"errors"
	"testing"
	"time"
)

type repositoryStub struct {
	remaining    int64
	countErr     error
	user         User
	items        []Item
	findErr      error
	listErr      error
	markErr      error
	requestedMax int
	markedIDs    []uint64
	markedAt     time.Time
	events       *[]string
}

func (stub *repositoryStub) CountRemaining(context.Context, uint64, uint64, []uint64) (int64, error) {
	return stub.remaining, stub.countErr
}

func (stub *repositoryStub) ListActiveSchedules(context.Context) ([]Schedule, error) {
	return nil, nil
}
func (stub *repositoryStub) FindActiveUser(context.Context, uint64, uint64) (User, error) {
	return stub.user, stub.findErr
}
func (stub *repositoryStub) ListCandidates(_ context.Context, _ uint64, _ uint64, limit int) ([]Item, error) {
	stub.requestedMax = limit
	return stub.items, stub.listErr
}
func (stub *repositoryStub) MarkDelivered(
	_ context.Context, _ uint64, _ uint64, ids []uint64, at time.Time,
) (int64, error) {
	if stub.events != nil {
		*stub.events = append(*stub.events, "mark-delivered")
	}
	stub.markedIDs = append([]uint64(nil), ids...)
	stub.markedAt = at
	return int64(len(ids)), stub.markErr
}

type coordinatorStub struct {
	acquired    bool
	complete    bool
	acquireErr  error
	completeErr error
	markErr     error
	marked      bool
	released    bool
	events      *[]string
}

func (stub *coordinatorStub) Acquire(context.Context, Job) (ReleaseFunc, bool, error) {
	return func(context.Context) error { stub.released = true; return nil }, stub.acquired, stub.acquireErr
}
func (stub *coordinatorStub) IsComplete(context.Context, Job) (bool, error) {
	return stub.complete, stub.completeErr
}
func (stub *coordinatorStub) MarkComplete(context.Context, Job) error {
	if stub.events != nil {
		*stub.events = append(*stub.events, "mark-complete")
	}
	stub.marked = true
	return stub.markErr
}

type senderStub struct {
	err     error
	message Message
	events  *[]string
}

func (stub *senderStub) Send(_ context.Context, message Message) error {
	if stub.events != nil {
		*stub.events = append(*stub.events, "send")
	}
	stub.message = message
	return stub.err
}

func digestFixture() (Job, User, []Item, time.Time) {
	now := time.Date(2026, 9, 8, 8, 30, 0, 0, time.UTC)
	return Job{SubscriptionID: 3, UserID: 9, LocalDate: "2026-09-08"}, User{
			ID: 9, Email: "reader@example.test", Timezone: "UTC", MaxItemsPerDigest: 2,
		}, []Item{{
			PaperID: 11, ArXivID: "2609.00011", Title: "Agent Systems", Abstract: "An abstract.",
			Authors: []string{"Ada"}, Categories: []string{"cs.AI"}, PublishedAt: now,
			ArXivURL: "https://arxiv.org/abs/2609.00011", PDFURL: "https://arxiv.org/pdf/2609.00011",
			Matches: []Match{{SubscriptionID: 3, SubscriptionName: "Agents", Category: "cs.AI", MatchedKeywords: []string{"agent"}}},
		}}, now
}

func TestProcessorSendsThenMarksAllRelationsThenCompletes(t *testing.T) {
	job, user, items, now := digestFixture()
	events := []string{}
	repository := &repositoryStub{user: user, items: items, events: &events}
	coordinator := &coordinatorStub{acquired: true, events: &events}
	sender := &senderStub{events: &events}
	processor, err := newTestProcessor(repository, coordinator, sender, func() time.Time { return now })
	if err != nil {
		t.Fatalf("new processor: %v", err)
	}
	result, err := processor.Process(t.Context(), job)
	if err != nil {
		t.Fatalf("process digest: %v", err)
	}
	if result.Items != 1 || result.MarkedRelations != 1 || repository.requestedMax != 2 {
		t.Fatalf("unexpected process result: %+v max=%d", result, repository.requestedMax)
	}
	if len(repository.markedIDs) != 1 || repository.markedIDs[0] != 11 || !repository.markedAt.Equal(now) {
		t.Fatalf("unexpected delivery update: ids=%v at=%v", repository.markedIDs, repository.markedAt)
	}
	wantEvents := []string{"send", "mark-delivered", "mark-complete"}
	if len(events) != len(wantEvents) {
		t.Fatalf("unexpected event order: %v", events)
	}
	for index := range wantEvents {
		if events[index] != wantEvents[index] {
			t.Fatalf("unexpected event order: %v", events)
		}
	}
	if !coordinator.marked || !coordinator.released || sender.message.To != user.Email {
		t.Fatalf("completion/release/message missing: complete=%v release=%v message=%+v",
			coordinator.marked, coordinator.released, sender.message)
	}
}

func TestProcessorMarksEmptyDayCompleteWithoutSending(t *testing.T) {
	job, user, _, now := digestFixture()
	repository := &repositoryStub{user: user, items: []Item{}}
	coordinator := &coordinatorStub{acquired: true}
	sender := &senderStub{}
	processor, _ := newTestProcessor(repository, coordinator, sender, func() time.Time { return now })
	result, err := processor.Process(t.Context(), job)
	if err != nil || !result.Empty || !coordinator.marked || sender.message.To != "" {
		t.Fatalf("unexpected empty result=%+v marked=%v message=%+v err=%v",
			result, coordinator.marked, sender.message, err)
	}
}

func TestProcessorFailureNeverWritesCompletionMarker(t *testing.T) {
	job, user, items, now := digestFixture()
	tests := []struct {
		name     string
		sendErr  error
		markErr  error
		wantSent bool
	}{
		{name: "SMTP failure", sendErr: errors.New("smtp unavailable")},
		{name: "database failure after SMTP", markErr: errors.New("mysql unavailable"), wantSent: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{user: user, items: items, markErr: test.markErr}
			coordinator := &coordinatorStub{acquired: true}
			sender := &senderStub{err: test.sendErr}
			processor, _ := newTestProcessor(repository, coordinator, sender, func() time.Time { return now })
			if _, err := processor.Process(t.Context(), job); err == nil {
				t.Fatal("expected processing failure")
			}
			if coordinator.marked {
				t.Fatal("failure must not write completion marker")
			}
			if test.sendErr != nil && len(repository.markedIDs) != 0 {
				t.Fatal("SMTP failure must not update delivered_at")
			}
		})
	}
}

func TestProcessorSkipsLockContentionAndCompletedDay(t *testing.T) {
	job, user, items, now := digestFixture()
	for _, test := range []struct {
		name        string
		coordinator *coordinatorStub
		assert      func(ProcessResult) bool
	}{
		{name: "locked", coordinator: &coordinatorStub{acquired: false}, assert: func(result ProcessResult) bool { return result.Locked }},
		{name: "complete", coordinator: &coordinatorStub{acquired: true, complete: true}, assert: func(result ProcessResult) bool { return result.AlreadyComplete }},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{user: user, items: items}
			processor, _ := newTestProcessor(repository, test.coordinator, &senderStub{}, func() time.Time { return now })
			result, err := processor.Process(t.Context(), job)
			if err != nil || !test.assert(result) || repository.requestedMax != 0 {
				t.Fatalf("unexpected skip result=%+v queried=%d err=%v", result, repository.requestedMax, err)
			}
		})
	}
}

func TestRemainingCountFailureDoesNotBlockDelivery(t *testing.T) {
	job, u, items, now := digestFixture()
	repo := &repositoryStub{user: u, items: items, countErr: errors.New("count unavailable")}
	sender := &senderStub{}
	coord := &coordinatorStub{acquired: true}
	p, _ := newTestProcessor(repo, coord, sender, func() time.Time { return now })
	result, err := p.Process(t.Context(), job)
	if err != nil || result.Items != 1 || !coord.marked {
		t.Fatalf("footer blocked mail: %+v %v", result, err)
	}
}

type deliveryStub struct {
	coordinator *coordinatorStub
	repo        *repositoryStub
}

func newTestProcessor(repo *repositoryStub, c *coordinatorStub, sender Sender, now func() time.Time) (*Processor, error) {
	return NewProcessor(repo, &deliveryStub{c, repo}, sender, now)
}
func (s *deliveryStub) Claim(ctx context.Context, j Job) (Delivery, bool, error) {
	d := Delivery{ID: 1, Job: j}
	if s.coordinator.complete {
		d.State = "sent"
	}
	return d, s.coordinator.acquired, s.coordinator.acquireErr
}
func (s *deliveryStub) Freeze(context.Context, Delivery, Snapshot) error { return nil }
func (s *deliveryStub) Finish(ctx context.Context, d Delivery, snap Snapshot, now time.Time) (int64, error) {
	var n int64
	var err error
	if len(snap.PaperIDs) > 0 {
		n, err = s.repo.MarkDelivered(ctx, d.UserID, d.SubscriptionID, snap.PaperIDs, now)
		if err != nil {
			return 0, err
		}
	}
	s.coordinator.released = true
	return n, s.coordinator.MarkComplete(ctx, d.Job)
}
func (s *deliveryStub) Fail(context.Context, Delivery) error {
	s.coordinator.released = true
	return nil
}

func (s *deliveryStub) Cancel(context.Context, Delivery) error { return nil }

type ReleaseFunc func(context.Context) error
