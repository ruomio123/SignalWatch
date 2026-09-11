package ai

import (
	"context"
	"gorm.io/gorm"
	"time"
)

func (s *MySQLConfigurationStore) MarkInvalid(ctx context.Context, userID, version uint64) error {
	return s.db.WithContext(ctx).Model(&Configuration{}).Where("user_id=? AND config_version=?", userID, version).Update("status", ConfigurationInvalid).Error
}

func (s *MySQLConfigurationStore) MarkUsed(ctx context.Context, userID, version uint64, now time.Time) error {
	return s.db.WithContext(ctx).Model(&Configuration{}).Where("user_id=? AND config_version=?", userID, version).Update("last_used_at", now.UTC()).Error
}

// The counter survives configuration deletion, so stale internal commands also
// remain invalid even when callers only carry the numeric revision.
func nextConfigurationRevision(tx *gorm.DB, userID uint64, revision *uint64) error {
	if err := tx.Exec("INSERT INTO ai_configuration_counters(user_id,revision) VALUES (?,0) ON DUPLICATE KEY UPDATE user_id=user_id", userID).Error; err != nil {
		return err
	}
	if err := tx.Exec("UPDATE ai_configuration_counters SET revision=revision+1 WHERE user_id=?", userID).Error; err != nil {
		return err
	}
	return tx.Table("ai_configuration_counters").Select("revision").Where("user_id=?", userID).Scan(revision).Error
}
