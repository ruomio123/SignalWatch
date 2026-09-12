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
	err = readConfiguration(configurationScope(s.db.WithContext(ctx), ctx, uid), &c)
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
		err := readConfiguration(configurationScope(tx.Clauses(clause.Locking{Strength: "UPDATE"}), ctx, uid), &current)
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
		if _, scoped := ctx.Value(credentialIDScopeKey{}).(string); scoped && (missing || current.ProviderID != saved.ProviderID) {
			return ErrConfigurationConflict
		}
		if provider, scoped := ctx.Value(credentialScopeKey{}).(string); scoped {
			if provider != saved.ProviderID {
				return ErrConfigurationConflict
			}
			value := !missing && (current.IsDefault == nil || *current.IsDefault)
			saved.IsDefault = &value
		}
		if ctx.Value(credentialCreateScopeKey{}) != nil {
			var count int64
			if err := tx.Model(&Configuration{}).Where("user_id=?", uid).Count(&count).Error; err != nil {
				return err
			}
			value := count == 0
			saved.IsDefault = &value
		}
		if !missing && current.Generation != saved.Generation {
			if err := tx.Model(&Configuration{}).Where("user_id=? AND generation=?", uid, current.Generation).Update("is_default", false).Error; err != nil {
				return err
			}
			return tx.Create(&saved).Error
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
		var current Configuration
		if err := configurationScope(tx, ctx, uid).Where("config_version=?", version).Take(&current).Error; err != nil {
			return ErrConfigurationConflict
		}
		result := configurationScope(tx, ctx, uid).Where("config_version=?", version).Delete(&Configuration{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConfigurationConflict
		}
		if current.IsDefault != nil && !*current.IsDefault {
			return nil
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

func configurationScope(db *gorm.DB, ctx context.Context, uid uint64) *gorm.DB {
	q := db.Where("user_id=?", uid)
	if ctx.Value(credentialCreateScopeKey{}) != nil {
		return q.Where("1=0")
	}
	if id, ok := ctx.Value(credentialIDScopeKey{}).(string); ok {
		return q.Where("generation=?", id)
	}
	if provider, ok := ctx.Value(credentialScopeKey{}).(string); ok {
		return q.Where("provider_id=?", provider)
	}
	return q.Where("is_default=1")
}
func (s *MySQLConfigurationStore) ListCredentials(ctx context.Context, uid uint64) ([]Configuration, error) {
	rows := []Configuration{}
	err := s.db.WithContext(ctx).Where("user_id=?", uid).Order("created_at DESC, generation").Find(&rows).Error
	return rows, err
}
func (s *MySQLConfigurationStore) SelectDefault(ctx context.Context, uid uint64, provider, generation string, version uint64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockConfigurationUser(tx, uid); err != nil {
			return err
		}
		var row Configuration
		if err := tx.Where("user_id=? AND provider_id=? AND generation=? AND config_version=? AND status='active'", uid, provider, generation, version).Take(&row).Error; err != nil {
			return ErrConfigurationConflict
		}
		if err := tx.Model(&Configuration{}).Where("user_id=? AND is_default=1", uid).Update("is_default", false).Error; err != nil {
			return err
		}
		return tx.Model(&Configuration{}).Where("user_id=? AND generation=?", uid, generation).Update("is_default", true).Error
	})
}

// Legacy provider selectors are accepted only when unambiguous. Never pick an
// arbitrary key after a user saves several credentials for the same provider.
func readConfiguration(q *gorm.DB, out *Configuration) error {
	var rows []Configuration
	if err := q.Limit(2).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return gorm.ErrRecordNotFound
	}
	if len(rows) != 1 {
		return ErrConfigurationConflict
	}
	*out = rows[0]
	return nil
}
