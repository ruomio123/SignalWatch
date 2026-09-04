/*
隔离 GORM，只负责“创建用户”和“翻译数据库错误”
*/
package user

import (
	"context"
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
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
