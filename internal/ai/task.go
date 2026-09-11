package ai

import (
	"encoding/json"
	"errors"
	"signalwatch/internal/insight"
	"time"
)

const PaperKind = "paper"
const DigestKind = "digest"

type Input struct {
	SubscriptionID uint64          `json:"subscription_id,omitempty"`
	Papers         []insight.Paper `json:"papers"`
	Timezone       string          `json:"timezone,omitempty"`
	Limit          int             `json:"limit,omitempty"`
}
type Task struct {
	ID               uint64
	Kind             string `gorm:"-"`
	ScopeID          uint64
	OwnerUserID      uint64
	SubscriptionID   uint64
	LocalDate        string
	InputHash        string
	Language         string
	ProviderID       string
	ModelID          string
	ConfigGeneration string
	ConfigVersion    uint64
	Profile          string
	InputJSON        json.RawMessage `gorm:"column:input_json"`
	Status           string
	PayloadJSON      json.RawMessage `gorm:"column:payload_json"`
	Priority         int
	Attempts         int
	NextRetryAt      time.Time
	LeaseOwner       string
	LeaseUntil       *time.Time
	FailureCode      string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ExpiresAt        *time.Time
}

func newUserTask(kind string, owner, id uint64, date, lang, provider, model string, generation string, configVersion uint64, profile string, input Input, priority int, now time.Time) Task {
	hash := insight.Hash(input)
	if kind == PaperKind {
		hash = insight.PaperHash(input.Papers[0])
	}
	raw, _ := json.Marshal(input)
	t := Task{Kind: kind, OwnerUserID: owner, ScopeID: id, SubscriptionID: input.SubscriptionID, LocalDate: date, InputHash: hash, Language: lang, ProviderID: provider, ModelID: model, ConfigGeneration: generation, ConfigVersion: configVersion, Profile: profile, InputJSON: raw, Status: "pending", Priority: priority, NextRetryAt: now, CreatedAt: now, UpdatedAt: now}
	if kind == DigestKind {
		expires := now.Add(7 * 24 * time.Hour)
		t.ExpiresAt = &expires
	}
	return t
}

var ErrDisabled = errors.New("AI_DISABLED")
var ErrLanguage = errors.New("invalid AI language")

var ErrLeaseLost = errors.New("AI lease lost")
