package paper

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

var ErrQueryNotFound = errors.New("matched paper not found")

type QueryRepository interface {
	CountMatched(ctx context.Context, userID uint64, filter QueryFilter) (int64, error)
	ListMatched(ctx context.Context, userID uint64, filter QueryFilter, offset, limit int) ([]QueryResult, error)
	GetMatched(ctx context.Context, userID, paperID uint64) (QueryResult, error)
}

type queryRepository struct {
	db *gorm.DB
}

func NewQueryRepository(db *gorm.DB) QueryRepository {
	return &queryRepository{db: db}
}

func (repository *queryRepository) CountMatched(
	ctx context.Context,
	userID uint64,
	filter QueryFilter,
) (int64, error) {
	var count int64
	query := matchedPaperScope(repository.db.WithContext(ctx), userID, filter)
	if err := query.Distinct("papers.id").Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count owned matched papers: %w", err)
	}
	return count, nil
}

func (repository *queryRepository) ListMatched(
	ctx context.Context,
	userID uint64,
	filter QueryFilter,
	offset int,
	limit int,
) ([]QueryResult, error) {
	var papers []Paper
	query := matchedPaperScope(repository.db.WithContext(ctx), userID, filter).
		Select("papers.*").
		Group("papers.id").
		Order("papers.first_seen_at DESC").
		Order("papers.id DESC").
		Offset(offset).
		Limit(limit)
	if err := query.Find(&papers).Error; err != nil {
		return nil, fmt.Errorf("list owned matched papers: %w", err)
	}
	return repository.attachMatches(ctx, userID, papers)
}

func (repository *queryRepository) GetMatched(
	ctx context.Context,
	userID uint64,
	paperID uint64,
) (QueryResult, error) {
	var stored Paper
	err := matchedPaperScope(repository.db.WithContext(ctx), userID, QueryFilter{}).
		Select("papers.*").
		Where("papers.id = ?", paperID).
		Group("papers.id").
		Take(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return QueryResult{}, ErrQueryNotFound
	}
	if err != nil {
		return QueryResult{}, fmt.Errorf("get owned matched paper: %w", err)
	}
	results, err := repository.attachMatches(ctx, userID, []Paper{stored})
	if err != nil {
		return QueryResult{}, err
	}
	return results[0], nil
}

func matchedPaperScope(db *gorm.DB, userID uint64, filter QueryFilter) *gorm.DB {
	query := db.Model(&Paper{}).
		Joins("JOIN subscription_papers ON subscription_papers.paper_id = papers.id").
		Joins("JOIN subscriptions ON subscriptions.id = subscription_papers.subscription_id").
		Where("subscriptions.user_id = ?", userID)
	if filter.SubscriptionID != nil {
		query = query.Where("subscription_papers.subscription_id = ?", *filter.SubscriptionID)
	}
	return query
}

func (repository *queryRepository) attachMatches(
	ctx context.Context,
	userID uint64,
	papers []Paper,
) ([]QueryResult, error) {
	results := make([]QueryResult, len(papers))
	if len(papers) == 0 {
		return results, nil
	}
	ids := make([]uint64, len(papers))
	indexByID := make(map[uint64]int, len(papers))
	for index, stored := range papers {
		ids[index] = stored.ID
		indexByID[stored.ID] = index
		results[index] = QueryResult{Paper: stored, Matches: []MatchRow{}}
	}

	var rows []struct {
		PaperID uint64 `gorm:"column:paper_id"`
		MatchRow
	}
	err := repository.db.WithContext(ctx).
		Table("subscription_papers").
		Select(`subscription_papers.paper_id,
			subscriptions.id AS subscription_id,
			subscriptions.name AS subscription_name,
			subscriptions.category AS subscription_category,
			subscriptions.enabled AS subscription_enabled,
			subscriptions.deleted_at AS subscription_deleted_at,
			subscription_papers.matched_keywords_json,
			subscription_papers.matched_at`).
		Joins("JOIN subscriptions ON subscriptions.id = subscription_papers.subscription_id").
		Where("subscriptions.user_id = ?", userID).
		Where("subscription_papers.paper_id IN ?", ids).
		Order("subscription_papers.matched_at ASC").
		Order("subscriptions.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list owned paper match reasons: %w", err)
	}
	for _, row := range rows {
		if index, exists := indexByID[row.PaperID]; exists {
			results[index].Matches = append(results[index].Matches, row.MatchRow)
		}
	}
	return results, nil
}
