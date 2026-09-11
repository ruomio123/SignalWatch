package digest

import "time"

const localDateLayout = "2006-01-02"

type Schedule struct {
	LocalDate         string `gorm:"column:local_date"`
	SubscriptionID    uint64 `gorm:"column:subscription_id"`
	UserID            uint64 `gorm:"column:user_id"`
	Email             string `gorm:"column:email"`
	Timezone          string `gorm:"column:timezone"`
	DigestTime        string `gorm:"column:digest_time"`
	MaxItemsPerDigest uint16 `gorm:"column:max_items_per_digest"`
}

type Job struct {
	SubscriptionID uint64
	UserID         uint64
	LocalDate      string
}

type Match struct {
	SubscriptionID   uint64
	SubscriptionName string
	Category         string
	MatchedKeywords  []string
}

type Item struct {
	PaperID        uint64
	ArXivID        string
	Title          string
	Abstract       string
	Comments       string
	Authors        []string
	Categories     []string
	PublishedAt    time.Time
	ArXivUpdatedAt time.Time
	ArXivURL       string
	PDFURL         string
	FirstSeenAt    time.Time
	Matches        []Match
}

type User struct {
	SubscriptionID    uint64
	SubscriptionName  string
	DigestAIEnabled   bool
	DigestAILanguage  string
	ID                uint64
	Email             string
	Timezone          string
	MaxItemsPerDigest uint16
}

type ProcessResult struct {
	Retried         bool
	Locked          bool
	AlreadyComplete bool
	Stale           bool
	Empty           bool
	Items           int
	MarkedRelations int64
}
