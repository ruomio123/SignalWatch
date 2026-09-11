package digest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	userpkg "signalwatch/internal/user"
)

var ErrUserNotFound = errors.New("digest user not found")

type CandidateReader interface {
	FindActiveUser(context.Context, uint64, uint64) (User, error)
	ListCandidates(context.Context, uint64, uint64, int) ([]Item, error)
}
type CandidateRepository interface {
	CandidateReader
	CountRemaining(context.Context, uint64, uint64, []uint64) (int64, error)
}
type Repository interface {
	CandidateRepository
	ScheduleRepository
}

type repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository { return &repository{db: db} }

func (repository *repository) ListActiveSchedules(ctx context.Context) ([]Schedule, error) {
	var schedules []Schedule
	err := repository.db.WithContext(ctx).
		Table("users u").Joins("JOIN subscriptions s ON s.user_id=u.id").
		Select(`u.id AS user_id, s.id AS subscription_id, u.email, u.timezone, TIME_FORMAT(u.digest_time, '%H:%i:%s') AS digest_time, s.max_items_per_digest`).
		Where("u.status=? AND u.role=? AND s.enabled=1 AND s.deleted_at IS NULL", userpkg.StatusActive, userpkg.RoleUser).Order("u.id ASC,s.id ASC").
		Scan(&schedules).Error
	if err != nil {
		return nil, fmt.Errorf("list active digest users: %w", err)
	}
	var retries []Schedule
	if err := repository.db.WithContext(ctx).Table("digest_deliveries").Select("user_id,subscription_id,local_date").Where("(state='retry' AND next_retry_at<=UTC_TIMESTAMP(6)) OR (state='sending' AND lease_until<=UTC_TIMESTAMP(6))").Limit(1000).Find(&retries).Error; err != nil {
		return nil, err
	}
	return append(retries, schedules...), nil
}

func (repository *repository) FindActiveUser(ctx context.Context, userID, subscriptionID uint64) (User, error) {
	var row struct {
		SubscriptionID    uint64
		SubscriptionName  string
		DigestAIEnabled   bool
		DigestAILanguage  string
		ID                uint64 `gorm:"column:id"`
		Email             string `gorm:"column:email"`
		Timezone          string `gorm:"column:timezone"`
		MaxItemsPerDigest uint16 `gorm:"column:max_items_per_digest"`
	}
	err := repository.db.WithContext(ctx).Table("users u").Joins("JOIN subscriptions s ON s.user_id=u.id").
		Select("u.id, u.email, u.timezone, s.max_items_per_digest, s.digest_ai_enabled, s.digest_ai_language, s.id AS subscription_id, s.name AS subscription_name").
		Where("u.id=? AND s.id=? AND u.status=? AND u.role=? AND s.enabled=1 AND s.deleted_at IS NULL", userID, subscriptionID, userpkg.StatusActive, userpkg.RoleUser).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("find active digest user: %w", err)
	}
	return User{
		SubscriptionID: row.SubscriptionID, SubscriptionName: row.SubscriptionName,
		DigestAIEnabled: row.DigestAIEnabled, DigestAILanguage: row.DigestAILanguage,
		ID: row.ID, Email: row.Email, Timezone: row.Timezone,
		MaxItemsPerDigest: row.MaxItemsPerDigest,
	}, nil
}

func (repository *repository) ListCandidates(
	ctx context.Context,
	userID, subscriptionID uint64,
	limit int,
) ([]Item, error) {
	if userID == 0 || subscriptionID == 0 || limit < 1 {
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
		Where("s.user_id = ? AND s.id = ?", userID, subscriptionID).
		Where("s.enabled = ? AND s.deleted_at IS NULL", true).
		Where("sp.delivered_at IS NULL").
		Where("NOT EXISTS (SELECT 1 FROM digest_delivery_items di WHERE di.subscription_id=sp.subscription_id AND di.paper_id=sp.paper_id)").
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
		Comments            string          `gorm:"column:comments"`
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
		Select(`p.id AS paper_id, p.arxiv_id, p.title, p.abstract, p.comments, p.authors_json,
			p.categories_json, p.published_at, p.arxiv_updated_at, p.arxiv_url,
			p.pdf_url, p.first_seen_at, s.id AS subscription_id,
			s.name AS subscription_name, s.category, sp.matched_keywords_json`).
		Joins("JOIN subscription_papers AS sp ON sp.paper_id = p.id").
		Joins("JOIN subscriptions AS s ON s.id = sp.subscription_id").
		Where("s.user_id = ? AND s.id = ?", userID, subscriptionID).
		Where("s.enabled = ? AND s.deleted_at IS NULL", true).
		Where("sp.delivered_at IS NULL").
		Where("NOT EXISTS (SELECT 1 FROM digest_delivery_items di WHERE di.subscription_id=sp.subscription_id AND di.paper_id=sp.paper_id)").
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
				Abstract: row.Abstract, Comments: row.Comments, Authors: authors, Categories: categories,
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

// Count only this subscription's undelivered papers, excluding this email's IDs.
func (repository *repository) CountRemaining(ctx context.Context, userID, subscriptionID uint64, selectedIDs []uint64) (int64, error) {
	if userID == 0 || subscriptionID == 0 || len(selectedIDs) == 0 {
		return 0, errors.New("invalid remaining query")
	}
	var count int64
	err := repository.db.WithContext(ctx).Table("subscription_papers sp").
		Joins("JOIN subscriptions s ON s.id=sp.subscription_id").
		Joins("JOIN papers p ON p.id=sp.paper_id").
		Where("s.user_id=? AND s.id=? AND s.enabled=1 AND s.deleted_at IS NULL AND sp.delivered_at IS NULL", userID, subscriptionID).
		Where("NOT EXISTS (SELECT 1 FROM digest_delivery_items di WHERE di.subscription_id=sp.subscription_id AND di.paper_id=sp.paper_id)").Where("sp.paper_id NOT IN ?", selectedIDs).Distinct("sp.paper_id").Count(&count).Error
	return count, err
}
