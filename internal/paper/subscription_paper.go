package paper

import (
	"encoding/json"
	"time"
)

// SubscriptionPaper is the durable, idempotent result of matching one paper
// to one subscription. Matching and delivery services are implemented later.
type SubscriptionPaper struct {
	ID                  uint64          `gorm:"column:id;primaryKey;autoIncrement"`
	SubscriptionID      uint64          `gorm:"column:subscription_id"`
	PaperID             uint64          `gorm:"column:paper_id"`
	MatchedKeywordsJSON json.RawMessage `gorm:"column:matched_keywords_json"`
	MatchedAt           time.Time       `gorm:"column:matched_at"`
	DeliveredAt         *time.Time      `gorm:"column:delivered_at"`
}

func (SubscriptionPaper) TableName() string { return "subscription_papers" }
