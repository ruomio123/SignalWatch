package matcher

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"signalwatch/internal/paper"
)

// Candidate contains only the subscription fields needed by the deterministic
// M3 matcher. Filtering active subscriptions and the from-now boundary in SQL
// keeps disabled, deleted, and historical subscriptions out of the hot path.
type Candidate struct {
	ID           uint64    `gorm:"column:id"`
	Category     string    `gorm:"column:category"`
	KeywordsJSON []byte    `gorm:"column:keywords_json"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

type Repository interface {
	FindPaper(ctx context.Context, paperID uint64) (paper.Paper, error)
	ListCandidates(
		ctx context.Context,
		sourceID uint64,
		categories []string,
		firstSeenAt time.Time,
	) ([]Candidate, error)
	InsertMatches(ctx context.Context, matches []paper.SubscriptionPaper) (int64, error)
}

type repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &repository{db: db}
}

func (repository *repository) FindPaper(ctx context.Context, paperID uint64) (paper.Paper, error) {
	var stored paper.Paper
	if err := repository.db.WithContext(ctx).Where("id = ?", paperID).Take(&stored).Error; err != nil {
		return paper.Paper{}, fmt.Errorf("find paper %d: %w", paperID, err)
	}
	return stored, nil
}

func (repository *repository) ListCandidates(
	ctx context.Context,
	sourceID uint64,
	categories []string,
	firstSeenAt time.Time,
) ([]Candidate, error) {
	if len(categories) == 0 {
		return []Candidate{}, nil
	}

	var candidates []Candidate
	err := repository.db.WithContext(ctx).
		Table("subscriptions").
		Select("id, category, keywords_json, created_at").
		Where("source_id = ?", sourceID).
		Where("enabled = ?", true).
		Where("deleted_at IS NULL").
		Where("category IN ?", categories).
		Where("created_at <= ?", firstSeenAt.UTC()).
		Order("id ASC").
		Scan(&candidates).Error
	if err != nil {
		return nil, fmt.Errorf("list matcher candidates: %w", err)
	}
	return candidates, nil
}

func (repository *repository) InsertMatches(
	ctx context.Context,
	matches []paper.SubscriptionPaper,
) (int64, error) {
	if len(matches) == 0 {
		return 0, nil
	}
	result := repository.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&matches)
	if result.Error != nil {
		return 0, fmt.Errorf("insert subscription paper matches: %w", result.Error)
	}
	return result.RowsAffected, nil
}
