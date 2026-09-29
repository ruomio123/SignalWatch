package document

import (
	"context"
	"errors"
)

// StructuredService is an optional enrichment capability. Its failures never
// change the readiness or content hash of an existing PDF text document.
type StructuredService struct {
	Store     StructuredStore
	Extractor StructuredExtractor
}

func NewStructuredService(store StructuredStore, extractor StructuredExtractor) *StructuredService {
	return &StructuredService{Store: store, Extractor: extractor}
}

func (s *StructuredService) Structured(ctx context.Context, source StructuredSource) (StructuredMaterial, error) {
	if err := ValidateStructuredSource(source); err != nil {
		return StructuredMaterial{}, err
	}
	if err := ctx.Err(); err != nil {
		return StructuredMaterial{}, err
	}
	if s == nil || s.Store == nil || s.Extractor == nil {
		return StructuredMaterial{}, &ExtractionError{Code: "structured_html_unavailable"}
	}
	material, err := s.Store.LoadStructured(ctx, source)
	if err == nil {
		return material, ValidateStructuredMaterial(source, material)
	}
	if !errors.Is(err, ErrNotFound) {
		return StructuredMaterial{}, err
	}
	material, err = s.Extractor.ExtractStructured(ctx, source)
	if err != nil {
		return StructuredMaterial{}, err
	}
	if err := ctx.Err(); err != nil {
		return StructuredMaterial{}, err
	}
	if err := ValidateStructuredMaterial(source, material); err != nil {
		return StructuredMaterial{}, err
	}
	if err := s.Store.SaveStructured(ctx, source, material); err != nil {
		return StructuredMaterial{}, err
	}
	// Concurrent successful readers converge on the first cached artifact.
	material, err = s.Store.LoadStructured(ctx, source)
	if err != nil {
		return StructuredMaterial{}, err
	}
	return material, ValidateStructuredMaterial(source, material)
}
