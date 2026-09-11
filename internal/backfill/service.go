package backfill

import (
	"context"
	"encoding/json"
	"log/slog"
	"signalwatch/internal/rules"
	"time"
)

type Task struct {
	SubscriptionID, SourceID, CursorID uint64
	Category                           string
	KeywordsJSON                       json.RawMessage
	WindowFrom, WindowTo               time.Time
	LeaseOwner                         string
	Attempts                           int
}
type Paper struct {
	ID              uint64
	Title, Abstract string
	CategoriesJSON  json.RawMessage
}
type Match struct {
	PaperID  uint64
	Keywords []string
}
type Store interface {
	Claim(context.Context) (Task, bool, error)
	Papers(context.Context, Task, int) ([]Paper, error)
	Commit(context.Context, Task, uint64, int, []Match, bool) error
	Fail(context.Context, Task) error
}
type Service struct {
	store  Store
	logger *slog.Logger
}

func New(store Store, logger *slog.Logger) *Service { return &Service{store: store, logger: logger} }
func (s *Service) Step(ctx context.Context) (bool, error) {
	task, ok, err := s.store.Claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err = s.process(work, task)
	if err != nil {
		save, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if e := s.store.Fail(save, task); e != nil {
			s.logger.Error("backfill failure persistence failed", "error", e)
		}
	}
	return true, err
}
func (s *Service) process(ctx context.Context, t Task) error {
	const batchSize = 200
	papers, err := s.store.Papers(ctx, t, batchSize)
	if err != nil {
		return err
	}
	var keywords []string
	if err := json.Unmarshal(t.KeywordsJSON, &keywords); err != nil {
		return err
	}
	matches := []Match{}
	cursor := t.CursorID
	for _, p := range papers {
		var categories []string
		if err := json.Unmarshal(p.CategoriesJSON, &categories); err != nil {
			return err
		}
		hits, ok := rules.Match(rules.Paper{Title: p.Title, Abstract: p.Abstract, Categories: categories}, rules.Rule{Category: t.Category, Keywords: keywords})
		if ok {
			matches = append(matches, Match{PaperID: p.ID, Keywords: hits})
		}
		cursor = p.ID
	}
	return s.store.Commit(ctx, t, cursor, len(papers), matches, len(papers) < batchSize)
}
func (s *Service) Run(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := s.Step(ctx)
		if err != nil {
			s.logger.Error("backfill batch failed", "error", err)
		}
		if !worked || err != nil {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
