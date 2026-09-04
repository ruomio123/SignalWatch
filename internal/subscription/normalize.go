package subscription

import (
	"errors"
	"strings"
	"unicode/utf8"

	"signalwatch/internal/source"
)

const (
	maxCategories      = 10
	maxAuthors         = 20
	maxIncludeKeywords = 30
	maxExcludeKeywords = 30
	maxRuleRunes       = 100
)

var (
	ErrInvalidRule        = errors.New("invalid subscription rule")
	ErrDuplicateRule      = errors.New("duplicate subscription rule")
	ErrRuleLimit          = errors.New("subscription rule limit exceeded")
	ErrCategoryRequired   = errors.New("at least one category is required")
	ErrCategoryNotAllowed = errors.New("category is not allowed")
	ErrRuleNotSupported   = errors.New("rule type is not supported")
)

type NormalizedRule struct {
	RuleValue       string
	NormalizedValue string
}

type NormalizedRules struct {
	Categories      []NormalizedRule
	Authors         []NormalizedRule
	IncludeKeywords []NormalizedRule
	ExcludeKeywords []NormalizedRule
}

// NormalizeCategory maps a trimmed, case-insensitive category to the exact
// spelling advertised by the selected source.
func NormalizeCategory(value string, allowed []string) (NormalizedRule, error) {
	trimmed := strings.TrimSpace(value)
	if !validRuleValue(trimmed) {
		return NormalizedRule{}, ErrInvalidRule
	}
	for _, official := range allowed {
		if strings.EqualFold(trimmed, official) {
			return NormalizedRule{RuleValue: official, NormalizedValue: official}, nil
		}
	}
	return NormalizedRule{}, ErrCategoryNotAllowed
}

// NormalizeTextRule is database-independent so the matcher can reuse the
// exact display and comparison representation.
func NormalizeTextRule(value string) (NormalizedRule, error) {
	if !utf8.ValidString(value) {
		return NormalizedRule{}, ErrInvalidRule
	}
	display := strings.Join(strings.Fields(value), " ")
	if !validRuleValue(display) {
		return NormalizedRule{}, ErrInvalidRule
	}
	return NormalizedRule{
		RuleValue:       display,
		NormalizedValue: foldASCIICase(display),
	}, nil
}

func NormalizeRules(input RulesInput, catalog source.PublicSource) (NormalizedRules, error) {
	if len(input.Categories) > maxCategories ||
		len(input.Authors) > maxAuthors ||
		len(input.IncludeKeywords) > maxIncludeKeywords ||
		len(input.ExcludeKeywords) > maxExcludeKeywords {
		return NormalizedRules{}, ErrRuleLimit
	}
	if catalog.Kind == source.KindArXiv && len(input.Categories) == 0 {
		return NormalizedRules{}, ErrCategoryRequired
	}

	groups := []struct {
		ruleType string
		values   []string
		target   *[]NormalizedRule
	}{
		{source.RuleTypeCategory, input.Categories, nil},
		{source.RuleTypeAuthor, input.Authors, nil},
		{source.RuleTypeIncludeKeyword, input.IncludeKeywords, nil},
		{source.RuleTypeExcludeKeyword, input.ExcludeKeywords, nil},
	}

	var result NormalizedRules
	groups[0].target = &result.Categories
	groups[1].target = &result.Authors
	groups[2].target = &result.IncludeKeywords
	groups[3].target = &result.ExcludeKeywords

	for _, group := range groups {
		if len(group.values) == 0 {
			*group.target = []NormalizedRule{}
			continue
		}
		if !supportsRule(catalog.RuleTypes, group.ruleType) {
			return NormalizedRules{}, ErrRuleNotSupported
		}

		normalized := make([]NormalizedRule, 0, len(group.values))
		seen := make(map[string]struct{}, len(group.values))
		for _, value := range group.values {
			var rule NormalizedRule
			var err error
			if group.ruleType == source.RuleTypeCategory {
				rule, err = NormalizeCategory(value, catalog.AllowedCategories)
			} else {
				rule, err = NormalizeTextRule(value)
			}
			if err != nil {
				return NormalizedRules{}, err
			}
			if _, exists := seen[rule.NormalizedValue]; exists {
				return NormalizedRules{}, ErrDuplicateRule
			}
			seen[rule.NormalizedValue] = struct{}{}
			normalized = append(normalized, rule)
		}
		*group.target = normalized
	}
	return result, nil
}

func validRuleValue(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRuleRunes
}

func foldASCIICase(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
}

func supportsRule(ruleTypes []string, wanted string) bool {
	for _, ruleType := range ruleTypes {
		if ruleType == wanted {
			return true
		}
	}
	return false
}
