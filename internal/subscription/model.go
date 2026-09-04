package subscription

import (
	"time"

	"signalwatch/internal/source"
)

const InitialVersion uint32 = 1

type Subscription struct {
	ID        uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	UserID    uint64     `gorm:"column:user_id"`
	SourceID  uint64     `gorm:"column:source_id"`
	Name      string     `gorm:"column:name"`
	Objective *string    `gorm:"column:objective"`
	Enabled   bool       `gorm:"column:enabled"`
	Version   uint32     `gorm:"column:version"`
	CreatedAt time.Time  `gorm:"column:created_at"`
	UpdatedAt time.Time  `gorm:"column:updated_at"`
	DeletedAt *time.Time `gorm:"column:deleted_at"`
}

func (Subscription) TableName() string { return "subscriptions" }

type Rule struct {
	ID              uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	SubscriptionID  uint64    `gorm:"column:subscription_id"`
	RuleType        string    `gorm:"column:rule_type"`
	RuleValue       string    `gorm:"column:rule_value"`
	NormalizedValue string    `gorm:"column:normalized_value"`
	CreatedAt       time.Time `gorm:"column:created_at"`
}

func (Rule) TableName() string { return "subscription_rules" }

type RulesInput struct {
	Categories      []string
	Authors         []string
	IncludeKeywords []string
	ExcludeKeywords []string
}

type CreateInput struct {
	SourceID  uint64
	Name      string
	Objective *string
	Enabled   *bool
	Rules     RulesInput
}

type PublicRules struct {
	Categories      []string `json:"categories"`
	Authors         []string `json:"authors"`
	IncludeKeywords []string `json:"include_keywords"`
	ExcludeKeywords []string `json:"exclude_keywords"`
}

type PublicSubscription struct {
	ID        uint64              `json:"id"`
	Source    source.PublicSource `json:"source"`
	Name      string              `json:"name"`
	Objective *string             `json:"objective"`
	Enabled   bool                `json:"enabled"`
	Version   uint32              `json:"version"`
	Rules     PublicRules         `json:"rules"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}
