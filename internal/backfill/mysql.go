package backfill

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"slices"
	"time"
)

var ErrLeaseLost = errors.New("backfill lease lost")

type MySQLStore struct{ db *gorm.DB }

func NewMySQLStore(db *gorm.DB) *MySQLStore { return &MySQLStore{db} }
func (s *MySQLStore) Claim(ctx context.Context) (task Task, ok bool, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Table("subscription_backfills").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("(state='pending' AND next_retry_at<=UTC_TIMESTAMP(6)) OR (state='processing' AND lease_until<=UTC_TIMESTAMP(6))").Order("next_retry_at,subscription_id").Take(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		task.LeaseOwner = rand.Text()
		if err := tx.Table("subscription_backfills").Where("subscription_id=?", task.SubscriptionID).Updates(map[string]any{"state": "processing", "lease_owner": task.LeaseOwner, "lease_until": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 SECOND)"), "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")}).Error; err != nil {
			return err
		}
		ok = true
		return nil
	})
	return
}
func (s *MySQLStore) Papers(ctx context.Context, t Task, limit int) ([]Paper, error) {
	var rows []Paper
	err := s.db.WithContext(ctx).Table("papers USE INDEX (idx_papers_source_cursor)").Select("id,title,abstract,categories_json").Where("source_id=? AND id>? AND published_at BETWEEN ? AND ? AND first_seen_at<=?", t.SourceID, t.CursorID, t.WindowFrom, t.WindowTo, t.WindowTo).Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}
func (s *MySQLStore) Commit(ctx context.Context, t Task, cursor uint64, processed int, matches []Match, complete bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owned Task
		if err := tx.Table("subscription_backfills").Clauses(clause.Locking{Strength: "UPDATE"}).Where("subscription_id=? AND lease_owner=? AND state='processing' AND lease_until>UTC_TIMESTAMP(6)", t.SubscriptionID, t.LeaseOwner).Take(&owned).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLeaseLost
			}
			return err
		}
		var subscription struct {
			Enabled      bool
			DeletedAt    *time.Time
			Category     string
			KeywordsJSON json.RawMessage
		}
		if err := tx.Table("subscriptions").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", t.SubscriptionID).Take(&subscription).Error; err != nil {
			return err
		}
		var oldKeywords, newKeywords []string
		if err := json.Unmarshal(subscription.KeywordsJSON, &newKeywords); err != nil {
			return err
		}
		if err := json.Unmarshal(t.KeywordsJSON, &oldKeywords); err != nil {
			return err
		}
		active := subscription.Enabled && subscription.DeletedAt == nil && subscription.Category == t.Category && slices.Equal(oldKeywords, newKeywords)

		state := "pending"
		if complete {
			state = "complete"
		}
		if !active {
			state = "cancelled"
			matches = nil
		}
		for _, m := range matches {
			raw, err := json.Marshal(m.Keywords)
			if err != nil {
				return err
			}
			if err := tx.Exec("INSERT INTO subscription_papers(subscription_id,paper_id,matched_keywords_json,matched_at) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE subscription_id=subscription_id", t.SubscriptionID, m.PaperID, string(raw), t.WindowTo).Error; err != nil {
				return err
			}
		}
		return tx.Table("subscription_backfills").Where("subscription_id=?", t.SubscriptionID).Updates(map[string]any{"state": state, "cursor_id": cursor, "processed": gorm.Expr("processed+?", processed), "matched": gorm.Expr("matched+?", len(matches)), "attempts": 0, "failure_code": "", "lease_owner": "", "lease_until": nil, "next_retry_at": gorm.Expr("UTC_TIMESTAMP(6)"), "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")}).Error
	})
}
func (s *MySQLStore) Fail(ctx context.Context, t Task) error {
	return s.db.WithContext(ctx).Exec("UPDATE subscription_backfills SET state=IF(attempts>=4,'failed','pending'),attempts=attempts+1,failure_code='batch_failed',lease_owner='',lease_until=NULL,next_retry_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 1 MINUTE),updated_at=UTC_TIMESTAMP(6) WHERE subscription_id=? AND lease_owner=? AND state='processing'", t.SubscriptionID, t.LeaseOwner).Error
}

// Retry resumes the existing cursor; it never resets already committed batches.
func (s *MySQLStore) Retry(ctx context.Context, subscriptionID uint64) error {
	r := s.db.WithContext(ctx).Exec("UPDATE subscription_backfills SET state='pending',attempts=0,failure_code='',next_retry_at=UTC_TIMESTAMP(6) WHERE subscription_id=? AND state='failed'", subscriptionID)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return errors.New("failed backfill not found")
	}
	return nil
}
