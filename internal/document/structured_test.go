package document

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func structuredTestMaterial() (StructuredSource, StructuredMaterial) {
	source := StructuredSource{DocumentID: "document-1", SourceVersion: "2609.31620v1"}
	element := StructuredElement{ID: StructuredElementID("table", "S5.T1"), Kind: "table", Label: "Table 1", Anchor: "S5.T1", Table: &StructuredTable{Caption: "Numerical results", Headers: []string{"Method", "PSNR ↑", "rFID ↓"}, Rows: [][]string{{"FuseReg", "27.52", "0.42"}}, Notes: []string{"Same evaluation split."}}}
	element.Quote = StructuredQuote(element)
	return source, StructuredMaterial{DocumentID: source.DocumentID, SourceVersion: source.SourceVersion, ParserVersion: StructuredParserVersion, ContentHash: strings.Repeat("a", 64), SourceURL: StructuredURL(source.SourceVersion), Elements: []StructuredElement{element}, Gaps: []StructuredGap{}}
}

func cloneStructuredTestMaterial(material StructuredMaterial) StructuredMaterial {
	raw, _ := json.Marshal(material)
	var clone StructuredMaterial
	_ = json.Unmarshal(raw, &clone)
	return clone
}

func TestStructuredMaterialContract(t *testing.T) {
	source, original := structuredTestMaterial()
	if err := ValidateStructuredMaterial(source, original); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*StructuredMaterial)
	}{
		{"document", func(m *StructuredMaterial) { m.DocumentID = "other" }},
		{"version", func(m *StructuredMaterial) { m.SourceVersion = "2609.31620v2" }},
		{"latest_url", func(m *StructuredMaterial) { m.SourceURL = "https://arxiv.org/html/2609.31620" }},
		{"external_url", func(m *StructuredMaterial) { m.SourceURL = "https://example.com/html/2609.31620v1" }},
		{"parser", func(m *StructuredMaterial) { m.ParserVersion = "other" }},
		{"hash", func(m *StructuredMaterial) { m.ContentHash = "not-a-hash" }},
		{"nil_elements", func(m *StructuredMaterial) { m.Elements = nil }},
		{"id", func(m *StructuredMaterial) { m.Elements[0].ID = "p1-c1-s1" }},
		{"unsafe_anchor", func(m *StructuredMaterial) { m.Elements[0].Anchor = "S5.T1#script" }},
		{"quote_mismatch", func(m *StructuredMaterial) { m.Elements[0].Quote += " invented" }},
		{"ragged_rows", func(m *StructuredMaterial) { m.Elements[0].Table.Rows[0] = []string{"FuseReg"} }},
		{"nil_notes", func(m *StructuredMaterial) { m.Elements[0].Table.Notes = nil }},
		{"blank_header", func(m *StructuredMaterial) { m.Elements[0].Table.Headers[0] = "" }},
		{"duplicate_element", func(m *StructuredMaterial) { m.Elements = append(m.Elements, m.Elements[0]) }},
		{"duplicate_gap_anchor", func(m *StructuredMaterial) {
			m.Gaps = []StructuredGap{{Kind: "table", Anchor: "S5.T1", Reason: "element_budget"}}
		}},
		{"unsafe_gap", func(m *StructuredMaterial) {
			m.Gaps = []StructuredGap{{Kind: "formula", Anchor: "S3.E2", Reason: "raw internal failure"}}
		}},
		{"wrong_union", func(m *StructuredMaterial) { m.Elements[0].Formula = &StructuredFormula{TeX: "x"} }},
		{"utf8", func(m *StructuredMaterial) { m.Elements[0].Table.Rows[0][0] = string([]byte{0xff}) }},
		{"element_bytes", func(m *StructuredMaterial) {
			m.Elements[0].Table.Rows = make([][]string, 20)
			for i := range m.Elements[0].Table.Rows {
				m.Elements[0].Table.Rows[i] = []string{strings.Repeat("中", 1000), "1", "2"}
			}
			m.Elements[0].Quote = StructuredQuote(m.Elements[0])
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := cloneStructuredTestMaterial(original)
			tc.mutate(&m)
			if err := ValidateStructuredMaterial(source, m); err == nil {
				t.Fatal("invalid material accepted")
			}
		})
	}
	for _, version := range []string{"2609.31620", "2609.31620v0", "https://arxiv.org/abs/2609.31620v1", "2609.31620v1/../../other"} {
		source.SourceVersion = version
		if ValidateStructuredSource(source) == nil {
			t.Fatalf("accepted version %q", version)
		}
	}
	source.SourceVersion = "hep-th/9901001v2"
	if err := ValidateStructuredSource(source); err != nil {
		t.Fatal(err)
	}
}

func TestStructuredFormulaAndCombinedLimit(t *testing.T) {
	source, material := structuredTestMaterial()
	element := StructuredElement{ID: "h-formula-S3.E2", Kind: "formula", Anchor: "S3.E2", Label: "Equation 2", Formula: &StructuredFormula{TeX: `z_m=\frac{\sum_k m_k h_k}{\sum_k m_k},\quad\sum_k m_k>0`, Context: "A complete definition paragraph."}}
	element.Quote = StructuredQuote(element)
	material.Elements = []StructuredElement{element}
	if err := ValidateStructuredMaterial(source, material); err != nil {
		t.Fatal(err)
	}
	element.Formula.TeX = strings.Repeat("中", StructuredFormulaLimit/3+1)
	element.Quote = StructuredQuote(element)
	if ValidateStructuredElement(element) == nil {
		t.Fatal("oversized UTF-8 formula accepted")
	}
	material.Elements = make([]StructuredElement, 0)
	material.Gaps = make([]StructuredGap, StructuredElementCount+1)
	if ValidateStructuredMaterial(source, material) == nil {
		t.Fatal("combined count limit accepted")
	}
}

type structuredMemoryStore struct {
	material         *StructuredMaterial
	saves, loads     int
	loadErr, saveErr error
}

func (s *structuredMemoryStore) LoadStructured(context.Context, StructuredSource) (StructuredMaterial, error) {
	s.loads++
	if s.loadErr != nil {
		return StructuredMaterial{}, s.loadErr
	}
	if s.material == nil {
		return StructuredMaterial{}, ErrNotFound
	}
	return cloneStructuredTestMaterial(*s.material), nil
}
func (s *structuredMemoryStore) SaveStructured(_ context.Context, _ StructuredSource, m StructuredMaterial) error {
	s.saves++
	if s.saveErr != nil {
		return s.saveErr
	}
	if s.material == nil {
		copy := cloneStructuredTestMaterial(m)
		s.material = &copy
	}
	return nil
}

type structuredTestExtractor struct {
	material StructuredMaterial
	err      error
	calls    int
	after    func()
}

func (e *structuredTestExtractor) ExtractStructured(context.Context, StructuredSource) (StructuredMaterial, error) {
	e.calls++
	if e.after != nil {
		e.after()
	}
	return cloneStructuredTestMaterial(e.material), e.err
}

func TestStructuredServiceCacheAndFailure(t *testing.T) {
	source, material := structuredTestMaterial()
	store := &structuredMemoryStore{}
	extractor := &structuredTestExtractor{material: material}
	service := NewStructuredService(store, extractor)
	for range 2 {
		got, err := service.Structured(context.Background(), source)
		if err != nil || got.ContentHash != material.ContentHash {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	}
	if extractor.calls != 1 || store.saves != 1 {
		t.Fatalf("calls=%d saves=%d", extractor.calls, store.saves)
	}
	store = &structuredMemoryStore{}
	extractor = &structuredTestExtractor{material: material, err: &ExtractionError{Code: "structured_html_unavailable"}}
	service = NewStructuredService(store, extractor)
	if _, err := service.Structured(context.Background(), source); err == nil {
		t.Fatal("failure accepted")
	}
	if store.saves != 0 {
		t.Fatal("failure cached")
	}
	extractor.err = nil
	if _, err := service.Structured(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if extractor.calls != 2 || store.saves != 1 {
		t.Fatal("failure incorrectly negative-cached")
	}
	store.material.SourceVersion = "2609.31620v2"
	if _, err := service.Structured(context.Background(), source); err == nil {
		t.Fatal("invalid cached version accepted")
	}
	if extractor.calls != 2 {
		t.Fatal("corrupt cache silently refetched")
	}
}

func TestStructuredServiceCancellationAndValidation(t *testing.T) {
	source, material := structuredTestMaterial()
	t.Run("already_cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		store := &structuredMemoryStore{}
		extractor := &structuredTestExtractor{material: material}
		_, err := NewStructuredService(store, extractor).Structured(ctx, source)
		if !errors.Is(err, context.Canceled) || store.loads != 0 || extractor.calls != 0 {
			t.Fatalf("err=%v loads=%d calls=%d", err, store.loads, extractor.calls)
		}
	})
	t.Run("after_extract", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store := &structuredMemoryStore{}
		extractor := &structuredTestExtractor{material: material, after: cancel}
		_, err := NewStructuredService(store, extractor).Structured(ctx, source)
		if !errors.Is(err, context.Canceled) || store.saves != 0 {
			t.Fatalf("err=%v saves=%d", err, store.saves)
		}
	})
	t.Run("bad_provider", func(t *testing.T) {
		store := &structuredMemoryStore{}
		invalid := cloneStructuredTestMaterial(material)
		invalid.SourceURL = "https://evil.example"
		_, err := NewStructuredService(store, &structuredTestExtractor{material: invalid}).Structured(context.Background(), source)
		if err == nil || store.saves != 0 {
			t.Fatal("invalid material cached")
		}
	})
	t.Run("store_failure", func(t *testing.T) {
		want := errors.New("store unavailable")
		store := &structuredMemoryStore{loadErr: want}
		extractor := &structuredTestExtractor{material: material}
		_, err := NewStructuredService(store, extractor).Structured(context.Background(), source)
		if !errors.Is(err, want) || extractor.calls != 0 {
			t.Fatal("store failure hidden")
		}
	})
}
