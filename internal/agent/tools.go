package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"signalwatch/internal/document"
	"signalwatch/internal/subscription"
	"strings"
	"time"
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
func (s *Service) prepareDocument(ctx context.Context, r Run, c Conversation, cp *Checkpoint) (document.Document, error) {
	p, err := s.Papers.Get(ctx, r.UserID, *c.PaperID)
	if err != nil {
		return document.Document{}, err
	}
	h := sha256.Sum256([]byte(p.Title + "\n" + p.Abstract))
	hash := hex.EncodeToString(h[:])
	abstract := Citation{ID: "abstract", DocumentID: "abstract:" + hash, ContentHash: hash, Page: 0, Quote: p.Title + "\n" + p.Abstract, URL: p.ArXivURL}
	if len(cp.Evidence) > 0 && cp.Evidence[0].ContentHash != hash {
		return document.Document{}, ErrConflict
	}
	cp.Evidence = mergeEvidence(cp.Evidence, []Citation{abstract})
	if r.ContextMode == "abstract" {
		return document.Document{}, nil
	}
	src := document.Source{PaperID: p.ID, ArXivID: p.ArXivID, PDFURL: p.PDFURL, UpdatedAt: p.ArXivUpdatedAt}
	d, err := s.Documents.Ensure(ctx, src)
	if err != nil {
		return d, err
	}
	if cp.DocumentID != "" && cp.DocumentID != d.ID {
		return d, ErrConflict
	}
	cp.DocumentID = d.ID
	if err := s.Store.Save(ctx, r, *cp, "preparing_document", nil); err != nil {
		return d, err
	}
	for d.State != "ready" {
		if d.State == "failed" {
			return d, document.ErrUnavailable
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return d, ctx.Err()
		case <-timer.C:
		}
		d, err = s.Documents.Get(ctx, d.ID)
		if err != nil {
			return d, err
		}
	}
	return d, nil
}
func (s *Service) tool(ctx context.Context, r Run, c Conversation, d document.Document, a Action) (any, []Citation, string, error) {
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
	if r.ContextMode == "abstract" || d.State != "ready" {
		return nil, nil, "", ErrInput
	}
	chunks, err := s.Documents.Chunks(ctx, d.ID)
	if err != nil {
		return nil, nil, "", err
	}
	selected := []document.Chunk{}
	switch a.Tool {
	case "get_document_outline":
		var args struct{}
		if strict(a.Arguments, &args) != nil {
			return nil, nil, "", ErrInput
		}
		// Cover beginning, middle and ending even for long papers.
		stride := max(1, (len(chunks)+23)/24)
		for i := 0; i < len(chunks); i += stride {
			chunk := chunks[i]
			words := strings.Fields(chunk.Text)
			chunk.Text = strings.Join(words[:min(80, len(words))], " ")
			selected = append(selected, chunk)
		}
		if len(chunks) > 0 && selected[len(selected)-1].Number != chunks[len(chunks)-1].Number {
			chunk := chunks[len(chunks)-1]
			words := strings.Fields(chunk.Text)
			chunk.Text = strings.Join(words[:min(80, len(words))], " ")
			selected = append(selected, chunk)
		}
	case "search_document":
		var args struct {
			Query string `json:"query"`
		}
		if strict(a.Arguments, &args) != nil || len(args.Query) == 0 || len(args.Query) > 1000 {
			return nil, nil, "", ErrInput
		}
		selected = document.Search(chunks, args.Query, 4)
	case "read_document_chunks":
		var args struct {
			Numbers []int `json:"numbers"`
		}
		if strict(a.Arguments, &args) != nil || len(args.Numbers) < 1 || len(args.Numbers) > 6 {
			return nil, nil, "", ErrInput
		}
		seen := map[int]bool{}
		for _, n := range args.Numbers {
			if n < 1 || n > len(chunks) || seen[n] {
				return nil, nil, "", ErrInput
			}
			selected = append(selected, chunks[n-1])
			seen[n] = true
		}
	default:
		return nil, nil, "", ErrInput
	}
	p, err := s.Papers.Get(ctx, r.UserID, *c.PaperID)
	if err != nil {
		return nil, nil, "", err
	}
	if document.Identity(document.Source{PaperID: p.ID, ArXivID: p.ArXivID, PDFURL: p.PDFURL, UpdatedAt: p.ArXivUpdatedAt}) != d.ID {
		return nil, nil, "", ErrConflict
	}
	evidence := []Citation{}
	for _, chunk := range selected {
		evidence = append(evidence, Citation{ID: fmt.Sprintf("p%d-c%d", chunk.Page, chunk.Number), DocumentID: d.ID, ContentHash: d.ContentHash, Page: chunk.Page, Quote: chunk.Text, URL: "https://arxiv.org/pdf/" + d.SourceVersion + fmt.Sprintf("#page=%d", chunk.Page)})
	}
	return map[string]any{"document_id": d.ID, "content_hash": d.ContentHash, "total_chunks": len(chunks), "evidence": evidence}, evidence, "", nil
}
