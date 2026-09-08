package subscription

import (
	"errors"
	"strings"
	"testing"

	"signalwatch/internal/source"
)

func TestNormalizeRulesNormalizesFlatCategoryAndKeywords(t *testing.T) {
	rules, err := NormalizeRules(RulesInput{
		Categories:      []string{" cs.ai "},
		IncludeKeywords: []string{" Tool\u2003Use ", "Agent"},
	}, arXivCatalog())
	if err != nil {
		t.Fatalf("normalize rules: %v", err)
	}

	assertNormalizedGroup(t, rules.Categories, []NormalizedRule{
		{RuleValue: "cs.AI", NormalizedValue: "cs.AI"},
	})
	assertNormalizedGroup(t, rules.IncludeKeywords, []NormalizedRule{
		{RuleValue: "Tool Use", NormalizedValue: "tool use"},
		{RuleValue: "Agent", NormalizedValue: "agent"},
	})
	if len(rules.Authors) != 0 || len(rules.ExcludeKeywords) != 0 {
		t.Fatalf("unsupported groups must remain empty: %+v", rules)
	}
}

func TestNormalizeRulesRejectsDuplicatesAfterNormalization(t *testing.T) {
	for _, test := range []struct {
		name  string
		rules RulesInput
	}{
		{"include keyword", RulesInput{Categories: []string{"cs.AI"}, IncludeKeywords: []string{"Agent", " agent "}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeRules(test.rules, arXivCatalog())
			if !errors.Is(err, ErrDuplicateRule) {
				t.Fatalf("expected duplicate error, got %v", err)
			}
		})
	}
}

func TestNormalizeRulesValidatesCategoryCapability(t *testing.T) {
	if _, err := NormalizeRules(RulesInput{}, arXivCatalog()); !errors.Is(err, ErrCategoryRequired) {
		t.Fatalf("expected required category error, got %v", err)
	}
	if _, err := NormalizeRules(
		RulesInput{Categories: []string{"math.AG"}},
		arXivCatalog(),
	); !errors.Is(err, ErrCategoryNotAllowed) {
		t.Fatalf("expected category capability error, got %v", err)
	}
}

func TestNormalizeRulesValidatesEveryGroupLimit(t *testing.T) {
	for _, test := range []struct {
		name  string
		rules RulesInput
	}{
		{"categories", RulesInput{Categories: repeatedValues("cs.AI", maxCategories+1)}},
		{"authors", RulesInput{Categories: []string{"cs.AI"}, Authors: distinctValues("author", maxAuthors+1)}},
		{"include", RulesInput{Categories: []string{"cs.AI"}, IncludeKeywords: distinctValues("include", maxIncludeKeywords+1)}},
		{"exclude", RulesInput{Categories: []string{"cs.AI"}, ExcludeKeywords: distinctValues("exclude", maxExcludeKeywords+1)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeRules(test.rules, arXivCatalog()); !errors.Is(err, ErrRuleLimit) {
				t.Fatalf("expected rule limit error, got %v", err)
			}
		})
	}
}

func TestNormalizeRulesAcceptsFlatFieldsAtTheirCountBoundary(t *testing.T) {
	catalog := arXivCatalog()
	catalog.AllowedCategories = distinctValues("category", maxCategories)
	rules, err := NormalizeRules(RulesInput{
		Categories:      append([]string(nil), catalog.AllowedCategories...),
		IncludeKeywords: distinctValues("include", maxIncludeKeywords),
	}, catalog)
	if err != nil {
		t.Fatalf("expected exact group limits to be accepted: %v", err)
	}
	if len(rules.Categories) != maxCategories ||
		len(rules.IncludeKeywords) != maxIncludeKeywords {
		t.Fatalf("unexpected normalized group sizes %+v", rules)
	}
}

func TestNormalizeTextRuleValidatesUnicodeLengthAndWhitespace(t *testing.T) {
	if _, err := NormalizeTextRule(" \t\n "); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("expected whitespace-only rule rejection, got %v", err)
	}
	if _, err := NormalizeTextRule(strings.Repeat("界", maxRuleRunes)); err != nil {
		t.Fatalf("expected %d Unicode characters to be valid: %v", maxRuleRunes, err)
	}
	if _, err := NormalizeTextRule(strings.Repeat("界", maxRuleRunes+1)); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("expected too-long rule rejection, got %v", err)
	}
	if _, err := NormalizeTextRule(string([]byte{0xff})); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("expected invalid UTF-8 rejection, got %v", err)
	}
}

func TestNormalizeRulesRejectsRuleOutsideAdvertisedCapabilities(t *testing.T) {
	catalog := arXivCatalog()
	catalog.RuleTypes = []string{source.RuleTypeCategory}
	_, err := NormalizeRules(RulesInput{
		Categories: []string{"cs.AI"}, Authors: []string{"Jane Doe"},
	}, catalog)
	if !errors.Is(err, ErrRuleNotSupported) {
		t.Fatalf("expected unsupported rule error, got %v", err)
	}
}

func arXivCatalog() source.PublicSource {
	return source.PublicSource{
		ID: 1, SourceKey: "arxiv", Kind: source.KindArXiv, Name: "arXiv",
		RuleTypes: []string{
			source.RuleTypeCategory,
			source.RuleTypeIncludeKeyword,
		},
		AllowedCategories: []string{"cs.AI", "cs.CL"},
	}
}

func assertNormalizedGroup(t *testing.T, got, want []NormalizedRule) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %d rules, got %+v", len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("rule %d: expected %+v, got %+v", index, want[index], got[index])
		}
	}
}

func repeatedValues(value string, count int) []string {
	result := make([]string, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func distinctValues(prefix string, count int) []string {
	result := make([]string, count)
	for index := range result {
		result[index] = prefix + strings.Repeat("x", index)
	}
	return result
}
