package agent

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"signalwatch/internal/document"
	"sort"
	"strings"
	"time"
)

// Structural extraction is optional local preparation, not another model step.
// Its complete evidence and failures freeze before the first model request.
func (s *Service) capturePaperStructure(ctx context.Context, r Run, cp *Checkpoint, doc document.Document, check func(context.Context) error) error {
	pc := cp.Paper
	if pc.StructuredCaptured {
		return nil
	}
	pc.StructuredEvidence = []Citation{}
	pc.StructuredGap = "unavailable"
	if s.Structured != nil {
		deadline := time.Now().Add(20 * time.Second)
		if pc.PreparationDeadline != nil {
			deadline = *pc.PreparationDeadline
		}
		if deadline.After(time.Now()) {
			preparation, stop := context.WithDeadline(ctx, deadline)
			source := document.StructuredSource{DocumentID: doc.ID, SourceVersion: doc.SourceVersion}
			material, err := s.Structured.Structured(preparation, source)
			stop()
			if err == nil {
				err = document.ValidateStructuredMaterial(source, material)
			}
			if err == nil {
				pc.StructuredGap = ""
				if len(material.Gaps) > 0 {
					pc.StructuredGap = "incomplete"
				}
				for _, e := range material.Elements {
					pc.StructuredEvidence = append(pc.StructuredEvidence, Citation{
						ID: e.ID, DocumentID: doc.ID, ContentHash: doc.ContentHash, Quote: e.Quote,
						URL: material.SourceURL + "#" + url.PathEscape(e.Anchor), SourceType: "html",
						SourceVersion: material.SourceVersion, SourceHash: material.ContentHash, ParserVersion: material.ParserVersion,
						Anchor: e.Anchor, Label: e.Label, Kind: e.Kind, Table: e.Table, Formula: e.Formula,
					})
				}
			}
		} else {
			pc.StructuredGap = "preparation_budget"
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := check(ctx); err != nil {
		return err
	}
	pc.StructuredCaptured = true
	return s.Store.Save(ctx, r, *cp, "planning_paper", nil)
}

var structuralReference = regexp.MustCompile(`(?i)(table|tab\.?|表|equation|eq\.?|公式|式)\s*[（(]?\s*([a-z]?\d+(?:\.\d+)*(?:[a-z])?)`)
var structuralNumber = regexp.MustCompile(`(?i)[a-z]?\d+(?:\.\d+)*(?:[a-z])?`)

func explicitPaperStructures(evidence []Citation, queries []string) []Citation {
	ordered := append([]Citation(nil), evidence...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	groups := [][]Citation{}
	for _, query := range queries {
		for _, match := range structuralReference.FindAllStringSubmatch(query, -1) {
			kind := "formula"
			if strings.HasPrefix(strings.ToLower(match[1]), "tab") || match[1] == "表" {
				kind = "table"
			}
			group := []Citation{}
			for _, source := range ordered {
				if source.SourceType == "html" && source.Kind == kind && strings.EqualFold(structuralNumber.FindString(source.Label), match[2]) {
					group = append(group, source)
				}
			}
			groups = append(groups, group)
		}
	}
	out := []Citation{}
	seen := map[string]bool{}
	for position := 0; ; position++ {
		more := false
		for _, group := range groups {
			if position < len(group) {
				more = true
				if !seen[group[position].ID] {
					out = append(out, group[position])
					seen[group[position].ID] = true
				}
			}
		}
		if !more {
			return out
		}
	}
}

// Publication uses the frozen source snapshot, never model-authored metadata.
func frozenStructuredCitation(pc *PaperCheckpoint, ref Citation) bool {
	if pc == nil || !pc.StructuredCaptured || ref.SourceVersion != pc.SourceVersion || ref.ContentHash != pc.ContentHash {
		return false
	}
	ref.ID = ""
	want, _ := json.Marshal(ref)
	for _, source := range pc.StructuredEvidence {
		source.ID = ""
		got, _ := json.Marshal(source)
		if string(want) == string(got) {
			return true
		}
	}
	return false
}
