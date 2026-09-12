package subscription

import (
	"encoding/json"
	"time"

	"signalwatch/internal/source"
)

const InitialVersion uint32 = 1

type BackfillStatus struct {
	State     string `json:"state"`
	Processed uint64 `json:"processed"`
	Matched   uint64 `json:"matched"`
}

type Subscription struct {
	Backfill          BackfillStatus  `gorm:"-"`
	MaxItemsPerDigest uint16          `gorm:"column:max_items_per_digest;default:20"`
	DigestAIEnabled   bool            `gorm:"column:digest_ai_enabled;default:false"`
	DigestAILanguage  string          `gorm:"column:digest_ai_language;default:zh"`
	ID                uint64          `gorm:"column:id;primaryKey;autoIncrement"`
	UserID            uint64          `gorm:"column:user_id"`
	SourceID          uint64          `gorm:"column:source_id"`
	Name              string          `gorm:"column:name"`
	Objective         *string         `gorm:"column:objective"`
	Category          string          `gorm:"column:category"`
	KeywordsJSON      json.RawMessage `gorm:"column:keywords_json"`
	Enabled           bool            `gorm:"column:enabled"`
	Version           uint32          `gorm:"column:version"`
	CreatedAt         time.Time       `gorm:"column:created_at"`
	UpdatedAt         time.Time       `gorm:"column:updated_at"`
	DeletedAt         *time.Time      `gorm:"column:deleted_at"`
}

func (Subscription) TableName() string { return "subscriptions" }

type RulesInput struct {
	Category string   `json:"category"`
	Keywords []string `json:"keywords"`
}

type CreateInput struct {
	MaxItemsPerDigest *uint16    `json:"max_items_per_digest"`
	DigestAIEnabled   *bool      `json:"digest_ai_enabled"`
	DigestAILanguage  *string    `json:"digest_ai_language"`
	SourceID          uint64     `json:"source_id"`
	Name              string     `json:"name"`
	Objective         *string    `json:"objective"`
	Enabled           *bool      `json:"enabled"`
	Rules             RulesInput `json:"rules"`
}

// UpdateInput keeps field presence separate from field values. In particular,
// ObjectiveSet distinguishes an omitted objective from an explicit clear.
type UpdateInput struct {
	MaxItemsPerDigest *uint16
	DigestAIEnabled   *bool
	DigestAILanguage  *string
	Name              *string
	ObjectiveSet      bool
	Objective         *string
	Enabled           *bool
	Rules             *RulesInput
}

type SubscriptionPatch struct {
	MaxItemsPerDigest *uint16
	DigestAIEnabled   *bool
	DigestAILanguage  *string
	Name              *string
	ObjectiveSet      bool
	Objective         *string
	Enabled           *bool
	Category          *string
	KeywordsJSON      *json.RawMessage
}

type ListFilter struct {
	Enabled  *bool
	SourceID *uint64
}

type ListInput struct {
	Page     int
	PageSize int
	Filter   ListFilter
}

type QueryResult struct {
	Subscription Subscription
	Source       source.Source
}

type PublicRules = RulesInput

type PublicSubscription struct {
	Backfill          BackfillStatus      `json:"backfill"`
	MaxItemsPerDigest uint16              `json:"max_items_per_digest"`
	DigestAIEnabled   bool                `json:"digest_ai_enabled"`
	DigestAILanguage  string              `json:"digest_ai_language"`
	ID                uint64              `json:"id"`
	Source            source.PublicSource `json:"source"`
	Name              string              `json:"name"`
	Objective         *string             `json:"objective"`
	Enabled           bool                `json:"enabled"`
	Version           uint32              `json:"version"`
	Rules             PublicRules         `json:"rules"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
}

type ListResult struct {
	Items    []PublicSubscription `json:"items"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
	Total    int64                `json:"total"`
}
