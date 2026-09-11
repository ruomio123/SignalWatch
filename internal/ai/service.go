package ai

import (
	"context"
	"fmt"
	"log/slog"
	"signalwatch/internal/digest"
	"signalwatch/internal/paper"
	"signalwatch/internal/user"
	"sync"
	"time"
)

type UserReader interface {
	FindActiveByID(context.Context, uint64) (user.User, error)
}
type PaperReader interface {
	Get(context.Context, uint64, uint64) (paper.PublicPaper, error)
}
type Dependencies struct {
	Tasks          TaskStore
	Configurations *ConfigurationService
	Calls          *CallRunner
	Users          UserReader
	Papers         PaperReader
	Digests        digest.CandidateReader
	Completion     digest.CompletionReader
}

// Service owns scheduling only. Queries, demand scanning and execution have
// separate dependencies and lifecycles; no component opens a database.
type Service struct {
	repo           TaskStore
	configurations *ConfigurationService
	*SummaryQueries
	executor *Executor
	scanner  *DemandScanner
	enabled  bool
	workers  int
	queue    chan Task
	queued   sync.Map
	logger   *slog.Logger
	now      func() time.Time
}

func NewService(d Dependencies, workers, capacity int, providers []string, logger *slog.Logger, now func() time.Time) *Service {
	if d.Tasks == nil || d.Configurations == nil || d.Calls == nil || d.Users == nil || d.Papers == nil || d.Digests == nil || workers < 1 || capacity < 1 || logger == nil || now == nil {
		panic("invalid AI dependencies")
	}
	return &Service{repo: d.Tasks, configurations: d.Configurations, enabled: true, workers: workers, queue: make(chan Task, capacity), logger: logger, now: now,
		SummaryQueries: &SummaryQueries{repo: d.Tasks, configurations: d.Configurations, enabled: true, users: d.Users, papers: d.Papers, now: now},
		executor:       &Executor{repo: d.Tasks, configurations: d.Configurations, calls: d.Calls, enabledProviders: append([]string(nil), providers...), digest: d.Digests, coordinator: d.Completion, logger: logger, now: now},
		scanner:        &DemandScanner{repo: d.Tasks, configurations: d.Configurations, digest: d.Digests, coordinator: d.Completion, logger: logger, now: now, enabled: true},
	}
}
func Disabled(logger *slog.Logger) *Service {
	return &Service{logger: logger, SummaryQueries: &SummaryQueries{}}
}
func (s *Service) Configurations() *ConfigurationService { return s.configurations }
func (s *Service) Process(ctx context.Context, t Task) {
	if s.enabled {
		s.executor.Process(ctx, t)
	}
}
func (s *Service) Scan(ctx context.Context) {
	if s.enabled {
		if err := s.executor.calls.store.Recover(ctx, s.now().UTC()); err != nil {
			s.warn("call_recovery")
		}
		s.scanner.Scan(ctx)
	}
}
func (s *Service) Run(ctx context.Context) {
	if !s.enabled {
		return
	}
	var wg sync.WaitGroup
	for i := 0; i < s.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case t := <-s.queue:
					s.Process(ctx, t)
					s.queued.Delete(fmt.Sprintf("%s:%d", t.Kind, t.ID))
				}
			}
		}()
	}
	defer wg.Wait()
	scan := time.NewTicker(time.Minute)
	dispatch := time.NewTicker(time.Second)
	defer scan.Stop()
	defer dispatch.Stop()
	s.Scan(ctx)
	s.dispatch(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-scan.C:
			s.Scan(ctx)
		case <-dispatch.C:
			s.dispatch(ctx)
		}
	}
}
func (s *Service) dispatch(ctx context.Context) {
	query, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for _, kind := range []string{DigestKind, PaperKind} {
		rows, err := s.repo.Pending(query, kind, "", s.now().UTC(), cap(s.queue))
		if err != nil {
			s.warn("dispatch")
			return
		}
		for _, t := range rows {
			key := fmt.Sprintf("%s:%d", t.Kind, t.ID)
			if _, exists := s.queued.LoadOrStore(key, true); exists {
				continue
			}
			select {
			case s.queue <- t:
			case <-ctx.Done():
				s.queued.Delete(key)
				return
			default:
				s.queued.Delete(key)
				return
			}
		}
	}
}
func (s *Service) warn(stage string) {
	s.logger.Warn("AI background operation unavailable", "module", "ai", "stage", stage)
}

// Stats reports no source text, prompts, secrets or user identities.
func (s *Service) Stats(ctx context.Context) (map[string]any, error) {
	result := map[string]any{"enabled": s.enabled}
	if !s.enabled {
		return result, nil
	}
	result["queue_depth"] = len(s.queue)
	result["queue_capacity"] = cap(s.queue)
	result["workers"] = s.workers
	result["processing"] = s.executor.active.Load()
	result["succeeded"] = s.executor.succeeded.Load()
	result["failed"] = s.executor.failed.Load()
	result["cache_hits"] = s.hits.Load()
	result["cache_misses"] = s.misses.Load()
	usage, err := s.repo.UsageTotals(ctx, s.now().UTC())
	if err != nil {
		return result, err
	}
	result["call_outcomes"] = s.executor.calls.Stats()
	result["requests_today"] = usage.Requests
	result["input_tokens"] = usage.InputTokens
	result["output_tokens"] = usage.OutputTokens
	result["estimated_calls"] = usage.EstimatedCalls
	return result, nil
}

func (s *Service) Calls() *CallRunner {
	if s.executor == nil {
		return nil
	}
	return s.executor.calls
}
