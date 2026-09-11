package subscription

import (
	"errors"
	"signalwatch/internal/source"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxKeywords  = 30
	maxRuleRunes = 100
)

var (
	ErrInvalidRule        = errors.New("invalid subscription rule")
	ErrDuplicateRule      = errors.New("duplicate subscription rule")
	ErrRuleLimit          = errors.New("subscription rule limit exceeded")
	ErrCategoryRequired   = errors.New("one category is required")
	ErrCategoryNotAllowed = errors.New("category is not allowed")
	ErrRuleNotSupported   = errors.New("rule type is not supported")
)

func NormalizeRules(input RulesInput, catalog source.PublicSource) (RulesInput, error) {
	if strings.TrimSpace(input.Category) == "" {
		return RulesInput{}, ErrCategoryRequired
	}
	if !slices.Contains(catalog.RuleTypes, "category") || len(input.Keywords) > 0 && !slices.Contains(catalog.RuleTypes, "include_keyword") {
		return RulesInput{}, ErrRuleNotSupported
	}
	if len(input.Keywords) > maxKeywords {
		return RulesInput{}, ErrRuleLimit
	}
	if !validRuleValue(strings.TrimSpace(input.Category)) {
		return RulesInput{}, ErrInvalidRule
	}
	result := RulesInput{Keywords: []string{}}
	for _, official := range catalog.AllowedCategories {
		if strings.EqualFold(strings.TrimSpace(input.Category), official) {
			result.Category = official
			break
		}
	}
	if result.Category == "" {
		return RulesInput{}, ErrCategoryNotAllowed
	}
	seen := map[string]bool{}
	for _, value := range input.Keywords {
		if !utf8.ValidString(value) {
			return RulesInput{}, ErrInvalidRule
		}
		display := strings.Join(strings.Fields(value), " ")
		if !validRuleValue(display) {
			return RulesInput{}, ErrInvalidRule
		}
		folded := strings.ToLower(display)
		if seen[folded] {
			return RulesInput{}, ErrDuplicateRule
		}
		seen[folded] = true
		result.Keywords = append(result.Keywords, display)
	}
	return result, nil
}
func validRuleValue(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRuleRunes
}
