// Package user keeps GORM details and database error translation in the
// repository layer.
package user

import (
	"context"
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	mysqlDuplicateEntryErrorNumber uint16 = 1062
	usersEmailUniqueKey                   = "uk_users_email"
)

var (
	ErrEmailAlreadyRegistered = errors.New("email already registered")
	ErrNotFound               = errors.New("user not found")
)

// 定义 Service 所依赖的最小数据库能力
type Repository interface {
	Create(ctx context.Context, user *User) error
	FindByEmail(ctx context.Context, normalizedEmail string) (User, error)
	FindActiveByID(ctx context.Context, userID uint64) (User, error)
	UpdateProfile(ctx context.Context, userID uint64, changes ProfileChanges) (User, error)
}

// FindByEmail loads the credentials and status needed by the authentication
// use case. Callers must pass an email normalized with NormalizeEmail.
func (repository *gormRepository) FindByEmail(
	ctx context.Context,
	normalizedEmail string,
) (User, error) {
	var found User
	err := repository.db.WithContext(ctx).
		Where("email = ?", normalizedEmail).
		Take(&found).
		Error
	if err != nil {
		return User{}, mapFindError(err)
	}
	return found, nil
}

func (repository *gormRepository) FindActiveByID(
	ctx context.Context,
	userID uint64,
) (User, error) {
	var found User
	err := repository.db.WithContext(ctx).
		Where("id = ? AND status = ?", userID, StatusActive).
		Take(&found).
		Error
	if err != nil {
		return User{}, mapFindError(err)
	}
	return found, nil
}

// UpdateProfile locks the authenticated user's row, updates only the three
// profile columns, and reads the resulting row in one transaction.
func (repository *gormRepository) UpdateProfile(
	ctx context.Context,
	userID uint64,
	changes ProfileChanges,
) (User, error) {
	var updated User
	err := repository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ?", userID, StatusActive).
			Take(&current).
			Error; err != nil {
			return mapFindError(err)
		}
		// Configuration mutations use the same user lock. This storage
		// precondition closes the race between API validation and deletion.
		if changes.AIEnabled != nil && *changes.AIEnabled {
			var active int64
			if err := tx.Table("user_ai_configurations").Where("user_id=? AND is_default=1 AND status='active'", userID).Count(&active).Error; err != nil {
				return err
			}
			if active == 0 {
				return ErrAIConfigurationRequired
			}
		}
		if !profileNeedsUpdate(current, changes) {
			updated = current
			return nil
		}

		updates := profileUpdateColumns(changes)
		if err := tx.Model(&User{}).
			Where("id = ? AND status = ?", userID, StatusActive).
			Updates(updates).
			Error; err != nil {
			return err
		}

		if err := tx.Where("id = ? AND status = ?", userID, StatusActive).
			Take(&updated).
			Error; err != nil {
			return mapFindError(err)
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return updated, nil
}

func profileNeedsUpdate(current User, changes ProfileChanges) bool {
	return (changes.AIEnabled != nil && current.AIEnabled != *changes.AIEnabled) || (changes.AILanguage != nil && current.AILanguage != *changes.AILanguage) || changes.Timezone != nil && current.Timezone != *changes.Timezone ||
		changes.DigestTime != nil && current.DigestTime != *changes.DigestTime ||
		changes.MaxItemsPerDigest != nil &&
			current.MaxItemsPerDigest != *changes.MaxItemsPerDigest
}

func profileUpdateColumns(changes ProfileChanges) map[string]any {
	updates := make(map[string]any, 6)
	if changes.AIEnabled != nil {
		updates["ai_enabled"] = *changes.AIEnabled
	}
	if changes.AILanguage != nil {
		updates["ai_language"] = *changes.AILanguage
	}
	if changes.Timezone != nil {
		updates["timezone"] = *changes.Timezone
	}
	if changes.DigestTime != nil {
		updates["digest_time"] = *changes.DigestTime
	}
	if changes.MaxItemsPerDigest != nil {
		updates["max_items_per_digest"] = *changes.MaxItemsPerDigest
	}
	return updates
}

func mapFindError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

type gormRepository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// 创建用户
func (repository *gormRepository) Create(ctx context.Context, user *User) error {
	err := repository.db.WithContext(ctx).Create(user).Error
	//使用当前请求的 Context 向数据库插入这个用户，并将插入产生的错误保存到 err 中。
	if err == nil {
		return nil
	}
	return mapCreateError(err)
}

// 负责把数据库错误翻译成业务层可以理解的错误
func mapCreateError(err error) error {
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) &&
		mysqlError.Number == mysqlDuplicateEntryErrorNumber &&
		strings.Contains(mysqlError.Message, usersEmailUniqueKey) {
		return ErrEmailAlreadyRegistered
	}
	return err
}
