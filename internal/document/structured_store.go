package document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type structuredRecord struct {
	ID            string
	DocumentID    string
	SourceVersion string
	ParserVersion string
	ContentHash   string
	Payload       json.RawMessage
	CreatedAt     time.Time
}

func (structuredRecord) TableName() string { return "paper_document_structures" }

func structuredCacheID(source StructuredSource) string {
	hash := sha256.Sum256([]byte(source.DocumentID + "|" + source.SourceVersion + "|" + StructuredParserVersion))
	return hex.EncodeToString(hash[:])
}

func (s *MySQLStore) LoadStructured(ctx context.Context, source StructuredSource) (StructuredMaterial, error) {
	var material StructuredMaterial
	if err := ValidateStructuredSource(source); err != nil {
		return material, err
	}
	var record structuredRecord
	err := s.db.WithContext(ctx).Where("id=?", structuredCacheID(source)).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return material, ErrNotFound
	}
	if err != nil {
		return material, err
	}
	if len(record.Payload) > StructuredMaterialLimit || json.Unmarshal(record.Payload, &material) != nil || record.DocumentID != source.DocumentID || record.SourceVersion != source.SourceVersion || record.ParserVersion != StructuredParserVersion || record.ContentHash != material.ContentHash {
		return StructuredMaterial{}, &ExtractionError{Code: "structured_html_invalid"}
	}
	return material, ValidateStructuredMaterial(source, material)
}

func (s *MySQLStore) SaveStructured(ctx context.Context, source StructuredSource, material StructuredMaterial) error {
	if err := ValidateStructuredMaterial(source, material); err != nil {
		return err
	}
	payload, err := json.Marshal(material)
	if err != nil {
		return err
	}
	record := structuredRecord{ID: structuredCacheID(source), DocumentID: source.DocumentID, SourceVersion: source.SourceVersion, ParserVersion: StructuredParserVersion, ContentHash: material.ContentHash, Payload: payload, CreatedAt: time.Now().UTC()}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error
}
