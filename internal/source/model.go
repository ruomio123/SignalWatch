package source

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	KindArXiv = "arxiv"

	RuleTypeCategory       = "category"
	RuleTypeAuthor         = "author"
	RuleTypeIncludeKeyword = "include_keyword"
	RuleTypeExcludeKeyword = "exclude_keyword"
)

// Source is the persistence model. Endpoint and ConfigJSON must never be
// serialized directly in an HTTP response.
type Source struct {
	ID         uint64          `gorm:"column:id;primaryKey;autoIncrement"`
	SourceKey  string          `gorm:"column:source_key"`
	Kind       string          `gorm:"column:kind"`
	Name       string          `gorm:"column:name"`
	Endpoint   *string         `gorm:"column:endpoint"`
	Enabled    bool            `gorm:"column:enabled"`
	ConfigJSON json.RawMessage `gorm:"column:config_json"`
	CreatedAt  time.Time       `gorm:"column:created_at"`
	UpdatedAt  time.Time       `gorm:"column:updated_at"`
}

func (Source) TableName() string { return "sources" }

type sourceConfig struct {
	AllowedCategories []string `json:"allowed_categories"`
	RuleTypes         []string `json:"rule_types"`
}

// PublicSource is the allowlisted source representation shared by the catalog
// response and subscription responses.
type PublicSource struct {
	ID                uint64   `json:"id"`
	SourceKey         string   `json:"source_key"`
	Kind              string   `json:"kind"`
	Name              string   `json:"name"`
	RuleTypes         []string `json:"rule_types"`
	AllowedCategories []string `json:"allowed_categories"`
}

func (source Source) Public() (PublicSource, error) {
	var config sourceConfig
	if err := json.Unmarshal(source.ConfigJSON, &config); err != nil {
		return PublicSource{}, fmt.Errorf("decode source config: %w", err)
	}

	ruleTypes := append([]string{}, config.RuleTypes...)
	if len(ruleTypes) == 0 && source.Kind == KindArXiv {
		ruleTypes = []string{
			RuleTypeCategory,
			RuleTypeAuthor,
			RuleTypeIncludeKeyword,
			RuleTypeExcludeKeyword,
		}
	}

	return PublicSource{
		ID:                source.ID,
		SourceKey:         source.SourceKey,
		Kind:              source.Kind,
		Name:              source.Name,
		RuleTypes:         ruleTypes,
		AllowedCategories: append([]string{}, config.AllowedCategories...),
	}, nil
}
