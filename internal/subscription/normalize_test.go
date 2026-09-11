package subscription

import (
	"errors"
	"fmt"
	"signalwatch/internal/source"
	"strings"
	"testing"
)

func arXivCatalog() source.PublicSource {
	return source.PublicSource{ID: 1, Kind: source.KindArXiv, AllowedCategories: []string{"cs.AI", "cs.CL"}, RuleTypes: []string{"category", "include_keyword"}}
}
func TestNormalizeV2Rules(t *testing.T) {
	r, err := NormalizeRules(RulesInput{Category: " cs.ai ", Keywords: []string{"  Tool   Use ", "代理"}}, arXivCatalog())
	if err != nil || r.Category != "cs.AI" || r.Keywords[0] != "Tool Use" {
		t.Fatalf("%+v %v", r, err)
	}
	for _, tc := range []struct {
		input RulesInput
		err   error
	}{
		{RulesInput{}, ErrCategoryRequired}, {RulesInput{Category: "invalid"}, ErrCategoryNotAllowed}, {RulesInput{Category: "cs.AI", Keywords: []string{"Agent", "agent"}}, ErrDuplicateRule}, {RulesInput{Category: "cs.AI", Keywords: []string{" "}}, ErrInvalidRule}, {RulesInput{Category: "cs.AI", Keywords: []string{strings.Repeat("界", 101)}}, ErrInvalidRule}, {RulesInput{Category: "cs.AI", Keywords: make([]string, 31)}, ErrRuleLimit},
	} {
		if _, err := NormalizeRules(tc.input, arXivCatalog()); !errors.Is(err, tc.err) {
			t.Fatalf("%+v: %v", tc.input, err)
		}
	}
}

func TestRulesRespectCapabilitiesAndUnicodeLimits(t *testing.T) {
	catalog := arXivCatalog()
	catalog.RuleTypes = []string{"category"}
	if _, err := NormalizeRules(RulesInput{Category: "cs.AI", Keywords: []string{"agent"}}, catalog); !errors.Is(err, ErrRuleNotSupported) {
		t.Fatal(err)
	}
	if _, err := NormalizeRules(RulesInput{Category: "cs.AI"}, catalog); err != nil {
		t.Fatal(err)
	}
	catalog = arXivCatalog()
	words := make([]string, 30)
	for i := range words {
		words[i] = fmt.Sprintf("keyword %d", i)
	}
	words[0] = strings.Repeat("界", 100)
	if _, err := NormalizeRules(RulesInput{Category: "cs.AI", Keywords: words}, catalog); err != nil {
		t.Fatal(err)
	}
	for _, words := range [][]string{{"École", "école"}, {string([]byte{0xff})}} {
		if _, err := NormalizeRules(RulesInput{Category: "cs.AI", Keywords: words}, catalog); err == nil {
			t.Fatal("accepted duplicate or invalid Unicode")
		}
	}
}
