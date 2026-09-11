package ai

import (
	"context"
	"signalwatch/internal/insight"
	"signalwatch/internal/paper"
	"signalwatch/internal/subscription"
	"signalwatch/internal/user"
	"time"
)

func (r *Repository) ReadyDigest(ctx context.Context, key Task) ([]Task, error) {
	var rows []Task
	err := r.identity(ctx, key).Where("status='ready' AND input_hash=? AND profile=?", key.InputHash, key.Profile).Find(&rows).Error
	return rows, err
}
func (r *Repository) PaperEligible(ctx context.Context, t Task) (bool, error) {
	var p paper.Paper
	if err := r.DB.WithContext(ctx).Where("id=?", t.ScopeID).Take(&p).Error; err != nil {
		return false, err
	}
	if insight.PaperHash(insight.Paper{Title: p.Title, Abstract: p.Abstract}) != t.InputHash {
		return false, nil
	}
	var count int64
	err := r.DB.WithContext(ctx).Table("subscription_papers sp").Joins("JOIN subscriptions sub ON sub.id=sp.subscription_id JOIN users u ON u.id=sub.user_id JOIN user_ai_configurations ac ON ac.user_id=u.id").Where("sp.paper_id=? AND u.id=? AND u.status='active' AND u.role='user' AND u.ai_enabled=1 AND ac.status='active' AND ac.generation=? AND ac.config_version=? AND ac.provider_id=? AND ac.model_id=?", p.ID, t.OwnerUserID, t.ConfigGeneration, t.ConfigVersion, t.ProviderID, t.ModelID).Count(&count).Error
	return count > 0, err
}
func (r *Repository) DemandUsers(ctx context.Context, after uint64) ([]user.User, error) {
	var rows []user.User
	err := r.DB.WithContext(ctx).Table("users u").Select("u.*").Joins("JOIN user_ai_configurations ac ON ac.user_id=u.id AND ac.status='active'").Where("u.id>? AND u.status='active' AND u.role='user'", after).Order("u.id").Limit(100).Scan(&rows).Error
	return rows, err
}
func (r *Repository) DemandSubscriptions(ctx context.Context, uid uint64) ([]subscription.Subscription, error) {
	var rows []subscription.Subscription
	err := r.DB.WithContext(ctx).Where("user_id=? AND enabled=1 AND digest_ai_enabled=1 AND deleted_at IS NULL", uid).Order("id").Limit(20).Find(&rows).Error
	return rows, err
}
func (r *Repository) UsageTotals(ctx context.Context, now time.Time) (UsageTotals, error) {
	var usage UsageTotals
	err := r.DB.WithContext(ctx).Table("ai_user_daily_usage").Select("COALESCE(SUM(calls),0) AS requests,COALESCE(SUM(input_tokens),0) AS input_tokens,COALESCE(SUM(output_tokens),0) AS output_tokens,COALESCE(SUM(usage_missing),0) AS estimated_calls").Where("day=?", now.UTC().Format("2006-01-02")).Scan(&usage).Error
	return usage, err
}
