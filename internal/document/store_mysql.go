package document

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type MySQLStore struct{ db *gorm.DB }

func NewMySQLStore(db *gorm.DB) *MySQLStore { return &MySQLStore{db} }
func (s *MySQLStore) Ensure(ctx context.Context, src Source) (Document, error) {
	now := time.Now().UTC()
	d := Document{ID: Identity(src), PaperID: src.PaperID, SourceVersion: src.ArXivID + "@" + src.UpdatedAt.UTC().Format(time.RFC3339Nano), ParserVersion: ParserVersion, State: "pending", CreatedAt: now, UpdatedAt: now}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&d).Error; err != nil {
		return d, err
	}
	return s.Get(ctx, d.ID)
}

// Prepare is an explicit user request to create or retry this document version.
// Reading status and ensuring a row exists must never restart a failed attempt.
func (s *MySQLStore) Prepare(ctx context.Context, src Source) (Document, error) {
	d, err := s.Ensure(ctx, src)
	if err != nil {
		return d, err
	}
	if d.State != "failed" {
		return d, nil
	}
	if err := s.db.WithContext(ctx).Model(&Document{}).Where("id=? AND state='failed' AND epoch=?", d.ID, d.Epoch).
		Updates(map[string]any{"state": "pending", "failure_code": "", "updated_at": time.Now().UTC()}).Error; err != nil {
		return d, err
	}
	return s.Get(ctx, d.ID)
}
func (s *MySQLStore) Get(ctx context.Context, id string) (Document, error) {
	var d Document
	err := s.db.WithContext(ctx).Where("id=?", id).Take(&d).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return d, ErrNotFound
	}
	return d, err
}
func (s *MySQLStore) Chunks(ctx context.Context, id string) ([]Chunk, error) {
	rows := []Chunk{}
	err := s.db.WithContext(ctx).Where("document_id=?", id).Order("number").Find(&rows).Error
	return rows, err
}
func (s *MySQLStore) Source(ctx context.Context, id uint64) (Source, error) {
	var p struct {
		ID             uint64
		ArXivID        string    `gorm:"column:arxiv_id"`
		PDFURL         string    `gorm:"column:pdf_url"`
		ArXivUpdatedAt time.Time `gorm:"column:arxiv_updated_at"`
	}
	err := s.db.WithContext(ctx).Table("papers").Select("id, arxiv_id, pdf_url, arxiv_updated_at").Where("id=?", id).Take(&p).Error
	return Source{p.ID, p.ArXivID, p.PDFURL, p.ArXivUpdatedAt}, err
}
func (s *MySQLStore) Claim(ctx context.Context, owner string) (d Document, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state='pending' OR (state='processing' AND lease_until<UTC_TIMESTAMP(6))").Order("created_at").Take(&d).Error; err != nil {
			return err
		}
		d.Epoch++
		d.LeaseOwner = owner
		d.State = "processing"
		return tx.Model(&Document{}).Where("id=?", d.ID).Updates(map[string]any{"state": d.State, "lease_owner": owner, "epoch": d.Epoch, "lease_until": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 SECOND)"), "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")}).Error
	})
	return
}
func (s *MySQLStore) owned(ctx context.Context, d Document) *gorm.DB {
	return s.db.WithContext(ctx).Model(&Document{}).Where("id=? AND epoch=? AND lease_owner=? AND state='processing' AND lease_until>UTC_TIMESTAMP(6)", d.ID, d.Epoch, d.LeaseOwner)
}
func (s *MySQLStore) Renew(ctx context.Context, d Document) error {
	r := s.owned(ctx, d).Update("lease_until", gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 SECOND)"))
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (s *MySQLStore) Fail(ctx context.Context, d Document, code string) error {
	r := s.owned(ctx, d).Updates(map[string]any{"state": "failed", "failure_code": code, "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")})
	return r.Error
}
func (s *MySQLStore) Complete(ctx context.Context, d Document, chunks []Chunk, hash, version string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND epoch=? AND lease_owner=? AND state='processing' AND lease_until>UTC_TIMESTAMP(6)", d.ID, d.Epoch, d.LeaseOwner).Take(&row).Error; err != nil {
			return ErrLeaseLost
		}
		if err := tx.Where("document_id=?", d.ID).Delete(&Chunk{}).Error; err != nil {
			return err
		}
		if err := tx.CreateInBatches(chunks, 50).Error; err != nil {
			return err
		}
		return tx.Model(&Document{}).Where("id=?", d.ID).Updates(map[string]any{"state": "ready", "content_hash": hash, "source_version": version, "page_count": d.PageCount, "text_complete": d.TextComplete, "sections": d.Sections, "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")}).Error
	})
}

func (s *MySQLStore) Stats(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		State string
		Count int
	}
	err := s.db.WithContext(ctx).Table("paper_documents").Select("state,COUNT(*) AS count").Group("state").Scan(&rows).Error
	out := map[string]int{}
	for _, row := range rows {
		out[row.State] = row.Count
	}
	return out, err
}
