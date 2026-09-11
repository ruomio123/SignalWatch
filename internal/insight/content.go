// Package insight defines versioned, validated AI content without network dependencies.
package insight

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const SchemaVersion = "summary-v1"

type Paper struct {
	ID       uint64 `json:"id"`
	Title    string `json:"title"`
	Abstract string `json:"abstract"`
}
type Evidence struct {
	Field string `json:"field"`
	Quote string `json:"quote"`
}
type Application struct {
	Text     string `json:"text"`
	Inferred bool   `json:"inferred"`
}
type Summary struct {
	Summary       string        `json:"summary"`
	Contributions []string      `json:"contributions"`
	Method        string        `json:"method"`
	Applications  []Application `json:"applications"`
	Evidence      []Evidence    `json:"evidence"`
	Limitations   string        `json:"limitations"`
}
type Theme struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	PaperIDs    []uint64 `json:"paper_ids"`
}
type Overview struct {
	Summary string  `json:"summary"`
	Themes  []Theme `json:"themes"`
}
type PaperResult struct {
	Language string  `json:"language"`
	Content  Summary `json:"content"`
}
type DigestResult struct {
	Language string   `json:"language"`
	Content  Overview `json:"content"`
}
type Enrichment struct {
	Papers  map[uint64][]PaperResult
	Digests []DigestResult
}

func Hash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func PaperHash(p Paper) string {
	return Hash(struct {
		Title    string
		Abstract string
	}{p.Title, p.Abstract})
}
func Languages(language string) []string {
	if language == "both" {
		return []string{"zh", "en"}
	}
	if language == "zh" || language == "en" {
		return []string{language}
	}
	return nil
}
func Decode(raw []byte, v any) error {
	if len(raw) > 64<<10 {
		return errors.New("output_too_large")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("invalid_output")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("invalid_output")
	}
	return nil
}

// Explicitly require the inference marker, so an omitted/null marker cannot
// silently turn a guessed use case into a claimed fact.
func (a *Application) UnmarshalJSON(raw []byte) error {
	var value struct {
		Text     string `json:"text"`
		Inferred *bool  `json:"inferred"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil || value.Inferred == nil {
		return errors.New("invalid_application")
	}
	a.Text, a.Inferred = value.Text, *value.Inferred
	return nil
}

func valid(s string, max int, required bool) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && (!required || strings.TrimSpace(s) != "")
}
func ValidateSummary(s Summary, p Paper) error {
	if !valid(s.Summary, 1200, true) || !valid(s.Method, 1200, true) || !valid(s.Limitations, 800, true) || s.Contributions == nil || len(s.Contributions) > 3 || s.Applications == nil || len(s.Applications) > 3 || len(s.Evidence) == 0 || len(s.Evidence) > 6 {
		return errors.New("invalid_summary")
	}
	for _, v := range s.Contributions {
		if !valid(v, 600, true) {
			return errors.New("invalid_contribution")
		}
	}
	for _, v := range s.Applications {
		if !valid(v.Text, 600, true) {
			return errors.New("invalid_application")
		}
	}
	for _, e := range s.Evidence {
		text := ""
		if e.Field == "title" {
			text = p.Title
		} else if e.Field == "abstract" {
			text = p.Abstract
		}
		if !valid(e.Quote, 500, true) || !strings.Contains(text, e.Quote) {
			return errors.New("unsupported_evidence")
		}
	}
	return nil
}
func ValidateOverview(s Overview, papers []Paper) error {
	if len(papers) < 2 || !valid(s.Summary, 1600, true) || s.Themes == nil || len(s.Themes) > 3 {
		return errors.New("invalid_overview")
	}
	ids := map[uint64]bool{}
	for _, p := range papers {
		ids[p.ID] = true
	}
	for _, t := range s.Themes {
		if !valid(t.Title, 160, true) || !valid(t.Description, 1000, true) || len(t.PaperIDs) == 0 || len(t.PaperIDs) > len(papers) {
			return errors.New("invalid_theme")
		}
		seen := map[uint64]bool{}
		for _, id := range t.PaperIDs {
			if !ids[id] || seen[id] {
				return errors.New("unsupported_paper")
			}
			seen[id] = true
		}
	}
	return nil
}
