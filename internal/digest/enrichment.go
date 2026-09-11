package digest

import (
	"signalwatch/internal/insight"
	"strings"
)

// Show only the overview paragraph. Detailed themes and per-paper summaries stay
// out of the email. Validate references against exactly the selected papers.
func overviewText(items []Item, language string, results []insight.DigestResult) (string, error) {
	papers := make([]insight.Paper, len(items))
	for i, p := range items {
		papers[i] = insight.Paper{ID: p.PaperID, Title: p.Title, Abstract: p.Abstract}
	}
	var paragraphs []string
	for _, lang := range insight.Languages(language) {
		for _, r := range results {
			if r.Language != lang {
				continue
			}
			if err := insight.ValidateOverview(r.Content, papers); err != nil {
				return "", err
			}
			paragraphs = append(paragraphs, compactOverview(r.Content.Summary, lang))
			break
		}
	}
	return strings.Join(paragraphs, "\n\n"), nil
}

// Keep cached long generations from expanding the email again. The ellipsis
// explicitly marks an excerpt; the stored result is unchanged.
func compactOverview(text, language string) string {
	text = strings.TrimSpace(text)
	limit := 240
	if language == "en" {
		limit = 600
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	excerpt := string(runes[:limit])
	if language == "en" {
		if i := strings.LastIndex(excerpt, " "); i > len(excerpt)/2 {
			excerpt = excerpt[:i]
		}
	}
	return strings.TrimSpace(excerpt) + "…"
}
