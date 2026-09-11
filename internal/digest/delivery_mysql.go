package digest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type MySQLDeliveryStore struct{ db *gorm.DB }

func NewMySQLDeliveryStore(db *gorm.DB) *MySQLDeliveryStore { return &MySQLDeliveryStore{db} }
func (s *MySQLDeliveryStore) Claim(ctx context.Context, job Job) (d Delivery, acquired bool, err error) {
	if err = validateJob(job); err != nil {
		return
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("INSERT INTO digest_deliveries(user_id,subscription_id,local_date,next_retry_at,created_at,updated_at) SELECT s.user_id,s.id,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6),UTC_TIMESTAMP(6) FROM subscriptions s JOIN users u ON u.id=s.user_id WHERE s.id=? AND s.user_id=? ON DUPLICATE KEY UPDATE id=digest_deliveries.id", job.LocalDate, job.SubscriptionID, job.UserID).Error; err != nil {
			return err
		}
		if err := tx.Table("digest_deliveries").Clauses(clause.Locking{Strength: "UPDATE"}).Where("subscription_id=? AND local_date=? AND user_id=?", job.SubscriptionID, job.LocalDate, job.UserID).Take(&d).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUserNotFound
			}
			return err
		}
		d.Job = job
		if d.State == "sent" || d.State == "empty" || d.State == "cancelled" {
			return nil
		}
		if err := tx.Exec("UPDATE digest_deliveries SET state='failed',failure_code='lease_expired',lease_owner='',lease_until=NULL WHERE id=? AND state='sending' AND attempts>=5 AND lease_until<=UTC_TIMESTAMP(6)", d.ID).Error; err != nil {
			return err
		}
		token := rand.Text()
		result := tx.Table("digest_deliveries").Where("id=? AND attempts<5 AND ((state IN ('pending','retry') AND next_retry_at<=UTC_TIMESTAMP(6)) OR (state='sending' AND lease_until<=UTC_TIMESTAMP(6)))", d.ID).Updates(map[string]any{"state": "sending", "attempts": gorm.Expr("attempts+1"), "lease_owner": token, "lease_until": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 60 SECOND)"), "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")})
		if result.Error != nil {
			return result.Error
		}
		acquired = result.RowsAffected == 1
		if acquired {
			d.LeaseOwner = token
			d.Attempts++
		}
		return nil
	})
	return
}
func ownedDelivery(tx *gorm.DB, d Delivery) *gorm.DB {
	return tx.Table("digest_deliveries").Where("id=? AND state='sending' AND lease_owner=? AND lease_until>UTC_TIMESTAMP(6)", d.ID, d.LeaseOwner)
}
func (s *MySQLDeliveryStore) Freeze(ctx context.Context, d Delivery, snap Snapshot) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Delivery
		if err := ownedDelivery(tx, d).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLeaseLost
			}
			return err
		}
		if len(current.SnapshotJSON) > 0 {
			return errors.New("digest snapshot already frozen")
		}
		for _, id := range snap.PaperIDs {
			if err := tx.Exec("INSERT INTO digest_delivery_items(subscription_id,paper_id,delivery_id) VALUES(?,?,?)", d.SubscriptionID, id, d.ID).Error; err != nil {
				return err
			}
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		return ownedDelivery(tx, d).Update("snapshot_json", string(raw)).Error
	})
}
func (s *MySQLDeliveryStore) Finish(ctx context.Context, d Delivery, snap Snapshot, now time.Time) (int64, error) {
	var marked int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Delivery
		if err := ownedDelivery(tx, d).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLeaseLost
			}
			return err
		}
		state := "empty"
		if len(snap.PaperIDs) > 0 {
			state = "sent"
			result := tx.Exec("UPDATE subscription_papers sp JOIN digest_delivery_items di ON di.subscription_id=sp.subscription_id AND di.paper_id=sp.paper_id SET sp.delivered_at=? WHERE di.delivery_id=? AND sp.delivered_at IS NULL", now, d.ID)
			if result.Error != nil {
				return result.Error
			}
			marked = result.RowsAffected
		}
		return ownedDelivery(tx, d).Updates(map[string]any{"state": state, "lease_owner": "", "lease_until": nil, "failure_code": "", "updated_at": now}).Error
	})
	return marked, err
}
func (s *MySQLDeliveryStore) Fail(ctx context.Context, d Delivery) error {
	return s.db.WithContext(ctx).Exec("UPDATE digest_deliveries SET state=IF(attempts>=5,'failed','retry'),failure_code='delivery_failed',lease_owner='',lease_until=NULL,next_retry_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL LEAST(900,60*POW(2,attempts-1)) SECOND),updated_at=UTC_TIMESTAMP(6) WHERE id=? AND lease_owner=? AND state='sending'", d.ID, d.LeaseOwner).Error
}
func (s *MySQLDeliveryStore) IsComplete(ctx context.Context, j Job) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Table("digest_deliveries").Where("user_id=? AND subscription_id=? AND local_date=? AND state IN ('sent','empty','cancelled')", j.UserID, j.SubscriptionID, j.LocalDate).Count(&count).Error
	return count > 0, err
}

// Retry is an explicit operational action; it preserves the frozen content.
func (s *MySQLDeliveryStore) Retry(ctx context.Context, id uint64) error {
	result := s.db.WithContext(ctx).Table("digest_deliveries").Where("id=? AND state='failed'", id).Updates(map[string]any{"state": "retry", "attempts": 0, "next_retry_at": gorm.Expr("UTC_TIMESTAMP(6)"), "failure_code": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("failed digest delivery not found")
	}
	return nil
}

func (s *MySQLDeliveryStore) Cancel(ctx context.Context, d Delivery) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := ownedDelivery(tx, d).Updates(map[string]any{"state": "cancelled", "lease_owner": "", "lease_until": nil, "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrLeaseLost
		}
		return tx.Exec("DELETE FROM digest_delivery_items WHERE delivery_id=?", d.ID).Error
	})
}
