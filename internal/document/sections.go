package document

import (
	"regexp"
	"strings"
)

var numberedHeading = regexp.MustCompile(`^(?:[1-9][0-9]*(?:\.[0-9]+)*\.?|[A-Z](?:\.[0-9]+)*\.?)\s+[A-Z][\p{L} ,:()/-]{2,100}$`)
var namedHeading = regexp.MustCompile(`(?i)^(abstract|introduction|background|related work|methods?|methodology|approach|experiments?|evaluation|results?|discussion|limitations?|conclusions?|references|acknowledg[e]?ments?|appendix(?: [A-Z])?)$`)

// IdentifySections runs before whitespace normalization. Locations refer to
// the original extracted page and line, not invented semantic boundaries.
func IdentifySections(pages []string) []Section {
	out := []Section{}
	for page, text := range pages {
		for line, raw := range strings.Split(text, "\n") {
			title := strings.Join(strings.Fields(raw), " ")
			if namedHeading.MatchString(title) || numberedHeading.MatchString(title) {
				out = append(out, Section{Title: title, Page: page + 1, Line: line + 1})
			}
		}
	}
	return out
}
