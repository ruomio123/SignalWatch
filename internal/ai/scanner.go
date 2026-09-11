package ai

import (
	"context"
	"log/slog"
	"signalwatch/internal/subscription"
	"time"

	"signalwatch/internal/digest"
	"signalwatch/internal/user"
)

type DemandStore interface {
	DemandUsers(context.Context, uint64) ([]user.User, error)
	DemandSubscriptions(context.Context, uint64) ([]subscription.Subscription, error)
	Ensure(context.Context, *Task) error
	Cleanup(context.Context, time.Time) error
}
type DemandScanner struct {
	repo           DemandStore
	configurations *ConfigurationService
	digest         digest.CandidateReader
	coordinator    digest.CompletionReader
	logger         *slog.Logger
	now            func() time.Time
	enabled        bool
	scanUser       uint64
	lastDigest     time.Time
}

func (s *DemandScanner) warn(stage string) {
	s.logger.Warn("AI demand scanning unavailable", "module", "ai", "stage", stage)
}

// Scan uses bounded pages and rotating cursors, independent of ingestion checkpoints.
func (s *DemandScanner) Scan(ctx context.Context) {
	if !s.enabled {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	now := s.now().UTC()
	users, err := s.repo.DemandUsers(ctx, s.scanUser)
	if err != nil {
		s.warn("demand_users")
		return
	}
	prepare := s.lastDigest.IsZero() || now.Sub(s.lastDigest) >= 5*time.Minute
	for _, u := range users {
		s.scanUser = u.ID
		if prepare {
			s.prepareDigest(ctx, u, now)
		}
	}
	if len(users) < 100 {
		s.scanUser = 0
		if prepare {
			s.lastDigest = now
		}
	}
	s.cleanup(ctx, now)
}
func UpcomingDate(now time.Time, timezone, clock string) (string, bool) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return "", false
	}
	at, err := time.Parse("15:04:05", clock)
	if err != nil {
		return "", false
	}
	local := now.In(loc)
	due := time.Date(local.Year(), local.Month(), local.Day(), at.Hour(), at.Minute(), 0, 0, loc)
	if due.Before(local) {
		due = time.Date(local.Year(), local.Month(), local.Day()+1, at.Hour(), at.Minute(), 0, 0, loc)
	}
	return due.Format("2006-01-02"), due.Sub(local) <= 30*time.Minute
}
func (s *DemandScanner) prepareDigest(ctx context.Context, u user.User, now time.Time) {
	date, due := UpcomingDate(now, u.Timezone, u.DigestTime)
	if !due {
		return
	}
	subs, err := s.repo.DemandSubscriptions(ctx, u.ID)
	if err != nil {
		s.warn("digest_demand")
		return
	}
	for _, sub := range subs {
		if s.coordinator != nil {
			complete, err := s.coordinator.IsComplete(ctx, digest.Job{UserID: u.ID, SubscriptionID: sub.ID, LocalDate: date})
			if err != nil || complete {
				continue
			}
		}
		items, err := s.digest.ListCandidates(ctx, u.ID, sub.ID, int(sub.MaxItemsPerDigest))
		if err != nil || len(items) < 2 {
			continue
		}
		input := Input{SubscriptionID: sub.ID, Papers: inputs(items), Timezone: u.Timezone, Limit: int(sub.MaxItemsPerDigest)}
		configuration, err := s.configurations.Get(ctx, u.ID)
		if err != nil || !configuration.Usable {
			continue
		}
		profile := generationProfile(configuration.ProviderID, configuration.ModelID)
		t := newUserTask(DigestKind, u.ID, u.ID, date, sub.DigestAILanguage, configuration.ProviderID, configuration.ModelID, configuration.Generation, configuration.Version, profile, input, 0, now)
		if err := s.repo.Ensure(ctx, &t); err != nil {
			s.warn("digest_enqueue")
		}
	}
}

func (s *DemandScanner) cleanup(ctx context.Context, now time.Time) {
	if err := s.repo.Cleanup(ctx, now); err != nil {
		s.warn("cleanup")
	}
}
