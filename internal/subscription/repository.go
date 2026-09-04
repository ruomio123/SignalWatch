package subscription

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxEnabledSubscriptions = int64(20)

var (
	ErrLimitReached = errors.New("subscription limit reached")
	ErrUserNotFound = errors.New("active user not found")
)

type Repository interface {
	CreateAtomic(ctx context.Context, subscription *Subscription, rules []Rule) error
}

type transactionStore interface {
	LockActiveUser(ctx context.Context, userID uint64) error
	CountEnabled(ctx context.Context, userID uint64) (int64, error)
	CreateSubscription(ctx context.Context, subscription *Subscription) error
	CreateRules(ctx context.Context, rules []Rule) error
}

type transactionManager interface {
	WithinTransaction(ctx context.Context, work func(transactionStore) error) error
}

type repository struct {
	transactions transactionManager
}

func NewRepository(db *gorm.DB) Repository {
	return &repository{transactions: gormTransactionManager{db: db}}
}

func (repository *repository) CreateAtomic(
	ctx context.Context,
	subscription *Subscription,
	rules []Rule,
) error {
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
		for index := range rules {
			rules[index].SubscriptionID = subscription.ID
		}
		if len(rules) > 0 {
			if err := tx.CreateRules(ctx, rules); err != nil {
				return err
			}
		}
		return nil
	})
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

func (store gormTransactionStore) CreateRules(ctx context.Context, rules []Rule) error {
	return store.db.WithContext(ctx).Create(&rules).Error
}
