package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"signalwatch/internal/source"
)

const maxEnabledSubscriptions = int64(20)

var (
	ErrLimitReached    = errors.New("subscription limit reached")
	ErrUserNotFound    = errors.New("active user not found")
	ErrNotFound        = errors.New("subscription not found")
	ErrVersionConflict = errors.New("subscription version conflict")
)

type Repository interface {
	CreateAtomic(ctx context.Context, subscription *Subscription, rules []Rule) error
	Count(ctx context.Context, userID uint64, filter ListFilter) (int64, error)
	List(ctx context.Context, userID uint64, filter ListFilter, offset, limit int) ([]QueryResult, error)
	Get(ctx context.Context, userID, id uint64) (QueryResult, error)
	UpdateAtomic(ctx context.Context, userID, id uint64, expectedVersion uint32, patch SubscriptionPatch, rules *[]Rule) (Subscription, []Rule, error)
	SoftDelete(ctx context.Context, userID, id uint64, expectedVersion uint32) error
}

type transactionStore interface {
	LockActiveUser(ctx context.Context, userID uint64) error
	CountEnabled(ctx context.Context, userID uint64) (int64, error)
	CreateSubscription(ctx context.Context, subscription *Subscription) error
	LockOwnedSubscription(ctx context.Context, userID, id uint64, expectedVersion uint32) (Subscription, error)
	OwnedActiveSubscriptionExists(ctx context.Context, userID, id uint64) (bool, error)
	UpdateSubscription(ctx context.Context, subscription *Subscription, patch SubscriptionPatch) error
}

type transactionManager interface {
	WithinTransaction(ctx context.Context, work func(transactionStore) error) error
}

type repository struct {
	db           *gorm.DB
	transactions transactionManager
	deletions    deletionStore
}

func NewRepository(db *gorm.DB) Repository {
	store := gormDeletionStore{db: db}
	return &repository{db: db, transactions: gormTransactionManager{db: db}, deletions: store}
}

func (repository *repository) CreateAtomic(
	ctx context.Context,
	subscription *Subscription,
	rules []Rule,
) error {
	category, keywordsJSON, err := flattenRules(rules)
	if err != nil {
		return err
	}
	subscription.Category = category
	subscription.KeywordsJSON = keywordsJSON
	return repository.transactions.WithinTransaction(ctx, func(tx transactionStore) error {
		if err := tx.LockActiveUser(ctx, subscription.UserID); err != nil {
			return err
		}
		if subscription.Enabled {
			count, err := tx.CountEnabled(ctx, subscription.UserID)
			if err != nil {
				return err
			}
			if count >= maxEnabledSubscriptions {
				return ErrLimitReached
			}
		}
		if err := tx.CreateSubscription(ctx, subscription); err != nil {
			return err
		}
		return nil
	})
}

func (repository *repository) UpdateAtomic(
	ctx context.Context,
	userID uint64,
	id uint64,
	expectedVersion uint32,
	patch SubscriptionPatch,
	replacementRules *[]Rule,
) (Subscription, []Rule, error) {
	var updated Subscription
	var resultRules []Rule
	err := repository.transactions.WithinTransaction(ctx, func(tx transactionStore) error {
		current, err := tx.LockOwnedSubscription(ctx, userID, id, expectedVersion)
		if errors.Is(err, ErrNotFound) {
			exists, existsErr := tx.OwnedActiveSubscriptionExists(ctx, userID, id)
			if existsErr != nil {
				return existsErr
			}
			if exists {
				return ErrVersionConflict
			}
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		currentRules, err := rulesFromSubscription(current)
		if err != nil {
			return err
		}
		changed := subscriptionPatchChanges(current, patch)
		if replacementRules != nil && !sameRules(currentRules, *replacementRules) {
			changed = true
		}
		if !changed {
			updated = current
			resultRules = currentRules
			return nil
		}

		if patch.Enabled != nil && !current.Enabled && *patch.Enabled {
			if err := tx.LockActiveUser(ctx, userID); err != nil {
				return err
			}
			count, err := tx.CountEnabled(ctx, userID)
			if err != nil {
				return err
			}
			if count >= maxEnabledSubscriptions {
				return ErrLimitReached
			}
		}

		if replacementRules != nil && !sameRules(currentRules, *replacementRules) {
			category, keywordsJSON, err := flattenRules(*replacementRules)
			if err != nil {
				return err
			}
			patch.Category = &category
			patch.KeywordsJSON = &keywordsJSON
			resultRules = append([]Rule(nil), (*replacementRules)...)
			for index := range resultRules {
				resultRules[index].SubscriptionID = current.ID
			}
		} else {
			resultRules = currentRules
		}
		if err := tx.UpdateSubscription(ctx, &current, patch); err != nil {
			return err
		}
		updated = current
		return nil
	})
	if err != nil {
		return Subscription{}, nil, err
	}
	return updated, resultRules, nil
}

func subscriptionPatchChanges(current Subscription, patch SubscriptionPatch) bool {
	if patch.Name != nil && current.Name != *patch.Name {
		return true
	}
	if patch.ObjectiveSet && !sameOptionalString(current.Objective, patch.Objective) {
		return true
	}
	return patch.Enabled != nil && current.Enabled != *patch.Enabled
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameRules(current, replacement []Rule) bool {
	if len(current) != len(replacement) {
		return false
	}
	for index := range current {
		if current[index].RuleType != replacement[index].RuleType ||
			current[index].RuleValue != replacement[index].RuleValue ||
			current[index].NormalizedValue != replacement[index].NormalizedValue {
			return false
		}
	}
	return true
}

func flattenRules(rules []Rule) (string, json.RawMessage, error) {
	category := ""
	keywords := make([]string, 0)
	for _, rule := range rules {
		switch rule.RuleType {
		case source.RuleTypeCategory:
			if category != "" {
				return "", nil, ErrInvalidStoredRule
			}
			category = rule.RuleValue
		case source.RuleTypeIncludeKeyword:
			keywords = append(keywords, rule.RuleValue)
		default:
			return "", nil, ErrInvalidStoredRule
		}
	}
	if category == "" {
		return "", nil, ErrInvalidStoredRule
	}
	encoded, err := json.Marshal(keywords)
	if err != nil {
		return "", nil, err
	}
	return category, encoded, nil
}

func rulesFromSubscription(item Subscription) ([]Rule, error) {
	if item.Category == "" {
		return nil, ErrInvalidStoredRule
	}
	var keywords []string
	if err := json.Unmarshal(item.KeywordsJSON, &keywords); err != nil {
		return nil, ErrInvalidStoredRule
	}
	rules := make([]Rule, 0, 1+len(keywords))
	rules = append(rules, Rule{
		SubscriptionID: item.ID,
		RuleType:       source.RuleTypeCategory, RuleValue: item.Category,
		NormalizedValue: item.Category,
	})
	for _, keyword := range keywords {
		normalized, err := NormalizeTextRule(keyword)
		if err != nil {
			return nil, ErrInvalidStoredRule
		}
		rules = append(rules, Rule{
			SubscriptionID: item.ID,
			RuleType:       source.RuleTypeIncludeKeyword,
			RuleValue:      normalized.RuleValue, NormalizedValue: normalized.NormalizedValue,
		})
	}
	return rules, nil
}

type deletionStore interface {
	SoftDelete(ctx context.Context, userID, id uint64, expectedVersion uint32) (bool, error)
	OwnedActiveSubscriptionExists(ctx context.Context, userID, id uint64) (bool, error)
}

func (repository *repository) SoftDelete(
	ctx context.Context,
	userID uint64,
	id uint64,
	expectedVersion uint32,
) error {
	deleted, err := repository.deletions.SoftDelete(ctx, userID, id, expectedVersion)
	if err != nil {
		return err
	}
	if deleted {
		return nil
	}
	exists, err := repository.deletions.OwnedActiveSubscriptionExists(ctx, userID, id)
	if err != nil {
		return err
	}
	if exists {
		return ErrVersionConflict
	}
	return ErrNotFound
}

func (repository *repository) Count(
	ctx context.Context,
	userID uint64,
	filter ListFilter,
) (int64, error) {
	var count int64
	query := applyListFilter(repository.db.WithContext(ctx).Model(&Subscription{}), userID, filter)
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (repository *repository) List(
	ctx context.Context,
	userID uint64,
	filter ListFilter,
	offset int,
	limit int,
) ([]QueryResult, error) {
	var rows []subscriptionQueryRow
	query := selectSubscriptionsWithSource(repository.db.WithContext(ctx))
	query = applyListFilter(query, userID, filter)
	if err := query.
		Order("subscriptions.created_at DESC, subscriptions.id DESC").
		Offset(offset).
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return repository.attachRules(ctx, rows)
}

func (repository *repository) Get(
	ctx context.Context,
	userID uint64,
	id uint64,
) (QueryResult, error) {
	var row subscriptionQueryRow
	err := ownedSubscriptionQuery(selectSubscriptionsWithSource(repository.db.WithContext(ctx)), userID, id).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return QueryResult{}, ErrNotFound
	}
	if err != nil {
		return QueryResult{}, err
	}

	results, err := repository.attachRules(ctx, []subscriptionQueryRow{row})
	if err != nil {
		return QueryResult{}, err
	}
	return results[0], nil
}

func applyListFilter(query *gorm.DB, userID uint64, filter ListFilter) *gorm.DB {
	query = query.Where("subscriptions.user_id = ? AND subscriptions.deleted_at IS NULL", userID)
	if filter.Enabled != nil {
		query = query.Where("subscriptions.enabled = ?", *filter.Enabled)
	}
	if filter.SourceID != nil {
		query = query.Where("subscriptions.source_id = ?", *filter.SourceID)
	}
	return query
}

func ownedSubscriptionQuery(query *gorm.DB, userID, id uint64) *gorm.DB {
	return query.Where(
		"subscriptions.id = ? AND subscriptions.user_id = ? AND subscriptions.deleted_at IS NULL",
		id,
		userID,
	)
}

func selectSubscriptionsWithSource(query *gorm.DB) *gorm.DB {
	return query.Table("subscriptions").
		Select(`
			subscriptions.id AS subscription_id,
			subscriptions.user_id AS subscription_user_id,
			subscriptions.source_id AS subscription_source_id,
			subscriptions.name AS subscription_name,
			subscriptions.objective AS subscription_objective,
			subscriptions.category AS subscription_category,
			subscriptions.keywords_json AS subscription_keywords_json,
			subscriptions.enabled AS subscription_enabled,
			subscriptions.version AS subscription_version,
			subscriptions.created_at AS subscription_created_at,
			subscriptions.updated_at AS subscription_updated_at,
			subscriptions.deleted_at AS subscription_deleted_at,
			sources.id AS public_source_id,
			sources.source_key AS public_source_key,
			sources.kind AS public_source_kind,
			sources.name AS public_source_name,
			sources.config_json AS public_source_config_json
		`).
		Joins("JOIN sources ON sources.id = subscriptions.source_id")
}

func (repository *repository) attachRules(
	_ context.Context,
	rows []subscriptionQueryRow,
) ([]QueryResult, error) {
	results := make([]QueryResult, 0, len(rows))
	for _, row := range rows {
		result, err := row.result()
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

type subscriptionQueryRow struct {
	SubscriptionID        uint64          `gorm:"column:subscription_id"`
	SubscriptionUserID    uint64          `gorm:"column:subscription_user_id"`
	SubscriptionSourceID  uint64          `gorm:"column:subscription_source_id"`
	SubscriptionName      string          `gorm:"column:subscription_name"`
	SubscriptionObjective *string         `gorm:"column:subscription_objective"`
	SubscriptionCategory  string          `gorm:"column:subscription_category"`
	SubscriptionKeywords  json.RawMessage `gorm:"column:subscription_keywords_json"`
	SubscriptionEnabled   bool            `gorm:"column:subscription_enabled"`
	SubscriptionVersion   uint32          `gorm:"column:subscription_version"`
	SubscriptionCreatedAt time.Time       `gorm:"column:subscription_created_at"`
	SubscriptionUpdatedAt time.Time       `gorm:"column:subscription_updated_at"`
	SubscriptionDeletedAt *time.Time      `gorm:"column:subscription_deleted_at"`
	PublicSourceID        uint64          `gorm:"column:public_source_id"`
	PublicSourceKey       string          `gorm:"column:public_source_key"`
	PublicSourceKind      string          `gorm:"column:public_source_kind"`
	PublicSourceName      string          `gorm:"column:public_source_name"`
	PublicSourceConfig    json.RawMessage `gorm:"column:public_source_config_json"`
}

func (row subscriptionQueryRow) result() (QueryResult, error) {
	result := QueryResult{
		Subscription: Subscription{
			ID: row.SubscriptionID, UserID: row.SubscriptionUserID,
			SourceID: row.SubscriptionSourceID, Name: row.SubscriptionName,
			Objective: row.SubscriptionObjective, Category: row.SubscriptionCategory,
			KeywordsJSON: append(json.RawMessage(nil), row.SubscriptionKeywords...),
			Enabled:      row.SubscriptionEnabled,
			Version:      row.SubscriptionVersion, CreatedAt: row.SubscriptionCreatedAt,
			UpdatedAt: row.SubscriptionUpdatedAt, DeletedAt: row.SubscriptionDeletedAt,
		},
		Source: source.Source{
			ID: row.PublicSourceID, SourceKey: row.PublicSourceKey,
			Kind: row.PublicSourceKind, Name: row.PublicSourceName,
			ConfigJSON: append(json.RawMessage(nil), row.PublicSourceConfig...),
		},
		Rules: make([]Rule, 0),
	}
	rules, err := rulesFromSubscription(result.Subscription)
	if err != nil {
		return QueryResult{}, err
	}
	result.Rules = rules
	return result, nil
}

type gormTransactionManager struct {
	db *gorm.DB
}

func (manager gormTransactionManager) WithinTransaction(
	ctx context.Context,
	work func(transactionStore) error,
) error {
	return manager.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return work(gormTransactionStore{db: tx})
	})
}

type gormTransactionStore struct {
	db *gorm.DB
}

func (store gormTransactionStore) LockActiveUser(ctx context.Context, userID uint64) error {
	var row struct{ ID uint64 }
	err := store.db.WithContext(ctx).
		Table("users").
		Select("id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND status = ?", userID, "active").
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrUserNotFound
	}
	return err
}

func (store gormTransactionStore) CountEnabled(ctx context.Context, userID uint64) (int64, error) {
	var count int64
	err := store.db.WithContext(ctx).
		Model(&Subscription{}).
		Where("user_id = ? AND enabled = ? AND deleted_at IS NULL", userID, true).
		Count(&count).Error
	return count, err
}

func (store gormTransactionStore) CreateSubscription(
	ctx context.Context,
	subscription *Subscription,
) error {
	return store.db.WithContext(ctx).Create(subscription).Error
}

func (store gormTransactionStore) LockOwnedSubscription(
	ctx context.Context,
	userID uint64,
	id uint64,
	expectedVersion uint32,
) (Subscription, error) {
	var item Subscription
	err := lockOwnedSubscriptionQuery(
		store.db.WithContext(ctx), userID, id, expectedVersion, &item,
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Subscription{}, ErrNotFound
	}
	return item, err
}

func lockOwnedSubscriptionQuery(
	db *gorm.DB,
	userID uint64,
	id uint64,
	expectedVersion uint32,
	target *Subscription,
) *gorm.DB {
	return db.
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(
			"id = ? AND user_id = ? AND version = ? AND deleted_at IS NULL",
			id,
			userID,
			expectedVersion,
		).
		Take(target)
}

func (store gormTransactionStore) OwnedActiveSubscriptionExists(
	ctx context.Context,
	userID uint64,
	id uint64,
) (bool, error) {
	return ownedActiveSubscriptionExists(store.db.WithContext(ctx), userID, id)
}

func (store gormTransactionStore) UpdateSubscription(
	ctx context.Context,
	item *Subscription,
	patch SubscriptionPatch,
) error {
	values := map[string]any{
		"updated_at": gorm.Expr("UTC_TIMESTAMP(6)"),
		"version":    gorm.Expr("version + 1"),
	}
	if patch.Name != nil {
		values["name"] = *patch.Name
	}
	if patch.ObjectiveSet {
		if patch.Objective == nil {
			values["objective"] = nil
		} else {
			values["objective"] = *patch.Objective
		}
	}
	if patch.Enabled != nil {
		values["enabled"] = *patch.Enabled
	}
	if patch.Category != nil {
		values["category"] = *patch.Category
	}
	if patch.KeywordsJSON != nil {
		values["keywords_json"] = *patch.KeywordsJSON
	}

	result := store.db.WithContext(ctx).
		Model(&Subscription{}).
		Where(
			"id = ? AND user_id = ? AND version = ? AND deleted_at IS NULL",
			item.ID,
			item.UserID,
			item.Version,
		).
		Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVersionConflict
	}
	return store.db.WithContext(ctx).
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", item.ID, item.UserID).
		Take(item).Error
}

type gormDeletionStore struct {
	db *gorm.DB
}

func (store gormDeletionStore) SoftDelete(
	ctx context.Context,
	userID uint64,
	id uint64,
	expectedVersion uint32,
) (bool, error) {
	result := softDeleteQuery(store.db.WithContext(ctx), userID, id, expectedVersion)
	return result.RowsAffected == 1, result.Error
}

func softDeleteQuery(db *gorm.DB, userID, id uint64, expectedVersion uint32) *gorm.DB {
	return db.Model(&Subscription{}).
		Where(
			"id = ? AND user_id = ? AND version = ? AND deleted_at IS NULL",
			id,
			userID,
			expectedVersion,
		).
		Updates(map[string]any{
			"deleted_at": gorm.Expr("UTC_TIMESTAMP(6)"),
			"updated_at": gorm.Expr("UTC_TIMESTAMP(6)"),
			"version":    gorm.Expr("version + 1"),
		})
}

func (store gormDeletionStore) OwnedActiveSubscriptionExists(
	ctx context.Context,
	userID uint64,
	id uint64,
) (bool, error) {
	return ownedActiveSubscriptionExists(store.db.WithContext(ctx), userID, id)
}

func ownedActiveSubscriptionExists(db *gorm.DB, userID, id uint64) (bool, error) {
	var count int64
	err := db.Model(&Subscription{}).
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).
		Count(&count).Error
	return count > 0, err
}
