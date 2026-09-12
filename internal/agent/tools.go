package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"signalwatch/internal/subscription"
)

func strict(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return ErrInput
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return ErrInput
	}
	return nil
}
func (s *Service) tool(ctx context.Context, r Run, c Conversation, a Action) (any, []Citation, string, error) {
	if c.Kind == "subscription" {
		switch a.Tool {
		case "list_subscription_options":
			var args struct{}
			if strict(a.Arguments, &args) != nil {
				return nil, nil, "", ErrInput
			}
			values, err := s.Sources.List(ctx)
			return map[string]any{"sources": values, "keyword_semantics": "OR", "categories_per_subscription": 1}, nil, "", err
		case "preview_subscription":
			var args struct {
				SourceID uint64                  `json:"source_id"`
				Rules    subscription.RulesInput `json:"rules"`
			}
			if strict(a.Arguments, &args) != nil {
				return nil, nil, "", ErrInput
			}
			src, err := s.Sources.Get(ctx, args.SourceID)
			if err != nil {
				return nil, nil, "", err
			}
			rule, err := subscription.NormalizeRules(args.Rules, src)
			if err != nil {
				return nil, nil, "", err
			}
			preview, err := s.Store.Preview(ctx, args.SourceID, rule)
			return preview, nil, "", err
		case "propose_subscription":
			var input subscription.CreateInput
			if strict(a.Arguments, &input) != nil {
				return nil, nil, "", ErrInput
			}
			if input.MaxItemsPerDigest == nil {
				limit, err := s.Store.DigestLimit(ctx, r.UserID)
				if err != nil {
					return nil, nil, "", err
				}
				input.MaxItemsPerDigest = &limit
			}
			if input.DigestAILanguage == nil {
				lang := "zh"
				input.DigestAILanguage = &lang
			}
			if input.DigestAIEnabled == nil {
				enabled := false
				input.DigestAIEnabled = &enabled
			}
			normalized, err := s.Subscriptions.ValidateDraft(ctx, r.UserID, input)
			if err != nil {
				return nil, nil, "", err
			}
			draft, err := s.Store.SaveDraft(ctx, r, normalized)
			if err != nil {
				return nil, nil, "", err
			}
			return draft, nil, draft.ID, nil
		}
		return nil, nil, "", ErrInput
	}
	return nil, nil, "", ErrInput
}
