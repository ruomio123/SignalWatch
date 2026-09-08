package paper

import (
	"encoding/json"
	"time"
)

// Paper is the single-row representation of an arXiv paper. Later arXiv
// versions overwrite this row; version history is intentionally not stored.
type Paper struct {
	ID             uint64          `gorm:"column:id;primaryKey;autoIncrement"`
	SourceID       uint64          `gorm:"column:source_id"`
	ArXivID        string          `gorm:"column:arxiv_id"`
	Title          string          `gorm:"column:title"`
	Abstract       string          `gorm:"column:abstract"`
	AuthorsJSON    json.RawMessage `gorm:"column:authors_json"`
	CategoriesJSON json.RawMessage `gorm:"column:categories_json"`
	PublishedAt    time.Time       `gorm:"column:published_at"`
	ArXivUpdatedAt time.Time       `gorm:"column:arxiv_updated_at"`
	ArXivURL       string          `gorm:"column:arxiv_url"`
	PDFURL         string          `gorm:"column:pdf_url"`
	FirstSeenAt    time.Time       `gorm:"column:first_seen_at"`
	CreatedAt      time.Time       `gorm:"column:created_at"`
	UpdatedAt      time.Time       `gorm:"column:updated_at"`
}

func (Paper) TableName() string { return "papers" }

// Record is the normalized boundary between an arXiv adapter and persistence.
// It deliberately has no subscription or matching fields.
type Record struct {
	ArXivID        string
	Title          string
	Abstract       string
	Authors        []string
	Categories     []string
	PublishedAt    time.Time
	ArXivUpdatedAt time.Time
	ArXivURL       string
	PDFURL         string
}

type UpsertResult struct {
	Inserted int
	Updated  int
	Papers   []Paper
}
