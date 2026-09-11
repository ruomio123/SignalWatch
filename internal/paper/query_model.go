package paper

import (
	"encoding/json"
	"time"
)

const (
	DefaultQueryPage     = 1
	DefaultQueryPageSize = 20
	MaxQueryPageSize     = 100
)

type QueryFilter struct {
	SubscriptionID *uint64
}

type QueryInput struct {
	Page     int
	PageSize int
	Filter   QueryFilter
}

type MatchRow struct {
	SubscriptionID        uint64          `gorm:"column:subscription_id"`
	SubscriptionName      string          `gorm:"column:subscription_name"`
	SubscriptionCategory  string          `gorm:"column:subscription_category"`
	SubscriptionEnabled   bool            `gorm:"column:subscription_enabled"`
	SubscriptionDeletedAt *time.Time      `gorm:"column:subscription_deleted_at"`
	MatchedKeywordsJSON   json.RawMessage `gorm:"column:matched_keywords_json"`
	MatchedAt             time.Time       `gorm:"column:matched_at"`
}

type QueryResult struct {
	Paper   Paper
	Matches []MatchRow
}

type PublicMatch struct {
	SubscriptionID     uint64    `json:"subscription_id"`
	SubscriptionName   string    `json:"subscription_name"`
	Category           string    `json:"category"`
	SubscriptionActive bool      `json:"subscription_active"`
	MatchedKeywords    []string  `json:"matched_keywords"`
	MatchedAt          time.Time `json:"matched_at"`
}

type PublicPaper struct {
	ID             uint64        `json:"id"`
	SourceID       uint64        `json:"source_id"`
	ArXivID        string        `json:"arxiv_id"`
	Title          string        `json:"title"`
	Abstract       string        `json:"abstract"`
	Comments       string        `json:"comments"`
	Authors        []string      `json:"authors"`
	Categories     []string      `json:"categories"`
	PublishedAt    time.Time     `json:"published_at"`
	ArXivUpdatedAt time.Time     `json:"arxiv_updated_at"`
	ArXivURL       string        `json:"arxiv_url"`
	PDFURL         string        `json:"pdf_url"`
	FirstSeenAt    time.Time     `json:"first_seen_at"`
	Matches        []PublicMatch `json:"matches"`
}

type QueryPage struct {
	Items    []PublicPaper `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int64         `json:"total"`
}
