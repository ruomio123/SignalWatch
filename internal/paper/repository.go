package paper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"signalwatch/internal/platform/fence"
)

type Repository interface {
	Upsert(ctx context.Context, sourceID uint64, records []Record, observedAt time.Time) (UpsertResult, error)
}

type repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &repository{db: db}
}

func (repository *repository) Upsert(
	ctx context.Context,
	sourceID uint64,
	records []Record,
	observedAt time.Time,
) (UpsertResult, error) {
	if sourceID == 0 || observedAt.IsZero() {
		return UpsertResult{}, ErrInvalidRecord
	}
	prepared := make([]Record, 0, len(records))
	for _, record := range records {
		normalized, err := NormalizeRecord(record)
		if err != nil {
			return UpsertResult{}, err
		}
		prepared = append(prepared, normalized)
	}

	result := UpsertResult{Papers: make([]Paper, 0, len(prepared))}
	err := repository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := fence.Guard(ctx, tx); err != nil {
			return err
		}
		for _, record := range prepared {
			stored, inserted, err := upsertRecord(tx, sourceID, record, observedAt.UTC())
			if err != nil {
				return err
			}
			if inserted {
				result.Inserted++
			} else {
				result.Updated++
			}
			result.Papers = append(result.Papers, stored)
		}
		return nil
	})
	if err != nil {
		return UpsertResult{}, fmt.Errorf("upsert papers: %w", err)
	}
	return result, nil
}

func upsertRecord(
	tx *gorm.DB,
	sourceID uint64,
	record Record,
	observedAt time.Time,
) (Paper, bool, error) {
	authorsJSON, err := json.Marshal(record.Authors)
	if err != nil {
		return Paper{}, false, err
	}
	categoriesJSON, err := json.Marshal(record.Categories)
	if err != nil {
		return Paper{}, false, err
	}

	var existing Paper
	lookup := tx.Where("source_id = ? AND arxiv_id = ?", sourceID, record.ArXivID).Take(&existing)
	inserted := errors.Is(lookup.Error, gorm.ErrRecordNotFound)
	if lookup.Error != nil && !inserted {
		return Paper{}, false, lookup.Error
	}

	candidate := Paper{
		SourceID: sourceID, ArXivID: record.ArXivID,
		Title: record.Title, Abstract: record.Abstract, Comments: record.Comments,
		AuthorsJSON: authorsJSON, CategoriesJSON: categoriesJSON,
		PublishedAt: record.PublishedAt, ArXivUpdatedAt: record.ArXivUpdatedAt,
		ArXivURL: record.ArXivURL, PDFURL: record.PDFURL,
		FirstSeenAt: observedAt, CreatedAt: observedAt, UpdatedAt: observedAt,
	}
	updates := clause.Set{}
	for _, column := range []string{"title", "abstract", "comments", "authors_json", "categories_json", "published_at", "arxiv_url", "pdf_url", "updated_at", "arxiv_updated_at"} {
		updates = append(updates, clause.Assignment{Column: clause.Column{Name: column}, Value: gorm.Expr("IF(VALUES(arxiv_updated_at)>=arxiv_updated_at,VALUES(" + column + ")," + column + ")")})
	}
	if err := tx.Clauses(clause.OnConflict{DoUpdates: updates}).Create(&candidate).Error; err != nil {
		return Paper{}, false, err
	}

	var stored Paper
	if err := tx.Where("source_id = ? AND arxiv_id = ?", sourceID, record.ArXivID).
		Take(&stored).Error; err != nil {
		return Paper{}, false, err
	}
	return stored, inserted, nil
}
