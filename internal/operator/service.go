package operator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"signalwatch/internal/user"
)

var (
	ErrAccountNotFound = errors.New("operator account not found")
	ErrAccountInactive = errors.New("operator account is not active")
	ErrLastOperator    = errors.New("cannot revoke the last active operator")
)

type Account struct {
	ID        uint64
	Email     string
	Status    string
	Role      string
	UpdatedAt time.Time
}

type Service struct {
	db  *gorm.DB
	now func() time.Time
}

func NewService(db *gorm.DB, now func() time.Time) (*Service, error) {
	if db == nil || now == nil {
		return nil, errors.New("invalid operator service configuration")
	}
	return &Service{db: db, now: now}, nil
}

func (service *Service) Grant(ctx context.Context, email string) (Account, error) {
	normalized := user.NormalizeEmail(email)
	var account user.User
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("email = ?", normalized).Take(&account).Error; err != nil {
			return mapAccountError(err)
		}
		if account.Status != user.StatusActive {
			return ErrAccountInactive
		}
		if account.Role == user.RoleOperator {
			return nil
		}
		updatedAt := service.now().UTC()
		if err := tx.Model(&user.User{}).Where("id = ?", account.ID).
			Updates(map[string]any{"role": user.RoleOperator, "updated_at": updatedAt}).Error; err != nil {
			return err
		}
		account.Role, account.UpdatedAt = user.RoleOperator, updatedAt
		return nil
	})
	if err != nil {
		return Account{}, fmt.Errorf("grant operator role: %w", err)
	}
	return publicAccount(account), nil
}

func (service *Service) Revoke(ctx context.Context, email string) (Account, error) {
	normalized := user.NormalizeEmail(email)
	var account user.User
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("role = ? AND status = ?", user.RoleOperator, user.StatusActive).
			Order("id ASC").Find(&[]user.User{}).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("email = ?", normalized).Take(&account).Error; err != nil {
			return mapAccountError(err)
		}
		if account.Role != user.RoleOperator {
			return nil
		}
		if account.Status == user.StatusActive {
			var activeOperators int64
			if err := tx.Model(&user.User{}).
				Where("role = ? AND status = ?", user.RoleOperator, user.StatusActive).
				Count(&activeOperators).Error; err != nil {
				return err
			}
			if activeOperators <= 1 {
				return ErrLastOperator
			}
		}
		updatedAt := service.now().UTC()
		if err := tx.Model(&user.User{}).Where("id = ?", account.ID).
			Updates(map[string]any{"role": user.RoleUser, "updated_at": updatedAt}).Error; err != nil {
			return err
		}
		account.Role, account.UpdatedAt = user.RoleUser, updatedAt
		return nil
	})
	if err != nil {
		return Account{}, fmt.Errorf("revoke operator role: %w", err)
	}
	return publicAccount(account), nil
}

func (service *Service) List(ctx context.Context) ([]Account, error) {
	var rows []user.User
	if err := service.db.WithContext(ctx).Where("role = ?", user.RoleOperator).
		Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list operators: %w", err)
	}
	result := make([]Account, 0, len(rows))
	for _, row := range rows {
		result = append(result, publicAccount(row))
	}
	return result, nil
}

func mapAccountError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrAccountNotFound
	}
	return err
}

func publicAccount(row user.User) Account {
	return Account{ID: row.ID, Email: row.Email, Status: row.Status, Role: row.Role, UpdatedAt: row.UpdatedAt}
}
