package collector

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"signalwatch/internal/source"
)

type ActiveSource struct {
	Source     source.Source
	Categories []string
}

type SourceRepository interface {
	ListEnabledArXivSources(ctx context.Context) ([]ActiveSource, error)
	UpdateLastSuccessfulSyncAt(ctx context.Context, sourceID uint64, syncedAt time.Time) error
}

type repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) SourceRepository {
	return &repository{db: db}
}

func (repository *repository) ListEnabledArXivSources(ctx context.Context) ([]ActiveSource, error) {
	var rows []source.Source
	if err := repository.db.WithContext(ctx).
		Where("enabled = ? AND kind = ?", true, source.KindArXiv).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list enabled arxiv sources: %w", err)
	}

	result := make([]ActiveSource, 0, len(rows))
	for _, row := range rows {
		publicSource, err := row.Public()
		if err != nil {
			return nil, fmt.Errorf("read source %d configuration: %w", row.ID, err)
		}
		if len(publicSource.AllowedCategories) == 0 {
			return nil, fmt.Errorf("source %d has no allowed categories", row.ID)
		}
		categories := make([]string, 0, len(publicSource.AllowedCategories))
		seen := make(map[string]struct{}, len(publicSource.AllowedCategories))
		for _, category := range publicSource.AllowedCategories {
			if _, exists := seen[category]; exists {
				continue
			}
			seen[category] = struct{}{}
			categories = append(categories, category)
		}
		result = append(result, ActiveSource{
			Source: row, Categories: categories,
		})
	}
	return result, nil
}

func (repository *repository) UpdateLastSuccessfulSyncAt(
	ctx context.Context,
	sourceID uint64,
	syncedAt time.Time,
) error {
	result := repository.db.WithContext(ctx).Model(&source.Source{}).
		Where("id = ?", sourceID).
		Update("last_successful_sync_at", syncedAt.UTC())
	if result.Error != nil {
		return fmt.Errorf("update source sync checkpoint: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("update source sync checkpoint: source %d not found", sourceID)
	}
	return nil
}
