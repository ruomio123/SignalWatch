package agent

import (
	"context"
	"errors"
	"signalwatch/internal/document"
	"time"
)

// PaperDocumentStatus contains preparation state only, never extracted text,
// process output, internal leases, or model credentials.
type PaperDocumentStatus struct {
	State         string     `json:"state"`
	Usable        bool       `json:"usable"`
	DocumentID    string     `json:"document_id"`
	SourceVersion string     `json:"source_version"`
	ParserVersion string     `json:"parser_version"`
	PageCount     int        `json:"page_count"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
	FailureCode   string     `json:"failure_code,omitempty"`
}

func (s *Service) paperDocumentSource(ctx context.Context, uid, pid uint64) (document.Source, error) {
	if uid == 0 || pid == 0 {
		return document.Source{}, ErrInput
	}
	p, err := s.Papers.Get(ctx, uid, pid)
	if err != nil {
		return document.Source{}, ErrNotFound
	}
	return document.Source{PaperID: p.ID, ArXivID: p.ArXivID, PDFURL: p.PDFURL, UpdatedAt: p.ArXivUpdatedAt}, nil
}

func (s *Service) PaperDocument(ctx context.Context, uid, pid uint64) (PaperDocumentStatus, error) {
	src, err := s.paperDocumentSource(ctx, uid, pid)
	if err != nil {
		return PaperDocumentStatus{}, err
	}
	id := document.Identity(src)
	d, err := s.Documents.Get(ctx, id)
	if errors.Is(err, document.ErrNotFound) {
		return PaperDocumentStatus{State: "not_prepared", DocumentID: id, ParserVersion: document.ParserVersion}, nil
	}
	if err != nil {
		return PaperDocumentStatus{}, err
	}
	return publicPaperDocument(d), nil
}

func (s *Service) PreparePaperDocument(ctx context.Context, uid, pid uint64) (PaperDocumentStatus, error) {
	src, err := s.paperDocumentSource(ctx, uid, pid)
	if err != nil {
		return PaperDocumentStatus{}, err
	}
	d, err := s.Documents.Prepare(ctx, src)
	if err != nil {
		return PaperDocumentStatus{}, err
	}
	return publicPaperDocument(d), nil
}

func publicPaperDocument(d document.Document) PaperDocumentStatus {
	status := PaperDocumentStatus{State: d.State, Usable: d.State == "ready" && d.TextComplete && d.ParserVersion == document.ParserVersion, DocumentID: d.ID, SourceVersion: d.SourceVersion, ParserVersion: d.ParserVersion, PageCount: d.PageCount}
	if !d.UpdatedAt.IsZero() {
		status.UpdatedAt = &d.UpdatedAt
	}
	if d.State == "failed" {
		switch d.FailureCode {
		case "document_version_unavailable", "document_download_failed", "invalid_pdf", "document_extraction_failed", "text_extraction_failed", "document_resource_limit", "ocr_required", "ocr_failed", "document_timeout":
			status.FailureCode = d.FailureCode
		default:
			status.FailureCode = "document_extraction_failed"
		}
	}
	return status
}
