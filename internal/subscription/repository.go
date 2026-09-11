package subscription

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"signalwatch/internal/source"
)

type repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) Repository { return &repository{db} }
func (r *repository) Transact(ctx context.Context, fn func(Tx) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return fn(mysqlTx{tx}) })
}
func scope(db *gorm.DB, uid uint64, f ListFilter) *gorm.DB {
	q := db.Model(&Subscription{}).Where("user_id=? AND deleted_at IS NULL", uid)
	if f.Enabled != nil {
		q = q.Where("enabled=?", *f.Enabled)
	}
	if f.SourceID != nil {
		q = q.Where("source_id=?", *f.SourceID)
	}
	return q
}
func (r *repository) Count(ctx context.Context, uid uint64, f ListFilter) (int64, error) {
	var n int64
	err := scope(r.db.WithContext(ctx), uid, f).Count(&n).Error
	return n, err
}
func (r *repository) List(ctx context.Context, uid uint64, f ListFilter, offset, limit int) ([]QueryResult, error) {
	var rows []Subscription
	if err := scope(r.db.WithContext(ctx), uid, f).Order("created_at DESC,id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return r.hydrate(ctx, rows)
}
func (r *repository) Get(ctx context.Context, uid, id uint64) (QueryResult, error) {
	var sub Subscription
	err := scope(r.db.WithContext(ctx), uid, ListFilter{}).Where("id=?", id).Take(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return QueryResult{}, ErrNotFound
	}
	if err != nil {
		return QueryResult{}, err
	}
	rows, err := r.hydrate(ctx, []Subscription{sub})
	if err != nil {
		return QueryResult{}, err
	}
	return rows[0], nil
}

// Hydrate uses full subscription records plus two bounded batch queries. New
// persisted subscription fields cannot disappear through a hand-copied projection.
func (r *repository) hydrate(ctx context.Context, subs []Subscription) ([]QueryResult, error) {
	result := make([]QueryResult, 0, len(subs))
	if len(subs) == 0 {
		return result, nil
	}
	sourceIDs := []uint64{}
	ids := []uint64{}
	for _, s := range subs {
		sourceIDs = append(sourceIDs, s.SourceID)
		ids = append(ids, s.ID)
	}
	var sources []source.Source
	if err := r.db.WithContext(ctx).Where("id IN ?", sourceIDs).Find(&sources).Error; err != nil {
		return nil, err
	}
	sourceByID := map[uint64]source.Source{}
	for _, s := range sources {
		sourceByID[s.ID] = s
	}
	var states []struct {
		SubscriptionID     uint64
		State              string
		Processed, Matched uint64
	}
	if err := r.db.WithContext(ctx).Table("subscription_backfills").Select("subscription_id,state,processed,matched").Where("subscription_id IN ?", ids).Scan(&states).Error; err != nil {
		return nil, err
	}
	byID := map[uint64]BackfillStatus{}
	for _, b := range states {
		byID[b.SubscriptionID] = BackfillStatus{b.State, b.Processed, b.Matched}
	}
	for _, s := range subs {
		s.Backfill = BackfillStatus{State: "complete"}
		if b, ok := byID[s.ID]; ok {
			s.Backfill = b
		}
		result = append(result, QueryResult{Subscription: s, Source: sourceByID[s.SourceID]})
	}
	return result, nil
}
func (r *repository) SoftDelete(ctx context.Context, uid, id uint64, version uint32) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Subscription
		err := scope(tx, uid, ListFilter{}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.Version != version {
			return ErrVersionConflict
		}
		return tx.Model(&Subscription{}).Where("id=?", id).Updates(map[string]any{"deleted_at": gorm.Expr("UTC_TIMESTAMP(6)"), "updated_at": gorm.Expr("UTC_TIMESTAMP(6)"), "version": gorm.Expr("version+1")}).Error
	})
}

type mysqlTx struct{ db *gorm.DB }

func (t mysqlTx) LockUser(ctx context.Context, uid uint64) (uint16, error) {
	var row struct{ MaxItemsPerDigest uint16 }
	err := t.db.WithContext(ctx).Table("users").Clauses(clause.Locking{Strength: "UPDATE"}).Select("max_items_per_digest").Where("id=? AND status='active' AND role='user'", uid).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, ErrUserNotFound
	}
	return row.MaxItemsPerDigest, err
}
func (t mysqlTx) CountEnabled(ctx context.Context, uid uint64) (int64, error) {
	var n int64
	err := scope(t.db.WithContext(ctx), uid, ListFilter{}).Where("enabled=1").Count(&n).Error
	return n, err
}
func (t mysqlTx) Insert(ctx context.Context, s *Subscription) error {
	if s.DigestAIEnabled {
		if err := t.requireActiveAI(ctx, s.UserID); err != nil {
			return err
		}
	}
	return t.db.WithContext(ctx).Create(s).Error
}
func (t mysqlTx) Enqueue(ctx context.Context, s *Subscription, w BackfillWindow) error {
	return t.db.WithContext(ctx).Exec("INSERT INTO subscription_backfills(subscription_id,source_id,category,keywords_json,window_from,window_to,next_retry_at,updated_at) VALUES(?,?,?,?,?,?,?,?)", s.ID, s.SourceID, s.Category, string(s.KeywordsJSON), w.From, w.To, w.To, w.To).Error
}
func (t mysqlTx) LockSubscription(ctx context.Context, uid, id uint64) (Subscription, error) {
	var s Subscription
	err := scope(t.db.WithContext(ctx), uid, ListFilter{}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s, ErrNotFound
	}
	return s, err
}
func (t mysqlTx) Update(ctx context.Context, s *Subscription, p SubscriptionPatch) error {
	if p.DigestAIEnabled != nil && *p.DigestAIEnabled {
		if err := t.requireActiveAI(ctx, s.UserID); err != nil {
			return err
		}
	}
	values := map[string]any{"updated_at": gorm.Expr("UTC_TIMESTAMP(6)"), "version": gorm.Expr("version+1")}
	if p.Name != nil {
		values["name"] = *p.Name
	}
	if p.ObjectiveSet {
		values["objective"] = p.Objective
	}
	if p.Enabled != nil {
		values["enabled"] = *p.Enabled
	}
	if p.MaxItemsPerDigest != nil {
		values["max_items_per_digest"] = *p.MaxItemsPerDigest
	}
	if p.DigestAIEnabled != nil {
		values["digest_ai_enabled"] = *p.DigestAIEnabled
	}
	if p.DigestAILanguage != nil {
		values["digest_ai_language"] = *p.DigestAILanguage
	}
	if p.Category != nil {
		values["category"] = *p.Category
	}
	if p.KeywordsJSON != nil {
		values["keywords_json"] = *p.KeywordsJSON
	}
	if err := t.db.WithContext(ctx).Model(&Subscription{}).Where("id=? AND version=?", s.ID, s.Version).Updates(values).Error; err != nil {
		return err
	}
	return t.db.WithContext(ctx).Where("id=?", s.ID).Take(s).Error
}

// Called only while holding the account lock shared by configuration deletion.
func (t mysqlTx) requireActiveAI(ctx context.Context, uid uint64) error {
	var active int64
	if err := t.db.WithContext(ctx).Table("user_ai_configurations").Where("user_id=? AND status='active'", uid).Count(&active).Error; err != nil {
		return err
	}
	if active == 0 {
		return ErrAIConfigurationRequired
	}
	return nil
}
