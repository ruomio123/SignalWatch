package paper

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

var ErrInvalidRecord = errors.New("invalid normalized paper record")

func NormalizeRecord(record Record) (Record, error) {
	record.ArXivID = strings.TrimSpace(record.ArXivID)
	record.Title = normalizeWhitespace(record.Title)
	record.Abstract = normalizeWhitespace(record.Abstract)
	record.Comments = normalizeWhitespace(record.Comments)
	record.Authors = normalizeList(record.Authors)
	record.Categories = normalizeList(record.Categories)
	record.ArXivURL = strings.TrimSpace(record.ArXivURL)
	record.PDFURL = strings.TrimSpace(record.PDFURL)

	if record.ArXivID == "" || len(record.ArXivID) > 64 || !utf8.ValidString(record.ArXivID) ||
		record.Title == "" || record.Abstract == "" ||
		len(record.Comments) > 65535 || !utf8.ValidString(record.Comments) ||
		len(record.Authors) == 0 || len(record.Categories) == 0 ||
		record.PublishedAt.IsZero() || record.ArXivUpdatedAt.IsZero() ||
		!validArXivURL(record.ArXivURL, "/abs/") ||
		!validArXivURL(record.PDFURL, "/pdf/") {
		return Record{}, ErrInvalidRecord
	}

	record.PublishedAt = record.PublishedAt.UTC()
	record.ArXivUpdatedAt = record.ArXivUpdatedAt.UTC()
	return record, nil
}

func validArXivURL(raw, pathPrefix string) bool {
	parsed, err := url.Parse(raw)
	return err == nil &&
		parsed.Scheme == "https" &&
		strings.EqualFold(parsed.Hostname(), "arxiv.org") &&
		strings.HasPrefix(parsed.EscapedPath(), pathPrefix)
}

func normalizeWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func normalizeList(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := normalizeWhitespace(value)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}
