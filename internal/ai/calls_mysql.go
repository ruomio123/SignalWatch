package ai

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"signalwatch/internal/generation"
	"time"
)

type MySQLCallStore struct{ db *gorm.DB }

func NewMySQLCallStore(db *gorm.DB) *MySQLCallStore { return &MySQLCallStore{db: db} }

type admission struct {
	UserID                                     uint64 `gorm:"primaryKey"`
	LeaseToken                                 string
	LeaseUntil, ConfigNextAt, GenerationNextAt time.Time
}

func (admission) TableName() string { return "ai_call_admission" }

// callStorageTime matches the DATETIME(6) precision before both persistence and
// comparison. Letting MySQL round nanoseconds can move an admission into the future.
func callStorageTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func (s *MySQLCallStore) Admit(ctx context.Context, c CallRecord, p CallPolicy) error {
	c.CreatedAt = callStorageTime(c.CreatedAt)
	c.LeaseUntil = callStorageTime(c.LeaseUntil)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A new user has no preceding call, so neither feature starts in cooldown.
		unset := time.Unix(0, 0).UTC()
		empty := admission{UserID: c.UserID, LeaseUntil: unset, ConfigNextAt: unset, GenerationNextAt: unset}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&empty).Error; err != nil {
			return err
		}
		var a admission
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", c.UserID).Take(&a).Error; err != nil {
			return err
		}
		if a.LeaseToken != "" && a.LeaseUntil.After(c.CreatedAt) {
			return &CallError{Code: "call_in_progress", RetryAt: &a.LeaseUntil}
		}
		next := a.GenerationNextAt
		column := "generation_next_at"
		if c.Feature == FeatureConfigTest {
			next = a.ConfigNextAt
			column = "config_next_at"
		}
		if next.After(c.CreatedAt) {
			return &CallError{Code: "rate_limited", RetryAt: &next}
		}
		var usage UsageRow
		if err := tx.Table("ai_user_daily_usage").Where("user_id=? AND day=? AND feature=?", c.UserID, c.CreatedAt.Format("2006-01-02"), c.Feature).Find(&usage).Error; err != nil {
			return err
		}
		if limit := p.limit(c.Feature); limit > 0 && usage.Calls >= limit {
			at := c.CreatedAt.Truncate(24 * time.Hour).Add(24 * time.Hour)
			return &CallError{Code: "daily_limit", RetryAt: &at}
		}
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		return tx.Model(&a).Updates(map[string]any{"lease_token": c.ID, "lease_until": c.LeaseUntil, column: callStorageTime(c.CreatedAt.Add(p.interval(c.Feature)))}).Error
	})
}
func (s *MySQLCallStore) Start(ctx context.Context, id string, now time.Time) error {
	now = callStorageTime(now)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c CallRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&c).Error; err != nil {
			return err
		}
		if c.Status != "reserved" || !c.LeaseUntil.After(now) {
			return ErrLeaseLost
		}
		if err := checkCallLease(tx, id, now); err != nil {
			return err
		}
		day := c.CreatedAt.UTC().Format("2006-01-02")
		if err := tx.Exec("INSERT INTO ai_user_daily_usage(user_id,day,feature,calls) VALUES(?,?,?,1) ON DUPLICATE KEY UPDATE calls=calls+1", c.UserID, day, c.Feature).Error; err != nil {
			return err
		}
		return tx.Model(&c).Updates(map[string]any{"status": "started", "started_at": now}).Error
	})
}

// checkCallLease is also used inside configuration and task commit transactions.
func checkCallLease(tx *gorm.DB, id string, now time.Time) error {
	now = callStorageTime(now)
	if id == "" {
		return nil
	}
	var a admission
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("lease_token=? AND lease_until>?", id, now).Take(&a).Error; err != nil {
		return ErrLeaseLost
	}
	return nil
}
func (s *MySQLCallStore) Finish(ctx context.Context, id string, result generation.Result, callErr error, now time.Time) error {
	now = callStorageTime(now)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c CallRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&c).Error; err != nil {
			return err
		}
		if c.Status != "started" {
			return nil
		} // Settlement is idempotent, including recovery races.
		code, status := "", "succeeded"
		if callErr != nil {
			code = callFailure(callErr)
			status = "failed"
			if code == "timeout" || code == "transport_failed" || code == "result_unknown" {
				status = "unknown"
			}
		}
		if !c.LeaseUntil.After(now) {
			code, status = "result_unknown", "unknown"
			result = generation.Result{}
		}
		updates := map[string]any{"status": status, "failure_code": code, "finished_at": now, "duration_ms": max(int64(0), now.Sub(*c.StartedAt).Milliseconds()), "input_tokens": result.InputTokens, "output_tokens": result.OutputTokens, "usage_known": result.UsageKnown}
		var f *generation.Failure
		if errors.As(callErr, &f) {
			updates["provider_code"] = f.ProviderCode
			updates["provider_request_id"] = f.ProviderRequestID
			updates["http_status"] = f.HTTPStatus
		}
		if err := tx.Model(&c).Updates(updates).Error; err != nil {
			return err
		}
		missing := 0
		if !result.UsageKnown {
			missing = 1
		}
		return tx.Exec("UPDATE ai_user_daily_usage SET "+status+"="+status+"+1,input_tokens=input_tokens+?,output_tokens=output_tokens+?,usage_missing=usage_missing+? WHERE user_id=? AND day=? AND feature=?", result.InputTokens, result.OutputTokens, missing, c.UserID, c.CreatedAt.UTC().Format("2006-01-02"), c.Feature).Error
	})
}
func (s *MySQLCallStore) Release(ctx context.Context, c CallRecord, now time.Time) error {
	now = callStorageTime(now)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&CallRecord{}).Where("id=? AND status='reserved'", c.ID).Updates(map[string]any{"status": "cancelled", "finished_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&admission{}).Where("user_id=? AND lease_token=?", c.UserID, c.ID).Updates(map[string]any{"lease_token": "", "lease_until": now}).Error
	})
}
func (s *MySQLCallStore) Recover(ctx context.Context, now time.Time) error {
	now = callStorageTime(now)
	var calls []CallRecord
	if err := s.db.WithContext(ctx).Where("status IN ('reserved','started') AND lease_until<=?", now).Order("lease_until").Limit(100).Find(&calls).Error; err != nil {
		return err
	}
	for _, c := range calls {
		if c.Status == "started" {
			if err := s.Finish(ctx, c.ID, generation.Result{}, &generation.Failure{Code: "result_unknown"}, now); err != nil {
				return err
			}
		}
		if err := s.Release(ctx, c, now); err != nil {
			return err
		}
	}
	return s.db.WithContext(ctx).Where("created_at<? AND status NOT IN ('reserved','started')", now.AddDate(0, 0, -30)).Limit(500).Delete(&CallRecord{}).Error
}
func (s *MySQLCallStore) Usage(ctx context.Context, u uint64, from, to time.Time) ([]UsageRow, error) {
	rows := []UsageRow{}
	err := s.db.WithContext(ctx).Table("ai_user_daily_usage").Select("DATE_FORMAT(day,'%Y-%m-%d') AS day,feature,calls,succeeded,failed,unknown,input_tokens,output_tokens,usage_missing").Where("user_id=? AND day BETWEEN ? AND ?", u, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02")).Order("day ASC,feature ASC").Find(&rows).Error
	return rows, err
}
func (s *MySQLCallStore) List(ctx context.Context, u uint64, feature, status string, page, size int) (CallPage, error) {
	result := CallPage{Items: []CallRecord{}, Page: page, PageSize: size}
	q := s.db.WithContext(ctx).Model(&CallRecord{}).Where("user_id=? AND status NOT IN ('reserved','cancelled')", u)
	if feature != "" {
		q = q.Where("feature=?", feature)
	}
	if status != "" {
		q = q.Where("status=?", status)
	}
	if err := q.Count(&result.Total).Error; err != nil {
		return result, err
	}
	err := q.Order("created_at DESC,id DESC").Offset((page - 1) * size).Limit(size).Find(&result.Items).Error
	return result, err
}
