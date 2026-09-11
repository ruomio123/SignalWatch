// Package rules contains database-independent subscription matching rules.
package rules

import "strings"

type Rule struct {
	Category string
	Keywords []string
}
type Paper struct {
	Title, Abstract string
	Categories      []string
}

// Match is shared by stream processing and historical backfill. Temporal
// eligibility is decided by the calling use case, never by this predicate.
func Match(p Paper, rule Rule) ([]string, bool) {
	found := false
	for _, c := range p.Categories {
		if c == rule.Category {
			found = true
			break
		}
	}
	if !found {
		return nil, false
	}
	text := strings.ToLower(strings.Join(strings.Fields(p.Title+" "+p.Abstract), " "))
	hits := []string{}
	seen := map[string]bool{}
	for _, k := range rule.Keywords {
		normalized := strings.ToLower(strings.Join(strings.Fields(k), " "))
		if !seen[normalized] && strings.Contains(text, normalized) {
			hits = append(hits, k)
			seen[normalized] = true
		}
	}
	return hits, len(rule.Keywords) == 0 || len(hits) > 0
}
