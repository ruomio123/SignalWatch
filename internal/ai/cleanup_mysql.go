package ai

import (
	"context"
	"signalwatch/internal/insight"
	"time"
)

func (r *Repository) Cleanup(ctx context.Context, now time.Time) error {
	// Retention starts when a version is observed obsolete, not when it was generated.
	// Bound each deletion; other passes continue the work.
	if err := r.DB.WithContext(ctx).Exec("DELETE FROM paper_ai_summaries WHERE expires_at<? LIMIT 100", now).Error; err != nil {
		return err
	}
	if err := r.DB.WithContext(ctx).Exec("DELETE FROM digest_ai_summaries WHERE expires_at<? LIMIT 100", now).Error; err != nil {
		return err
	}
	var rows []struct {
		ID             uint64
		InputHash      string
		Profile        string
		ConfigVersion  uint64
		CurrentVersion *uint64
		Title          string
		Abstract       string
	}
	err := r.DB.WithContext(ctx).Table("paper_ai_summaries a").Select("a.id,a.input_hash,a.profile,a.config_version,ac.config_version AS current_version,p.title,p.abstract").Joins("JOIN papers p ON p.id=a.scope_id").Joins("LEFT JOIN user_ai_configurations ac ON ac.user_id=a.owner_user_id AND ac.is_default=1").Where("a.expires_at IS NULL AND a.updated_at<?", now.Add(-time.Minute)).Order("a.updated_at,a.id").Limit(100).Scan(&rows).Error
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.CurrentVersion == nil || row.ConfigVersion != *row.CurrentVersion || row.InputHash != insight.PaperHash(insight.Paper{Title: row.Title, Abstract: row.Abstract}) {
			if err := r.DB.WithContext(ctx).Table(table(PaperKind)).Where("id=? AND expires_at IS NULL", row.ID).UpdateColumn("expires_at", now.Add(30*24*time.Hour)).Error; err != nil {
				return err
			}
		} else {
			// Move retained current rows past the next cleanup page to avoid starvation.
			if err := r.DB.WithContext(ctx).Table(table(PaperKind)).Where("id=?", row.ID).Update("updated_at", now).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
