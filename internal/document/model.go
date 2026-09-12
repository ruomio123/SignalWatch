// Package document owns versioned text and retrieval, independent of PDF tools.
package document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

const ParserVersion = "poppler-pages-v1"

var ErrUnavailable = errors.New("DOCUMENT_UNAVAILABLE")
var ErrLeaseLost = errors.New("DOCUMENT_LEASE_LOST")

type Document struct {
	ID            string     `json:"id"`
	PaperID       uint64     `json:"paper_id"`
	SourceVersion string     `json:"source_version"`
	ParserVersion string     `json:"parser_version"`
	ContentHash   string     `json:"content_hash"`
	State         string     `json:"state"`
	FailureCode   string     `json:"failure_code,omitempty"`
	PageCount     int        `json:"page_count"`
	LeaseOwner    string     `json:"-"`
	Epoch         uint64     `json:"-"`
	LeaseUntil    *time.Time `json:"-"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (Document) TableName() string { return "paper_documents" }

type Chunk struct {
	DocumentID string `json:"document_id"`
	Number     int    `json:"number"`
	Page       int    `json:"page"`
	Text       string `json:"text"`
}

func (Chunk) TableName() string { return "paper_document_chunks" }

type Source struct {
	PaperID         uint64
	ArXivID, PDFURL string
	UpdatedAt       time.Time
}

func Identity(s Source) string {
	h := sha256.Sum256([]byte(s.ArXivID + "|" + s.PDFURL + "|" + s.UpdatedAt.UTC().Format(time.RFC3339Nano) + "|" + ParserVersion))
	return hex.EncodeToString(h[:])
}

type Store interface {
	Stats(context.Context) (map[string]int, error)
	Ensure(context.Context, Source) (Document, error)
	Get(context.Context, string) (Document, error)
	Chunks(context.Context, string) ([]Chunk, error)
	Claim(context.Context, string) (Document, error)
	Renew(context.Context, Document) error
	Complete(context.Context, Document, []Chunk, string, string) error
	Fail(context.Context, Document, string) error
	Source(context.Context, uint64) (Source, error)
}
type Extracted struct {
	Pages         []string
	SourceVersion string
}
type Extractor interface {
	Extract(context.Context, Source) (Extracted, error)
}
type Service struct {
	Store     Store
	Extractor Extractor
}
