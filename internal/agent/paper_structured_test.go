package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"signalwatch/internal/document"
)

func structuredAgentTable(number int, topic string) document.StructuredElement {
	element := document.StructuredElement{Kind: "table", Label: fmt.Sprintf("Table %d", number), Anchor: fmt.Sprintf("S2.T%d", number), Table: &document.StructuredTable{
		Caption: topic + " reported accuracy", Headers: []string{"Method", "Accuracy (%)"}, Rows: [][]string{{"baseline", "85.0"}, {"proposed", "90.0"}}, Notes: []string{"Measured on the held-out test split."},
	}}
	element.ID = document.StructuredElementID(element.Kind, element.Anchor)
	element.Quote = document.StructuredQuote(element)
	return element
}

func structuredAgentFormula(number int) document.StructuredElement {
	element := document.StructuredElement{Kind: "formula", Label: fmt.Sprintf("Equation (%d)", number), Anchor: fmt.Sprintf("S3.E%d", number), Formula: &document.StructuredFormula{TeX: `L = -\sum_i y_i \log p_i`, Context: "The objective minimizes cross-entropy over labeled examples."}}
	element.ID = document.StructuredElementID(element.Kind, element.Anchor)
	element.Quote = document.StructuredQuote(element)
	return element
}

func structuredAgentCitation(element document.StructuredElement) Citation {
	return Citation{ID: element.ID, DocumentID: "pinned-document", ContentHash: "pdf-hash", Quote: element.Quote, URL: "https://arxiv.org/html/1706.03762v1#" + element.Anchor,
		SourceType: "html", SourceVersion: "1706.03762v1", SourceHash: strings.Repeat("a", 64), ParserVersion: document.StructuredParserVersion, Anchor: element.Anchor, Label: element.Label, Kind: element.Kind, Table: element.Table, Formula: element.Formula}
}

type structuredAgentProvider struct {
	elements     []document.StructuredElement
	calls        []document.StructuredSource
	err          error
	wrongVersion bool
}

func (p *structuredAgentProvider) Structured(_ context.Context, source document.StructuredSource) (document.StructuredMaterial, error) {
	p.calls = append(p.calls, source)
	if p.err != nil {
		return document.StructuredMaterial{}, p.err
	}
	version := source.SourceVersion
	if p.wrongVersion {
		version = "1706.03762v2"
	}
	return document.StructuredMaterial{DocumentID: source.DocumentID, SourceVersion: version, ParserVersion: document.StructuredParserVersion, ContentHash: strings.Repeat("a", 64), SourceURL: "https://arxiv.org/html/" + version, Elements: p.elements}, nil
}

func TestPaperStructuredUnitsStayWholeDuringPassageConstruction(t *testing.T) {
	table := structuredAgentTable(2, strings.Repeat("完整表格上下文🙂 ", 100))
	formula := structuredAgentFormula(3)
	formula.Formula.Context = strings.Repeat("The symbols and assumptions remain together. ", 50)
	formula.Quote = document.StructuredQuote(formula)
	sources := []Citation{{ID: "p1-c1", Page: 1, Quote: strings.Repeat("a", 2000)}, structuredAgentCitation(table), structuredAgentCitation(formula)}
	got := paperEvidence(sources)
	if len(got) != 4 || got[0].ID != "p1-c1-s0" || got[1].ID != "p1-c1-s1000" || !reflect.DeepEqual(got[2:], sources[1:]) {
		t.Fatalf("structured evidence was split or lost source metadata: ids=%v", structuredAgentIDs(got))
	}
	projected := evidencePassages(got)
	if projected[2].Quote != table.Quote || projected[3].Quote != formula.Quote || len(projected[2].Quote) <= paperPassageLimit || len(projected[3].Quote) <= paperPassageLimit {
		t.Fatal("model evidence omitted part of a table or formula")
	}
}

func structuredAgentIDs(sources []Citation) []string {
	ids := make([]string, len(sources))
	for i, source := range sources {
		ids[i] = source.ID
	}
	return ids
}

func TestPaperStructuredRankingKeepsHTMLIDsAndPrioritizesExplicitLabels(t *testing.T) {
	table := structuredAgentCitation(structuredAgentTable(2, "accuracy"))
	formula := structuredAgentCitation(structuredAgentFormula(3))
	body := Citation{ID: "p1-c1-s0", Page: 1, Quote: strings.Repeat("table equation accuracy objective ", 30)}
	other := structuredAgentCitation(structuredAgentTable(7, "unrelated"))
	for _, tc := range []struct{ query, id string }{{"Table 2", table.ID}, {"Equation (3)", formula.ID}, {"Eq. (3)", formula.ID}} {
		t.Run(tc.query, func(t *testing.T) {
			got := rankPaperEvidence([]Citation{body, other, formula, table}, []string{tc.query}, 1)
			if len(got) != 1 || got[0].ID != tc.id || got[0].SourceType != "html" {
				t.Fatalf("explicit structured reference lost to text ranking: got=%v want=%s", structuredAgentIDs(got), tc.id)
			}
		})
	}
	if got := rankPaperEvidence([]Citation{body, other, formula, table}, []string{"unmentioneduniqueconcept"}, 24); len(got) != 0 {
		t.Fatalf("zero-match query invented structured evidence: %v", structuredAgentIDs(got))
	}
}

func TestPaperStructuredRankingAndPackingKeep24And32WholeUnits(t *testing.T) {
	sources := []Citation{}
	queries := []string{"alpha", "beta", "gamma", "delta"}
	for q, topic := range queries {
		for i := 0; i < 10; i++ {
			sources = append(sources, structuredAgentCitation(structuredAgentTable(q*10+i+1, topic)))
		}
	}
	for _, limit := range []int{24, 32} {
		got := rankPaperEvidence(sources, queries, limit)
		if len(got) != limit {
			t.Fatalf("HTML IDs were discarded or selection limit changed: limit=%d got=%d", limit, len(got))
		}
		seen := map[string]bool{}
		for _, source := range got {
			if seen[source.ID] || source.SourceType != "html" || source.Table == nil || source.Anchor == "" {
				t.Fatal("typed evidence duplicated or lost provenance")
			}
			seen[source.ID] = true
		}
	}
	questions := []PaperQuestion{{ID: "q1", Question: "Compare the reported tables", Query: "accuracy"}}
	pc := &PaperCheckpoint{Mode: "fulltext"}
	r := Run{Question: questions[0].Question}
	initial, err := packPaperAnswerInput(r, pc, questions, sources)
	must(t, err)
	if len(initial.Evidence) != 24 || !reflect.DeepEqual(initial.Evidence, sources[:24]) {
		t.Fatal("initial structured evidence was split or exceeded 24 units")
	}
	answer := singleQuestionAnswer(fieldOutput{Status: "partial", Claims: []claimOutput{{Text: "The first table reports accuracy.", Evidence: []evidenceOutput{{ID: sources[0].ID}}}}})
	answer.SupplementalQueries = []PaperSupplementQuery{{QuestionID: "q1", Query: "additional tables"}}
	input, added, err := packPaperSupplementInput(r, pc, questions, initial, budgetJSON(t, answer), sources[24:])
	must(t, err)
	if added != 8 || len(input.Evidence) != 32 || input.Evidence[0].ID != sources[0].ID || len(input.Request) > paperInputLimit {
		t.Fatalf("supplementary structured budget changed: added=%d total=%d", added, len(input.Evidence))
	}
	index := evidenceIndex(sources)
	for _, source := range input.Evidence {
		if !reflect.DeepEqual(source, index[source.ID]) {
			t.Fatal("packing modified a complete structural unit")
		}
	}
}

func TestPaperStructuredRequestPackingUsesSerializedBytesWithoutClipping(t *testing.T) {
	questions := []PaperQuestion{{ID: "q1", Question: "Explain Table 2", Query: "Table 2"}}
	pc := &PaperCheckpoint{Mode: "fulltext", ConversationContext: PaperConversationContext{Turns: []PaperConversationTurn{{Question: strings.Repeat("Q", 2000), Answer: strings.Repeat("A", 4000)}}}}
	candidates := []Citation{}
	for i := 0; i < 12; i++ {
		element := structuredAgentTable(i+1, "accuracy")
		element.Table.Notes = []string{strings.Repeat("<", 1400) + "中文🙂"}
		element.Quote = document.StructuredQuote(element)
		candidates = append(candidates, structuredAgentCitation(element))
	}
	snapshot := string(budgetJSON(t, pc.ConversationContext))
	packed, err := packPaperAnswerInput(Run{Question: "Explain the complete table🙂"}, pc, questions, candidates)
	must(t, err)
	if len(packed.Request) > paperInputLimit || len(packed.Evidence) == 0 || len(packed.Evidence) >= len(candidates) {
		t.Fatalf("escaped structured input was not bounded: bytes=%d units=%d", len(packed.Request), len(packed.Evidence))
	}
	var request struct {
		Evidence []evidencePassage `json:"evidence"`
	}
	must(t, json.Unmarshal([]byte(packed.Request), &request))
	for i, source := range packed.Evidence {
		if !reflect.DeepEqual(source, candidates[i]) || request.Evidence[i].ID != source.ID || request.Evidence[i].Quote != source.Quote {
			t.Fatal("packing clipped table headers, cells, notes or provenance")
		}
	}
	if string(budgetJSON(t, pc.ConversationContext)) != snapshot {
		t.Fatal("packing changed the captured background")
	}
	if pc.StructuredGap != "input_budget" {
		t.Fatal("omitted structural units did not retain the input budget gap")
	}
}

func TestPaperStructuredOversizedUnitKeepsOrdinaryEvidence(t *testing.T) {
	element := structuredAgentFormula(3)
	element.Formula.TeX, element.Formula.Context = strings.Repeat("<", 2500), ""
	element.Quote = document.StructuredQuote(element)
	must(t, document.ValidateStructuredElement(element))
	ordinary := Citation{ID: "p1-c1-s0", Page: 1, Quote: "The method minimizes the training loss."}
	pc := &PaperCheckpoint{Mode: "fulltext", Context: PaperContext{Abstract: strings.Repeat("<", 8500)}}
	questions := []PaperQuestion{{ID: "q1", Question: "Explain Equation 3", Query: "Equation 3"}}
	packed, err := packPaperAnswerInput(Run{Question: questions[0].Question}, pc, questions, []Citation{structuredAgentCitation(element), ordinary})
	must(t, err)
	if len(packed.Request) > paperInputLimit || len(packed.Evidence) != 1 || !reflect.DeepEqual(packed.Evidence[0], ordinary) || pc.StructuredGap != "input_budget" {
		t.Fatalf("oversized formula was clipped or discarded ordinary evidence: ids=%v gap=%s", structuredAgentIDs(packed.Evidence), pc.StructuredGap)
	}
}

func TestPaperStructuredPublicationMatchesFrozenProvenance(t *testing.T) {
	source := structuredAgentCitation(structuredAgentTable(2, "accuracy"))
	pc := &PaperCheckpoint{SourceVersion: source.SourceVersion, ContentHash: source.ContentHash, StructuredCaptured: true, StructuredEvidence: []Citation{source}}
	public := source
	public.ID = "answer-q1-1-1"
	if !frozenStructuredCitation(pc, public) {
		t.Fatal("public citation ID prevented matching the frozen source")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Citation)
	}{
		{"document", func(ref *Citation) { ref.DocumentID = "another-document" }},
		{"pdf hash", func(ref *Citation) { ref.ContentHash = "another-pdf-hash" }},
		{"html hash", func(ref *Citation) { ref.SourceHash = strings.Repeat("b", 64) }},
		{"source version", func(ref *Citation) { ref.SourceVersion = "1706.03762v2" }},
		{"parser", func(ref *Citation) { ref.ParserVersion = "another-parser" }},
		{"anchor", func(ref *Citation) { ref.Anchor = "S2.T3" }},
		{"url", func(ref *Citation) { ref.URL = "https://arxiv.org/html/1706.03762v2#S2.T2" }},
		{"label", func(ref *Citation) { ref.Label = "Table 3" }},
		{"quote", func(ref *Citation) { ref.Quote += "modified" }},
		{"cell", func(ref *Citation) { ref.Table.Rows[0][1] = "99.9" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed Citation
			must(t, json.Unmarshal(budgetJSON(t, public), &changed))
			tc.mutate(&changed)
			if frozenStructuredCitation(pc, changed) {
				t.Fatal("publication accepted changed structural provenance")
			}
		})
	}
}

func TestPaperStructuredAnswersPublishTypedSourceProvenance(t *testing.T) {
	for _, kind := range []string{"table", "formula"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			c, doc := workflowPaper(t, f, []string{"The retrieval method discusses accuracy and training objectives."})
			element, query := structuredAgentTable(2, "accuracy"), "Table 2"
			if kind == "formula" {
				element, query = structuredAgentFormula(3), "Equation (3)"
			}
			provider := &structuredAgentProvider{elements: []document.StructuredElement{element}}
			f.s.Structured = provider
			f.gateway.actions = []string{
				string(budgetJSON(t, paperQuestionsOutput{Questions: []paperQuestionItemOutput{{Question: "说明论文中的" + element.Label, Query: query}}})),
				string(budgetJSON(t, singleQuestionAnswer(fieldOutput{Status: "supported", Claims: []claimOutput{{Text: "该结论来自完整结构化原文。", Evidence: []evidenceOutput{{ID: element.ID}}}}}))),
				`{"verdicts":[{"id":"q1-1","supported":true}]}`,
			}
			r := directSubmit(t, f, c, TaskPaperFollowup, "fulltext")
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			if end.State != "completed" || len(messages) != 2 || f.gateway.calls.Load() != 3 || len(provider.calls) != 1 {
				t.Fatalf("structured source failed publication: end=%+v calls=%d provider=%d", end, f.gateway.calls.Load(), len(provider.calls))
			}
			var citations []Citation
			must(t, json.Unmarshal(messages[1].Citations, &citations))
			if len(citations) != 1 {
				t.Fatal("structured source citation missing")
			}
			got := citations[0]
			if got.ID != "answer-q1-1-1" || got.DocumentID != doc.ID || got.ContentHash != "fixture-content-hash" || got.SourceType != "html" || got.SourceVersion != "1706.03762v1" || got.SourceHash != strings.Repeat("a", 64) || got.ParserVersion != document.StructuredParserVersion || got.Anchor != element.Anchor || got.URL != "https://arxiv.org/html/1706.03762v1#"+element.Anchor || got.Quote != element.Quote || got.Kind != kind || !reflect.DeepEqual(got.Table, element.Table) || !reflect.DeepEqual(got.Formula, element.Formula) {
				t.Fatalf("published citation lost typed immutable provenance: %+v", got)
			}
			cp := directCheckpoint(t, f, r)
			if !cp.Paper.StructuredCaptured || len(cp.Paper.StructuredEvidence) != 1 || cp.Paper.StructuredEvidence[0].ID != element.ID || cp.Paper.StructuredGap != "" {
				t.Fatal("structured material was not frozen before analysis")
			}
		})
	}
}

func TestPaperStructuredRecoveryReusesCapturedProviderMaterial(t *testing.T) {
	for _, barrier := range []string{"structured", "normalized", "retrieved"} {
		t.Run(barrier, func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, []string{"The retrieval method evaluates accuracy."})
			provider := &structuredAgentProvider{elements: []document.StructuredElement{structuredAgentTable(2, "retrieval accuracy")}}
			f.s.Structured = provider
			g := &workflowGateway{}
			f.s.Gateway = g
			watch := &questionCheckpointStore{Store: f.store, pause: func(cp Checkpoint, _ string) bool {
				if cp.Paper == nil || !cp.Paper.StructuredCaptured || cp.Phase != "ready" {
					return false
				}
				switch barrier {
				case "structured":
					return true
				case "normalized":
					return cp.Paper.Outputs["normalizing_question"] != nil
				default:
					return cp.Paper.QA != nil && cp.Paper.QA.Initial != nil
				}
			}}
			f.s.Store = watch
			r := f.claim(t, directSubmit(t, f, c, TaskPaperFollowup, "fulltext").ID)
			cp := directCheckpoint(t, f, r)
			err := f.s.processPaper(t.Context(), r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) })
			if !errors.Is(err, errContextCheckpointPause) || !watch.paused || len(provider.calls) != 1 {
				t.Fatalf("missed structural snapshot barrier: %v calls=%d", err, len(provider.calls))
			}
			frozen := directCheckpoint(t, f, r)
			provider.elements = []document.StructuredElement{structuredAgentTable(99, "changed structure")}
			provider.err = &document.ExtractionError{Code: "structured_html_unavailable"}
			must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, _ := paperOutcome(t, f, c, r)
			stored := directCheckpoint(t, f, r)
			if end.State != "completed" || len(g.calls) != 3 || len(provider.calls) != 1 || !reflect.DeepEqual(stored.Paper.StructuredEvidence, frozen.Paper.StructuredEvidence) || stored.Paper.StructuredGap != frozen.Paper.StructuredGap {
				t.Fatalf("resume re-fetched or changed structured material: end=%+v calls=%d provider=%d", end, len(g.calls), len(provider.calls))
			}
			if frozen.Paper.QA != nil && frozen.Paper.QA.Initial != nil && !reflect.DeepEqual(stored.Paper.QA.Initial, frozen.Paper.QA.Initial) {
				t.Fatal("resume changed the frozen typed evidence request")
			}
		})
	}
}

func TestPaperStructuredUnavailableMaterialFallsBackWithoutWrongVersionEvidence(t *testing.T) {
	for _, scenario := range []string{"abstract", "unavailable", "version-mismatch", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			c, _ := workflowPaper(t, f, []string{"The retrieval method evaluates accuracy."})
			provider := &structuredAgentProvider{elements: []document.StructuredElement{structuredAgentTable(2, "retrieval accuracy")}}
			mode, wantCalls := "fulltext", 1
			switch scenario {
			case "abstract":
				mode, wantCalls = "abstract", 0
			case "unavailable":
				provider.err = &document.ExtractionError{Code: "structured_html_unavailable"}
			case "version-mismatch":
				provider.wrongVersion = true
			default:
				provider.elements = []document.StructuredElement{}
			}
			f.s.Structured, f.s.Gateway = provider, &workflowGateway{}
			r := directSubmit(t, f, c, TaskPaperFollowup, mode)
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			cp := directCheckpoint(t, f, r)
			if end.State != "completed" || len(messages) != 2 || len(provider.calls) != wantCalls || len(cp.Paper.StructuredEvidence) != 0 {
				t.Fatalf("unavailable structure broke ordinary answer or leaked wrong evidence: scenario=%s end=%+v provider=%d", scenario, end, len(provider.calls))
			}
			if scenario == "unavailable" || scenario == "version-mismatch" {
				if cp.Paper.StructuredGap == "" || strings.Contains(cp.Paper.StructuredGap, "https://") {
					t.Fatal("structural fallback lost safe gap reason")
				}
			}
			var citations []Citation
			must(t, json.Unmarshal(messages[1].Citations, &citations))
			for _, ref := range citations {
				if ref.SourceType == "html" || ref.Table != nil || ref.Formula != nil {
					t.Fatal("unavailable source leaked into final citations")
				}
			}
		})
	}
}

func TestPaperStructuredMySQLCacheRoundTripAndFirstWriter(t *testing.T) {
	f := newFixture(t)
	_, doc := workflowPaper(t, f, []string{"Pinned PDF material survives the independent HTML cache."})
	store := document.NewMySQLStore(f.db)
	source := document.StructuredSource{DocumentID: doc.ID, SourceVersion: "1706.03762v1"}
	provider := &structuredAgentProvider{elements: []document.StructuredElement{structuredAgentTable(1, "accuracy"), structuredAgentFormula(2)}}
	original, err := provider.Structured(t.Context(), source)
	must(t, err)
	_, err = store.LoadStructured(t.Context(), source)
	if !errors.Is(err, document.ErrNotFound) {
		t.Fatalf("uncached material: %v", err)
	}
	must(t, store.SaveStructured(t.Context(), source, original))
	saved, err := store.LoadStructured(t.Context(), source)
	must(t, err)
	if !reflect.DeepEqual(saved, original) {
		t.Fatal("MySQL JSON changed structured provenance or contents")
	}
	later := original
	later.ContentHash = strings.Repeat("b", 64)
	must(t, store.SaveStructured(t.Context(), source, later))
	saved, err = store.LoadStructured(t.Context(), source)
	must(t, err)
	if saved.ContentHash != original.ContentHash {
		t.Fatal("concurrent cache write replaced a frozen version")
	}
	missing := source
	missing.SourceVersion = "1706.03762v2"
	_, err = store.LoadStructured(t.Context(), missing)
	if !errors.Is(err, document.ErrNotFound) {
		t.Fatal("cache mixed arXiv versions")
	}
	must(t, f.db.Delete(&document.Document{}, "id=?", doc.ID).Error)
	var count int64
	must(t, f.db.Table("paper_document_structures").Where("document_id=?", doc.ID).Count(&count).Error)
	if count != 0 {
		t.Fatal("orphan structural cache after document removal")
	}
}
