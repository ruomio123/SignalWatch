package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrDraftConflict = errors.New("SUBSCRIPTION_DRAFT_CONFLICT")

type Draft struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversation_id"`
	UserID         uint64          `json:"-"`
	Version        uint32          `json:"version"`
	Payload        json.RawMessage `json:"payload"`
	SubscriptionID *uint64         `json:"subscription_id,omitempty"`
	ExpiresAt      time.Time       `json:"expires_at"`
	CreatedAt      time.Time       `json:"created_at"`
}

func (Draft) TableName() string { return "agent_subscription_drafts" }

type DraftReader interface {
	ReadDraft(context.Context, uint64, string) (Draft, error)
}
type draftReference struct {
	ID      string
	Version uint32
	Payload string
}
type draftKey struct{}
type draftTx interface {
	LockDraft(context.Context, uint64, draftReference, time.Time) (*Subscription, error)
	ConfirmDraft(context.Context, string, uint64) error
}

func (s *Service) ConfirmDraft(ctx context.Context, uid uint64, id string, version uint32) (PublicSubscription, error) {
	reader, ok := s.repository.(DraftReader)
	if !ok {
		return PublicSubscription{}, ErrDraftConflict
	}
	d, err := reader.ReadDraft(ctx, uid, id)
	if err != nil {
		return PublicSubscription{}, err
	}
	if d.Version != version {
		return PublicSubscription{}, ErrDraftConflict
	}
	if d.SubscriptionID != nil {
		return s.Get(ctx, uid, *d.SubscriptionID)
	}
	var input CreateInput
	if json.Unmarshal(d.Payload, &input) != nil {
		return PublicSubscription{}, ErrDraftConflict
	}
	return s.Create(context.WithValue(ctx, draftKey{}, draftReference{id, version, string(d.Payload)}), uid, input)
}

// ValidateDraft uses exactly the normalization and validation used by Create.
func (s *Service) ValidateDraft(ctx context.Context, uid uint64, input CreateInput) (CreateInput, error) {
	_, _, rules, err := s.prepareCreate(ctx, uid, input)
	if err != nil {
		return input, err
	}
	input.Rules = rules
	return input, nil
}
