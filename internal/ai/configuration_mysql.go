package ai

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type MySQLConfigurationStore struct {
	db *gorm.DB
}

func NewMySQLConfigurationStore(db *gorm.DB) *MySQLConfigurationStore {
	return &MySQLConfigurationStore{db: db}
}
func (s *MySQLConfigurationStore) Read(ctx context.Context, uid uint64) (c Configuration, err error) {
	err = s.db.WithContext(ctx).Where("user_id=?", uid).Take(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrConfigurationRequired
	}
	return
}
func lockConfigurationUser(tx *gorm.DB, uid uint64) error {
	var row struct{ ID uint64 }
	return tx.Table("users").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id=? AND status='active' AND role='user'", uid).Take(&row).Error
}
func (s *MySQLConfigurationStore) Mutate(ctx context.Context, uid uint64, expected *uint64, fn func(Configuration, uint64) (Configuration, error)) (saved Configuration, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The global command lock order is user -> configuration -> subscription.
		if err := lockConfigurationUser(tx, uid); err != nil {
			return err
		}
		if err := checkCallLease(tx, activeCall(ctx), activeCallTime(ctx)); err != nil {
			return err
		}
		var current Configuration
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", uid).Take(&current).Error
		missing := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !missing {
			return err
		}
		if missing && expected != nil || !missing && (expected == nil || *expected != current.ConfigVersion) {
			return ErrConfigurationConflict
		}
		var revision uint64
		if err := nextConfigurationRevision(tx, uid, &revision); err != nil {
			return err
		}
		saved, err = fn(current, revision)
		if err != nil {
			return err
		}
		if missing {
			return tx.Create(&saved).Error
		}
		return tx.Save(&saved).Error
	})
	return
}
func (s *MySQLConfigurationStore) Delete(ctx context.Context, uid, version uint64, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockConfigurationUser(tx, uid); err != nil {
			return err
		}
		result := tx.Where("user_id=? AND config_version=?", uid, version).Delete(&Configuration{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConfigurationConflict
		}
		if err := tx.Table("users").Where("id=?", uid).Updates(map[string]any{"ai_enabled": false, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Table("subscriptions").Where("user_id=? AND digest_ai_enabled=1", uid).Updates(map[string]any{"digest_ai_enabled": false, "updated_at": now, "version": gorm.Expr("version+1")}).Error
	})
}
func (s *MySQLConfigurationStore) MarkTested(ctx context.Context, u, v uint64, now time.Time) error {
	q := s.db.WithContext(ctx).Model(&Configuration{})
	if id := activeCall(ctx); id != "" {
		q = q.Where("EXISTS (SELECT 1 FROM ai_call_admission WHERE lease_token=? AND lease_until>?)", id, now)
	}
	r := q.Where("user_id=? AND config_version=?", u, v).Updates(map[string]any{"status": ConfigurationActive, "last_tested_at": now, "updated_at": now})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrConfigurationConflict
	}
	return nil
}
