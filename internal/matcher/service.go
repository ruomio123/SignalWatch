package matcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"signalwatch/internal/paper"
	"signalwatch/internal/rules"
)

var (
	ErrInvalidPaperID    = errors.New("invalid paper id")
	ErrInvalidStoredJSON = errors.New("invalid stored matcher json")
)

type Result struct {
	Candidates int
	Matched    int
	Inserted   int64
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) (*Service, error) {
	if repository == nil || now == nil {
		return nil, errors.New("invalid matcher service configuration")
	}
	return &Service{repository: repository, now: now}, nil
}

func (service *Service) Match(ctx context.Context, paperID uint64) (Result, error) {
	if paperID == 0 {
		return Result{}, ErrInvalidPaperID
	}
	stored, err := service.repository.FindPaper(ctx, paperID)
	if err != nil {
		return Result{}, err
	}
	categories, err := decodeStoredStrings(stored.CategoriesJSON, "paper categories", false)
	if err != nil {
		return Result{}, err
	}
	categories = deduplicateCategories(categories)
	if len(categories) == 0 {
		return Result{}, fmt.Errorf("%w: paper categories must not be empty", ErrInvalidStoredJSON)
	}

	candidates, err := service.repository.ListCandidates(
		ctx, stored.SourceID, categories, stored.FirstSeenAt,
	)
	if err != nil {
		return Result{}, err
	}
	result := Result{Candidates: len(candidates)}
	matchedAt := service.now().UTC()
	matches := make([]paper.SubscriptionPaper, 0, len(candidates))
	for _, candidate := range candidates {
		// Keep the service boundary safe even when a different Repository
		// implementation does not apply the SQL from-now filter.
		if candidate.CreatedAt.After(stored.FirstSeenAt) ||
			!containsCategory(categories, candidate.Category) {
			continue
		}
		matchedKeywords, matched, err := Evaluate(stored, candidate)
		if err != nil {
			return Result{}, fmt.Errorf("subscription %d: %w", candidate.ID, err)
		}
		if !matched {
			continue
		}
		encoded, err := json.Marshal(matchedKeywords)
		if err != nil {
			return Result{}, fmt.Errorf("encode matched keywords: %w", err)
		}
		matches = append(matches, paper.SubscriptionPaper{
			SubscriptionID:      candidate.ID,
			PaperID:             paperID,
			MatchedKeywordsJSON: encoded,
			MatchedAt:           matchedAt,
		})
	}
	result.Matched = len(matches)
	inserted, err := service.repository.InsertMatches(ctx, matches)
	if err != nil {
		return Result{}, err
	}
	result.Inserted = inserted
	return result, nil
}

// Evaluate applies the shared deterministic category and keyword rules without
// applying the normal stream's from-now boundary.
func Evaluate(stored paper.Paper, candidate Candidate) ([]string, bool, error) {
	categories, err := decodeStoredStrings(stored.CategoriesJSON, "paper categories", false)
	if err != nil {
		return nil, false, err
	}
	if !containsCategory(deduplicateCategories(categories), candidate.Category) {
		return nil, false, nil
	}
	keywords, err := decodeStoredStrings(candidate.KeywordsJSON, "subscription keywords", true)
	if err != nil {
		return nil, false, err
	}
	matchedKeywords, matched := rules.Match(rules.Paper{Title: stored.Title, Abstract: stored.Abstract, Categories: categories}, rules.Rule{Category: candidate.Category, Keywords: keywords})
	return matchedKeywords, matched, nil
}

func decodeStoredStrings(raw []byte, field string, allowEmpty bool) ([]string, error) {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, fmt.Errorf("%w: %s must be an array", ErrInvalidStoredJSON, field)
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		if value == "" {
			return nil, fmt.Errorf("%w: %s contains a blank value", ErrInvalidStoredJSON, field)
		}
		normalized = append(normalized, value)
	}
	if !allowEmpty && len(normalized) == 0 {
		return nil, fmt.Errorf("%w: %s must not be empty", ErrInvalidStoredJSON, field)
	}
	return normalized, nil
}

func deduplicateCategories(categories []string) []string {
	seen := make(map[string]struct{}, len(categories))
	result := make([]string, 0, len(categories))
	for _, category := range categories {
		if _, exists := seen[category]; exists {
			continue
		}
		seen[category] = struct{}{}
		result = append(result, category)
	}
	return result
}

func containsCategory(categories []string, wanted string) bool {
	for _, category := range categories {
		if category == wanted {
			return true
		}
	}
	return false
}

func normalizeSearchText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func matchKeywords(searchText string, keywords []string) []string {
	matched := make([]string, 0, len(keywords))
	seen := make(map[string]struct{}, len(keywords))
	for _, keyword := range keywords {
		normalized := strings.ToLower(keyword)
		if _, exists := seen[normalized]; exists || !strings.Contains(searchText, normalized) {
			continue
		}
		seen[normalized] = struct{}{}
		matched = append(matched, keyword)
	}
	return matched
}
