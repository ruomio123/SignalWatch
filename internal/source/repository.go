package source

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

var ErrNotFound = errors.New("source not found")

type Repository interface {
	ListEnabled(ctx context.Context) ([]Source, error)
	FindEnabledByID(ctx context.Context, id uint64) (Source, error)
}

type gormRepository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

func (repository *gormRepository) ListEnabled(ctx context.Context) ([]Source, error) {
	var sources []Source
	if err := repository.db.WithContext(ctx).
		Where("enabled = ?", true).
		Order("id ASC").
		Find(&sources).Error; err != nil {
		return nil, err
	}
	return sources, nil
}

func (repository *gormRepository) FindEnabledByID(
	ctx context.Context,
	id uint64,
) (Source, error) {
	var found Source
	if err := repository.db.WithContext(ctx).
		Where("id = ? AND enabled = ?", id, true).
		Take(&found).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Source{}, ErrNotFound
		}
		return Source{}, err
	}
	return found, nil
}
