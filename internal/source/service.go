package source

import (
	"context"
	"errors"
	"fmt"
)

var ErrInvalidID = errors.New("invalid source id")

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

func (service *Service) List(ctx context.Context) ([]PublicSource, error) {
	found, err := service.repository.ListEnabled(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled sources: %w", err)
	}

	result := make([]PublicSource, 0, len(found))
	for _, item := range found {
		public, err := item.Public()
		if err != nil {
			return nil, fmt.Errorf("build public source %d: %w", item.ID, err)
		}
		result = append(result, public)
	}
	return result, nil
}

func (service *Service) Get(ctx context.Context, id uint64) (PublicSource, error) {
	if id == 0 {
		return PublicSource{}, ErrInvalidID
	}
	found, err := service.repository.FindEnabledByID(ctx, id)
	if err != nil {
		return PublicSource{}, fmt.Errorf("find enabled source: %w", err)
	}
	public, err := found.Public()
	if err != nil {
		return PublicSource{}, fmt.Errorf("build public source %d: %w", id, err)
	}
	return public, nil
}
