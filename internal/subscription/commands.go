package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

const maxEnabledSubscriptions = int64(20)

var (
	ErrLimitReached    = errors.New("subscription limit reached")
	ErrUserNotFound    = errors.New("active user not found")
	ErrNotFound        = errors.New("subscription not found")
	ErrVersionConflict = errors.New("subscription version conflict")
)

type BackfillWindow struct{ From, To, MatchedAt time.Time }
type Tx interface {
	LockUser(context.Context, uint64) (uint16, error)
	CountEnabled(context.Context, uint64) (int64, error)
	Insert(context.Context, *Subscription) error
	LockSubscription(context.Context, uint64, uint64) (Subscription, error)
	Update(context.Context, *Subscription, SubscriptionPatch) error
	Enqueue(context.Context, *Subscription, BackfillWindow) error
}
type Repository interface {
	Transact(context.Context, func(Tx) error) error
	Count(context.Context, uint64, ListFilter) (int64, error)
	List(context.Context, uint64, ListFilter, int, int) ([]QueryResult, error)
	Get(context.Context, uint64, uint64) (QueryResult, error)
	SoftDelete(context.Context, uint64, uint64, uint32) error
}

func (s *Service) createAtomic(ctx context.Context, sub *Subscription, window BackfillWindow) error {
	return s.repository.Transact(ctx, func(tx Tx) error {
		limit, err := tx.LockUser(ctx, sub.UserID)
		if err != nil {
			return err
		}
		if ref, ok := ctx.Value(draftKey{}).(draftReference); ok {
			dtx, ok := tx.(draftTx)
			if !ok {
				return ErrDraftConflict
			}
			existing, err := dtx.LockDraft(ctx, sub.UserID, ref, window.To)
			if err != nil {
				return err
			}
			if existing != nil {
				*sub = *existing
				return nil
			}
		}
		if sub.MaxItemsPerDigest == 0 {
			sub.MaxItemsPerDigest = limit
		}
		if sub.Enabled {
			count, err := tx.CountEnabled(ctx, sub.UserID)
			if err != nil {
				return err
			}
			if count >= maxEnabledSubscriptions {
				return ErrLimitReached
			}
		}
		sub.CreatedAt = window.To
		sub.UpdatedAt = window.To
		if err := tx.Insert(ctx, sub); err != nil {
			return err
		}
		if sub.Enabled {
			if err := tx.Enqueue(ctx, sub, window); err != nil {
				return err
			}
		}
		if ref, ok := ctx.Value(draftKey{}).(draftReference); ok {
			return tx.(draftTx).ConfirmDraft(ctx, ref.ID, sub.ID)
		}
		return nil
	})
}
func (s *Service) updateAtomic(ctx context.Context, uid, id uint64, version uint32, patch SubscriptionPatch, rule *RulesInput) (updated Subscription, err error) {
	err = s.repository.Transact(ctx, func(tx Tx) error {
		// All quota-changing commands lock user before subscription.
		if _, err := tx.LockUser(ctx, uid); err != nil {
			return err
		}
		current, err := tx.LockSubscription(ctx, uid, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrVersionConflict
		}
		changed := patchChanges(current, patch)
		if rule != nil {
			var old []string
			if err := json.Unmarshal(current.KeywordsJSON, &old); err != nil {
				return ErrInvalidStoredRule
			}
			if rule.Category != current.Category || !slices.Equal(old, rule.Keywords) {
				changed = true
				raw, _ := json.Marshal(rule.Keywords)
				value := json.RawMessage(raw)
				patch.Category = &rule.Category
				patch.KeywordsJSON = &value
			}
		}
		if !changed {
			updated = current
			return nil
		}
		if patch.Enabled != nil && *patch.Enabled && !current.Enabled {
			count, err := tx.CountEnabled(ctx, uid)
			if err != nil {
				return err
			}
			if count >= maxEnabledSubscriptions {
				return ErrLimitReached
			}
		}
		if err := tx.Update(ctx, &current, patch); err != nil {
			return err
		}
		updated = current
		return nil
	})
	return
}
func patchChanges(c Subscription, p SubscriptionPatch) bool {
	if p.Name != nil && c.Name != *p.Name {
		return true
	}
	if p.ObjectiveSet && !sameOptional(c.Objective, p.Objective) {
		return true
	}
	return p.Enabled != nil && c.Enabled != *p.Enabled || p.MaxItemsPerDigest != nil && c.MaxItemsPerDigest != *p.MaxItemsPerDigest || p.DigestAIEnabled != nil && c.DigestAIEnabled != *p.DigestAIEnabled || p.DigestAILanguage != nil && c.DigestAILanguage != *p.DigestAILanguage
}
func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
