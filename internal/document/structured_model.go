package document

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

const StructuredParserVersion = "arxiv-html-structure-v1"

const (
	StructuredHTMLLimit     = 8 << 20
	StructuredMaterialLimit = 2 << 20
	StructuredElementLimit  = 32 << 10
	StructuredElementCount  = 128
	StructuredTableRows     = 128
	StructuredTableColumns  = 32
	StructuredFormulaLimit  = 16 << 10
)

var structuredVersion = regexp.MustCompile(`^(?:[0-9]{4}\.[0-9]{4,5}|[a-zA-Z][a-zA-Z0-9.-]*/[0-9]{7})v[1-9][0-9]*$`)
var structuredAnchor = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:-]{0,159}$`)
var structuredHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

type StructuredSource struct {
	DocumentID    string `json:"document_id"`
	SourceVersion string `json:"source_version"`
}

type StructuredTable struct {
	Caption string     `json:"caption"`
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
	Notes   []string   `json:"notes"`
}

type StructuredFormula struct {
	TeX     string `json:"tex"`
	Context string `json:"context"`
}

type StructuredElement struct {
	ID      string             `json:"id"`
	Kind    string             `json:"kind"`
	Label   string             `json:"label"`
	Anchor  string             `json:"anchor"`
	Quote   string             `json:"quote"`
	Table   *StructuredTable   `json:"table,omitempty"`
	Formula *StructuredFormula `json:"formula,omitempty"`
}

type StructuredGap struct {
	Kind   string `json:"kind"`
	Anchor string `json:"anchor"`
	Reason string `json:"reason"`
}

type StructuredMaterial struct {
	DocumentID    string              `json:"document_id"`
	SourceVersion string              `json:"source_version"`
	ParserVersion string              `json:"parser_version"`
	ContentHash   string              `json:"content_hash"`
	SourceURL     string              `json:"source_url"`
	Elements      []StructuredElement `json:"elements"`
	Gaps          []StructuredGap     `json:"gaps"`
}

type StructuredProvider interface {
	Structured(context.Context, StructuredSource) (StructuredMaterial, error)
}

type StructuredExtractor interface {
	ExtractStructured(context.Context, StructuredSource) (StructuredMaterial, error)
}

type StructuredStore interface {
	LoadStructured(context.Context, StructuredSource) (StructuredMaterial, error)
	SaveStructured(context.Context, StructuredSource, StructuredMaterial) error
}

func ValidateStructuredSource(source StructuredSource) error {
	if !structuredText(source.DocumentID, 64, true) || len(source.SourceVersion) > 128 || !structuredVersion.MatchString(source.SourceVersion) {
		return &ExtractionError{Code: "structured_source_mismatch"}
	}
	return nil
}

func StructuredURL(version string) string { return "https://arxiv.org/html/" + version }

func StructuredElementID(kind, anchor string) string { return "h-" + kind + "-" + anchor }

// StructuredQuote is a complete, deterministic transcription for retrieval and
// review. It is never fed through the prose passage splitter.
func StructuredQuote(element StructuredElement) string {
	parts := []string{element.Label}
	if table := element.Table; table != nil {
		parts = append(parts, table.Caption, strings.Join(table.Headers, "\t"))
		for _, row := range table.Rows {
			parts = append(parts, strings.Join(row, "\t"))
		}
		parts = append(parts, table.Notes...)
	}
	if formula := element.Formula; formula != nil {
		parts = append(parts, formula.TeX, formula.Context)
	}
	return strings.Join(parts, "\n")
}

func structuredText(value string, maxBytes int, required bool) bool {
	return utf8.ValidString(value) && len(value) <= maxBytes && (!required || strings.TrimSpace(value) != "") && !strings.ContainsRune(value, 0)
}

func ValidateStructuredElement(element StructuredElement) error {
	invalid := func() error { return &ExtractionError{Code: "structured_html_invalid"} }
	if (element.Kind != "table" && element.Kind != "formula") || !structuredAnchor.MatchString(element.Anchor) || element.ID != StructuredElementID(element.Kind, element.Anchor) || !structuredText(element.Label, 512, true) {
		return invalid()
	}
	if element.Kind == "table" {
		table := element.Table
		if table == nil || element.Formula != nil || !structuredText(table.Caption, 8192, false) || table.Headers == nil || len(table.Headers) == 0 || len(table.Headers) > StructuredTableColumns || table.Rows == nil || len(table.Rows) == 0 || len(table.Rows) > StructuredTableRows || table.Notes == nil || len(table.Notes) > 32 {
			return invalid()
		}
		for _, header := range table.Headers {
			if !structuredText(header, 4096, true) {
				return invalid()
			}
		}
		for _, row := range table.Rows {
			if len(row) != len(table.Headers) {
				return invalid()
			}
			for _, cell := range row {
				if !structuredText(cell, 4096, false) {
					return invalid()
				}
			}
		}
		for _, note := range table.Notes {
			if !structuredText(note, 2048, true) {
				return invalid()
			}
		}
	} else {
		if element.Formula == nil || element.Table != nil || !structuredText(element.Formula.TeX, StructuredFormulaLimit, true) || !structuredText(element.Formula.Context, 8192, false) {
			return invalid()
		}
	}
	if !structuredText(element.Quote, StructuredElementLimit, true) || element.Quote != StructuredQuote(element) {
		return invalid()
	}
	encoded, err := json.Marshal(element)
	if err != nil || len(encoded) > StructuredElementLimit {
		return &ExtractionError{Code: "structured_resource_limit"}
	}
	return nil
}

// Optional providers are checked at the Agent boundary as well as before cache
// writes. Source identity and provenance must never come from model output.
func ValidateStructuredMaterial(source StructuredSource, material StructuredMaterial) error {
	if err := ValidateStructuredSource(source); err != nil {
		return err
	}
	if material.DocumentID != source.DocumentID || material.SourceVersion != source.SourceVersion || material.SourceURL != StructuredURL(source.SourceVersion) || material.ParserVersion != StructuredParserVersion || !structuredHash.MatchString(material.ContentHash) {
		return &ExtractionError{Code: "structured_source_mismatch"}
	}
	if material.Elements == nil || len(material.Elements)+len(material.Gaps) > StructuredElementCount {
		return &ExtractionError{Code: "structured_resource_limit"}
	}
	seen := map[string]bool{}
	for _, element := range material.Elements {
		if err := ValidateStructuredElement(element); err != nil {
			return err
		}
		if seen[element.Anchor] {
			return &ExtractionError{Code: "structured_html_invalid"}
		}
		seen[element.Anchor] = true
	}
	for _, gap := range material.Gaps {
		if (gap.Kind != "table" && gap.Kind != "formula") || !structuredAnchor.MatchString(gap.Anchor) || (gap.Reason != "unsupported_structure" && gap.Reason != "element_budget" && gap.Reason != "missing_tex") {
			return &ExtractionError{Code: "structured_html_invalid"}
		}
		if seen[gap.Anchor] {
			return &ExtractionError{Code: "structured_html_invalid"}
		}
		seen[gap.Anchor] = true
	}
	raw, err := json.Marshal(material)
	if err != nil || len(raw) > StructuredMaterialLimit {
		return &ExtractionError{Code: "structured_resource_limit"}
	}
	return nil
}
