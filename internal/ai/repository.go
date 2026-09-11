package ai

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct{ DB *gorm.DB }

func table(kind string) string {
	switch kind {
	case PaperKind:
		return "paper_ai_summaries"
	case DigestKind:
		return "digest_ai_summaries"
	default:
		panic("invalid AI task kind")
	}
}
func (r *Repository) Ensure(ctx context.Context, t *Task) error {
	err := r.DB.WithContext(ctx).Table(table(t.Kind)).Clauses(clause.OnConflict{DoNothing: true}).Create(t).Error
	if err != nil {
		return err
	}
	// Reprioritize existing demand; revive work cancelled before preferences or
	// candidates became relevant again, without resetting paid attempts.
	if err := r.identity(ctx, *t).Where("attempts=0 AND failure_code='obsolete'").Updates(map[string]any{"status": "pending", "next_retry_at": t.NextRetryAt, "failure_code": ""}).Error; err != nil {
		return err
	}
	if err := r.identity(ctx, *t).Where("priority>?", t.Priority).UpdateColumn("priority", t.Priority).Error; err != nil {
		return err
	}
	if t.Kind == PaperKind {
		if err := r.identity(ctx, *t).Where("expires_at IS NOT NULL").UpdateColumn("expires_at", nil).Error; err != nil {
			return err
		}
	}
	kind := t.Kind
	key := *t
	*t = Task{}
	err = r.identity(ctx, key).Take(t).Error
	t.Kind = kind
	return err
}
func (r *Repository) identity(ctx context.Context, t Task) *gorm.DB {
	query := r.DB.WithContext(ctx).Table(table(t.Kind))
	if t.Kind == DigestKind {
		return query.Where("owner_user_id=? AND subscription_id=? AND local_date=? AND language=? AND config_generation=? AND config_version=?", t.OwnerUserID, t.SubscriptionID, t.LocalDate, t.Language, t.ConfigGeneration, t.ConfigVersion)
	}
	return query.Where("owner_user_id=? AND scope_id=? AND local_date=? AND input_hash=? AND language=? AND profile=? AND config_generation=? AND config_version=?", t.OwnerUserID, t.ScopeID, t.LocalDate, t.InputHash, t.Language, t.Profile, t.ConfigGeneration, t.ConfigVersion)
}
func (r *Repository) Find(ctx context.Context, t Task) (Task, error) {
	var found Task
	err := r.identity(ctx, t).Take(&found).Error
	found.Kind = t.Kind
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrTaskNotFound
	}
	return found, err
}

func (r *Repository) Retry(ctx context.Context, t Task, now time.Time) error {
	result := r.identity(ctx, t).Where("status='failed' AND next_retry_at<=?", now).Updates(map[string]any{
		"status": "pending", "attempts": 0, "next_retry_at": now, "failure_code": "",
		"lease_owner": "", "lease_until": nil, "updated_at": now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (r *Repository) Requeue(ctx context.Context, t Task, next, now time.Time, code string) error {
	result := r.DB.WithContext(ctx).Table(table(t.Kind)).Where("id=? AND status='processing' AND lease_owner=?", t.ID, t.LeaseOwner).Updates(map[string]any{
		"status": "pending", "next_retry_at": next, "failure_code": code, "lease_owner": "", "lease_until": nil, "updated_at": now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (r *Repository) Pending(ctx context.Context, kind, profile string, now time.Time, limit int) ([]Task, error) {
	if err := r.DB.WithContext(ctx).Table(table(kind)).Where("status='processing' AND attempts>=1 AND lease_until<=?", now).Updates(map[string]any{"status": "failed", "failure_code": "lease_expired", "lease_owner": "", "lease_until": nil, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	var rows []Task
	query := r.DB.WithContext(ctx).Table(table(kind)).Where("owner_user_id IS NOT NULL AND config_version>0 AND config_generation<>'' AND attempts<1 AND (expires_at IS NULL OR expires_at>?) AND ((status='pending' AND next_retry_at<=?) OR (status='processing' AND lease_until<=?))", now, now, now)
	if profile != "" {
		query = query.Where("profile=?", profile)
	}
	err := query.Order("priority ASC, next_retry_at ASC, id ASC").Limit(limit).Find(&rows).Error
	for i := range rows {
		rows[i].Kind = kind
	}
	return rows, err
}
func (r *Repository) Claim(ctx context.Context, t Task, now time.Time) (Task, bool, error) {
	owner := rand.Text()
	until := now.Add(time.Minute)
	result := r.DB.WithContext(ctx).Table(table(t.Kind)).Where("id=? AND attempts<1 AND ((status='pending' AND next_retry_at<=?) OR (status='processing' AND lease_until<=?))", t.ID, now, now).Updates(map[string]any{"status": "processing", "lease_owner": owner, "lease_until": until, "updated_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		return t, false, result.Error
	}
	err := r.DB.WithContext(ctx).Table(table(t.Kind)).Where("id=?", t.ID).Take(&t).Error
	return t, err == nil, err
}

// MarkAttempt prevents an interrupted task from automatically calling again.
// Billing admission and accounting belong to CallStore.
func (r *Repository) MarkAttempt(ctx context.Context, t Task, now time.Time) (bool, error) {
	reserved := false
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owned Task
		if err := tx.Table(table(t.Kind)).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND lease_owner=? AND status='processing' AND attempts=0 AND lease_until>?", t.ID, t.LeaseOwner, now).Take(&owned).Error; err != nil {
			return err
		}
		if err := tx.Table(table(t.Kind)).Where("id=?", t.ID).UpdateColumn("attempts", gorm.Expr("attempts+1")).Error; err != nil {
			return err
		}
		reserved = true
		return nil
	})
	return reserved, err
}
func (r *Repository) Finish(ctx context.Context, t Task, payload []byte, code string, next time.Time, terminal bool, now time.Time) error {
	state := "ready"
	var raw any = json.RawMessage(payload)
	if code != "" {
		state = "failed"
		raw = nil
	}
	updates := map[string]any{"status": state, "payload_json": raw, "failure_code": code, "next_retry_at": next, "updated_at": now, "lease_owner": "", "lease_until": nil}
	if terminal {
		updates["attempts"] = 1
	}
	q := r.DB.WithContext(ctx).Table(table(t.Kind))
	if id := activeCall(ctx); id != "" {
		q = q.Where("EXISTS (SELECT 1 FROM ai_call_admission WHERE lease_token=? AND lease_until>?)", id, now)
	}
	q = q.Where("id=? AND status='processing' AND lease_owner=? AND lease_until>?", t.ID, t.LeaseOwner, now).Updates(updates)
	if q.Error != nil {
		return q.Error
	}
	if q.RowsAffected == 0 {
		return ErrLeaseLost
	}
	return nil
}
