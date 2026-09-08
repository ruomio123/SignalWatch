package collector

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"signalwatch/internal/source"
)

type ActiveSource struct {
	Source     source.Source
	Categories []string
}

type DemandRepository interface {
	ListActiveArXivSources(ctx context.Context) ([]ActiveSource, error)
}

type repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) DemandRepository {
	return &repository{db: db}
}

type activeCategoryRow struct {
	SourceID         uint64          `gorm:"column:source_id"`
	SourceKey        string          `gorm:"column:source_key"`
	SourceKind       string          `gorm:"column:source_kind"`
	SourceName       string          `gorm:"column:source_name"`
	SourceEndpoint   *string         `gorm:"column:source_endpoint"`
	SourceEnabled    bool            `gorm:"column:source_enabled"`
	SourceConfigJSON json.RawMessage `gorm:"column:source_config_json"`
	Category         string          `gorm:"column:category"`
}

func (repository *repository) ListActiveArXivSources(
	ctx context.Context,
) ([]ActiveSource, error) {
	var rows []activeCategoryRow
	err := repository.db.WithContext(ctx).
		Table("sources").
		Select(
			"sources.id AS source_id",
			"sources.source_key AS source_key",
			"sources.kind AS source_kind",
			"sources.name AS source_name",
			"sources.endpoint AS source_endpoint",
			"sources.enabled AS source_enabled",
			"sources.config_json AS source_config_json",
			"subscriptions.category AS category",
		).
		Joins("JOIN subscriptions ON subscriptions.source_id = sources.id").
		Where("sources.enabled = ? AND sources.kind = ?", true, source.KindArXiv).
		Where("subscriptions.enabled = ? AND subscriptions.deleted_at IS NULL", true).
		Order("sources.id ASC, subscriptions.category ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list active arxiv categories: %w", err)
	}

	result := make([]ActiveSource, 0)
	indexBySource := make(map[uint64]int)
	allowedBySource := make(map[uint64][]string)
	seenBySource := make(map[uint64]map[string]struct{})
	for _, row := range rows {
		index, exists := indexBySource[row.SourceID]
		if !exists {
			sourceRecord := source.Source{
				ID: row.SourceID, SourceKey: row.SourceKey,
				Kind: row.SourceKind, Name: row.SourceName,
				Endpoint: row.SourceEndpoint, Enabled: row.SourceEnabled,
				ConfigJSON: row.SourceConfigJSON,
			}
			publicSource, err := sourceRecord.Public()
			if err != nil {
				return nil, fmt.Errorf("read source %d configuration: %w", row.SourceID, err)
			}
			index = len(result)
			indexBySource[row.SourceID] = index
			result = append(result, ActiveSource{
				Source: sourceRecord, Categories: make([]string, 0),
			})
			allowedBySource[row.SourceID] = publicSource.AllowedCategories
			seenBySource[row.SourceID] = make(map[string]struct{})
		}

		_, alreadyAdded := seenBySource[row.SourceID][row.Category]
		if containsCategory(allowedBySource[row.SourceID], row.Category) && !alreadyAdded {
			result[index].Categories = append(result[index].Categories, row.Category)
			seenBySource[row.SourceID][row.Category] = struct{}{}
		}
	}

	filtered := result[:0]
	for _, active := range result {
		if len(active.Categories) > 0 {
			filtered = append(filtered, active)
		}
	}
	return filtered, nil
}

func containsCategory(categories []string, wanted string) bool {
	for _, category := range categories {
		if category == wanted {
			return true
		}
	}
	return false
}
