package digest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"signalwatch/internal/paper"
	"signalwatch/internal/subscription"
	userpkg "signalwatch/internal/user"
)

var ErrUserNotFound = errors.New("digest user not found")

type Repository interface {
	ScheduleRepository
	FindActiveUser(ctx context.Context, userID uint64) (User, error)
	ListCandidates(ctx context.Context, userID uint64, limit int) ([]Item, error)
	MarkDelivered(ctx context.Context, userID uint64, paperIDs []uint64, deliveredAt time.Time) (int64, error)
}

type repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository { return &repository{db: db} }

func (repository *repository) ListActiveSchedules(ctx context.Context) ([]Schedule, error) {
	var schedules []Schedule
	err := repository.db.WithContext(ctx).
		Model(&userpkg.User{}).
		Select(`id AS user_id, email, timezone, TIME_FORMAT(digest_time, '%H:%i:%s') AS digest_time,
			max_items_per_digest`).
		Where("status = ?", userpkg.StatusActive).
		Order("id ASC").
		Scan(&schedules).Error
	if err != nil {
		return nil, fmt.Errorf("list active digest users: %w", err)
	}
	return schedules, nil
}

func (repository *repository) FindActiveUser(ctx context.Context, userID uint64) (User, error) {
	var row struct {
		ID                uint64 `gorm:"column:id"`
		Email             string `gorm:"column:email"`
		Timezone          string `gorm:"column:timezone"`
		MaxItemsPerDigest uint16 `gorm:"column:max_items_per_digest"`
	}
	err := repository.db.WithContext(ctx).Model(&userpkg.User{}).
		Select("id, email, timezone, max_items_per_digest").
		Where("id = ? AND status = ?", userID, userpkg.StatusActive).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("find active digest user: %w", err)
	}
	return User{
		ID: row.ID, Email: row.Email, Timezone: row.Timezone,
		MaxItemsPerDigest: row.MaxItemsPerDigest,
	}, nil
}

func (repository *repository) ListCandidates(
	ctx context.Context,
	userID uint64,
	limit int,
) ([]Item, error) {
	if userID == 0 || limit < 1 {
		return nil, errors.New("invalid digest candidate query")
	}
	type selectedPaper struct {
		ID          uint64    `gorm:"column:id"`
		FirstSeenAt time.Time `gorm:"column:first_seen_at"`
	}
	var selected []selectedPaper
	err := repository.db.WithContext(ctx).
		Table("papers AS p").
		Select("p.id, p.first_seen_at").
		Joins("JOIN subscription_papers AS sp ON sp.paper_id = p.id").
		Joins("JOIN subscriptions AS s ON s.id = sp.subscription_id").
		Where("s.user_id = ?", userID).
		Where("s.enabled = ? AND s.deleted_at IS NULL", true).
		Where("sp.delivered_at IS NULL").
		Where(`NOT EXISTS (
			SELECT 1 FROM subscription_papers AS sent_sp
			JOIN subscriptions AS sent_s ON sent_s.id = sent_sp.subscription_id
			WHERE sent_s.user_id = ? AND sent_sp.paper_id = p.id
				AND sent_sp.delivered_at IS NOT NULL
		)`, userID).
		Group("p.id, p.first_seen_at").
		Order("p.first_seen_at ASC").
		Order("p.id ASC").
		Limit(limit).
		Scan(&selected).Error
	if err != nil {
		return nil, fmt.Errorf("select digest papers: %w", err)
	}
	if len(selected) == 0 {
		return []Item{}, nil
	}

	ids := make([]uint64, len(selected))
	for index, row := range selected {
		ids[index] = row.ID
	}
	type candidateRow struct {
		PaperID             uint64          `gorm:"column:paper_id"`
		ArXivID             string          `gorm:"column:arxiv_id"`
		Title               string          `gorm:"column:title"`
		Abstract            string          `gorm:"column:abstract"`
		AuthorsJSON         json.RawMessage `gorm:"column:authors_json"`
		CategoriesJSON      json.RawMessage `gorm:"column:categories_json"`
		PublishedAt         time.Time       `gorm:"column:published_at"`
		ArXivUpdatedAt      time.Time       `gorm:"column:arxiv_updated_at"`
		ArXivURL            string          `gorm:"column:arxiv_url"`
		PDFURL              string          `gorm:"column:pdf_url"`
		FirstSeenAt         time.Time       `gorm:"column:first_seen_at"`
		SubscriptionID      uint64          `gorm:"column:subscription_id"`
		SubscriptionName    string          `gorm:"column:subscription_name"`
		Category            string          `gorm:"column:category"`
		MatchedKeywordsJSON json.RawMessage `gorm:"column:matched_keywords_json"`
	}
	var rows []candidateRow
	err = repository.db.WithContext(ctx).
		Table("papers AS p").
		Select(`p.id AS paper_id, p.arxiv_id, p.title, p.abstract, p.authors_json,
			p.categories_json, p.published_at, p.arxiv_updated_at, p.arxiv_url,
			p.pdf_url, p.first_seen_at, s.id AS subscription_id,
			s.name AS subscription_name, s.category, sp.matched_keywords_json`).
		Joins("JOIN subscription_papers AS sp ON sp.paper_id = p.id").
		Joins("JOIN subscriptions AS s ON s.id = sp.subscription_id").
		Where("s.user_id = ?", userID).
		Where("s.enabled = ? AND s.deleted_at IS NULL", true).
		Where("sp.delivered_at IS NULL").
		Where("p.id IN ?", ids).
		Order("p.first_seen_at ASC").
		Order("p.id ASC").
		Order("s.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("load digest paper details: %w", err)
	}

	items := make([]Item, 0, len(selected))
	indexByPaper := make(map[uint64]int, len(selected))
	for _, row := range rows {
		index, exists := indexByPaper[row.PaperID]
		if !exists {
			var authors, categories []string
			if err := json.Unmarshal(row.AuthorsJSON, &authors); err != nil || authors == nil {
				return nil, fmt.Errorf("decode paper %d authors", row.PaperID)
			}
			if err := json.Unmarshal(row.CategoriesJSON, &categories); err != nil || categories == nil {
				return nil, fmt.Errorf("decode paper %d categories", row.PaperID)
			}
			items = append(items, Item{
				PaperID: row.PaperID, ArXivID: row.ArXivID, Title: row.Title,
				Abstract: row.Abstract, Authors: authors, Categories: categories,
				PublishedAt: row.PublishedAt, ArXivUpdatedAt: row.ArXivUpdatedAt,
				ArXivURL: row.ArXivURL, PDFURL: row.PDFURL, FirstSeenAt: row.FirstSeenAt,
				Matches: []Match{},
			})
			index = len(items) - 1
			indexByPaper[row.PaperID] = index
		}
		var keywords []string
		if err := json.Unmarshal(row.MatchedKeywordsJSON, &keywords); err != nil || keywords == nil {
			return nil, fmt.Errorf("decode paper %d matched keywords", row.PaperID)
		}
		items[index].Matches = append(items[index].Matches, Match{
			SubscriptionID: row.SubscriptionID, SubscriptionName: row.SubscriptionName,
			Category: row.Category, MatchedKeywords: keywords,
		})
	}
	return items, nil
}

func (repository *repository) MarkDelivered(
	ctx context.Context,
	userID uint64,
	paperIDs []uint64,
	deliveredAt time.Time,
) (int64, error) {
	if userID == 0 || len(paperIDs) == 0 || deliveredAt.IsZero() {
		return 0, errors.New("invalid digest delivery update")
	}
	var affected int64
	err := repository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ownedSubscriptions := tx.Model(&subscription.Subscription{}).
			Select("id").Where("user_id = ?", userID)
		result := tx.Model(&paper.SubscriptionPaper{}).
			Where("subscription_id IN (?)", ownedSubscriptions).
			Where("paper_id IN ?", paperIDs).
			Where("delivered_at IS NULL").
			Update("delivered_at", deliveredAt.UTC())
		if result.Error != nil {
			return result.Error
		}
		affected = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("mark digest delivered: %w", err)
	}
	return affected, nil
}
