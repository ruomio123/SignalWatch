package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// Store exact serialized requests, rather than rebuilding them on recovery.
type paperSerializedInput string

func paperReviewStage(index int) string {
	if index == 0 {
		return "validating_paper"
	}
	return fmt.Sprintf("validating_paper_%d", index+1)
}

func paperReviewLimit(task string) int {
	if task == TaskPaperReproduction {
		return 4
	}
	if task == TaskPaperReport {
		return 6
	}
	return 2
}

func planPaperReview(pc *PaperCheckpoint, task string, claims []reviewClaim, sources map[string]Citation) ([]PaperReviewBatch, error) {
	serialize := func(batch []reviewClaim) (PaperReviewBatch, error) {
		ids := make([]string, 0, len(batch))
		evidence := []Citation{}
		seen := map[string]bool{}
		for _, claim := range batch {
			ids = append(ids, claim.ID)
			for _, ref := range claim.Evidence {
				source, ok := sources[ref.ID]
				if !ok {
					return PaperReviewBatch{}, paperError("invalid_checkpoint")
				}
				if !seen[ref.ID] {
					seen[ref.ID] = true
					evidence = append(evidence, source)
				}
			}
		}
		raw, err := boundedPaperInput(map[string]any{"paper": pc.Context, "context_mode": pc.Mode, "claims": batch, "source_passages": evidencePassages(evidence)})
		return PaperReviewBatch{ClaimIDs: ids, Request: string(raw)}, err
	}
	plan := []PaperReviewBatch{}
	current := []reviewClaim{}
	for _, claim := range claims {
		candidate := append(append([]reviewClaim{}, current...), claim)
		if _, err := serialize(candidate); err != nil {
			if len(current) == 0 {
				return nil, err
			}
			batch, err := serialize(current)
			if err != nil {
				return nil, err
			}
			plan = append(plan, batch)
			current = []reviewClaim{claim}
			if _, err := serialize(current); err != nil {
				return nil, err
			}
		} else {
			current = candidate
		}
	}
	// An empty analysis still gets one structural review, preserving normal
	// report/followup call counts and a single publication boundary.
	last, err := serialize(current)
	if err != nil {
		return nil, err
	}
	plan = append(plan, last)
	if len(plan) > paperReviewLimit(task) {
		return nil, ErrBudget
	}
	return plan, nil
}

// Validate all checkpoint batches before starting any review. The original
// requests remain authoritative; they must still cover each current claim once.
func validatePaperReviewPlan(plan []PaperReviewBatch, task string, claims []reviewClaim) error {
	if len(plan) == 0 || len(plan) > paperReviewLimit(task) {
		return paperError("invalid_checkpoint")
	}
	position := 0
	for _, batch := range plan {
		var input struct {
			Claims []reviewClaim `json:"claims"`
		}
		if len(batch.Request) > paperInputLimit || json.Unmarshal([]byte(batch.Request), &input) != nil || len(input.Claims) != len(batch.ClaimIDs) {
			return paperError("invalid_checkpoint")
		}
		for i, id := range batch.ClaimIDs {
			if position >= len(claims) || id != claims[position].ID {
				return paperError("invalid_checkpoint")
			}
			got, _ := json.Marshal(input.Claims[i])
			want, _ := json.Marshal(claims[position])
			if string(got) != string(want) {
				return paperError("invalid_checkpoint")
			}
			position++
		}
	}
	if position != len(claims) {
		return paperError("invalid_checkpoint")
	}
	return nil
}

func (s *Service) reviewPaper(ctx context.Context, r Run, cp *Checkpoint, check func(context.Context) error, claims []reviewClaim, sources map[string]Citation) (map[string]bool, error) {
	if cp.Paper.ReviewPlan == nil {
		plan, err := planPaperReview(cp.Paper, r.Task, claims, sources)
		if err != nil {
			return nil, s.failPaperStage(ctx, r, cp, "validating_paper", err, nil)
		}
		cp.Paper.ReviewPlan = plan
		cp.Paper.CurrentStage = "validating_paper"
		if err := s.Store.Save(ctx, r, *cp, "validating_paper", nil); err != nil {
			return nil, err
		}
	}
	if err := validatePaperReviewPlan(cp.Paper.ReviewPlan, r.Task, claims); err != nil {
		return nil, s.failPaperStage(ctx, r, cp, "validating_paper", err, nil)
	}
	verdicts := map[string]bool{}
	position := 0
	for i, batch := range cp.Paper.ReviewPlan {
		batchClaims := claims[position : position+len(batch.ClaimIDs)]
		position += len(batch.ClaimIDs)
		raw, err := s.paperCall(ctx, r, cp, check, paperReviewStage(i), verdictPrompt, paperSerializedInput(batch.Request), func(raw []byte) error { _, err := decodeVerdicts(raw, batchClaims); return err })
		if err != nil {
			return nil, err
		}
		result, err := decodeVerdicts(raw, batchClaims)
		if err != nil {
			return nil, err
		}
		for id, supported := range result {
			verdicts[id] = supported
		}
	}
	return verdicts, nil
}
