package agent

import (
	"context"
	"encoding/json"
	"errors"
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
		raw, err := paperFitsInput(pc, paperReviewStage(0), verdictPrompt, map[string]any{"paper": pc.Context, "context_mode": pc.Mode, "claims": batch, "source_passages": evidencePassages(evidence)})
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
	// Keep the pure planner's empty-input shape stable. The live workflow skips
	// model calls when there are no factual claims to review.
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

func paperReviewPackingFailure(err error) bool {
	return errors.Is(err, ErrBudget) || paperFailureCode(err) == "context_too_large"
}

// Retain complete claims and every source they cite. A claim that cannot fit
// must not prevent later, independently reviewable facts from being checked.
// The ordinary planner still rejects over-budget plans for callers that need
// all-or-nothing feasibility checks, including optional supplement admission.
func planPaperReviewAvailable(pc *PaperCheckpoint, task string, claims []reviewClaim, sources map[string]Citation) ([]PaperReviewBatch, []string, string, error) {
	plan, err := planPaperReview(pc, task, claims, sources)
	if err == nil || !paperReviewPackingFailure(err) {
		return plan, nil, "", err
	}
	code := paperFailureCode(err)
	retained := []reviewClaim{}
	omitted := []string{}
	plan = []PaperReviewBatch{}
	for _, claim := range claims {
		candidate := append(append([]reviewClaim(nil), retained...), claim)
		candidatePlan, candidateErr := planPaperReview(pc, task, candidate, sources)
		if candidateErr != nil {
			if !paperReviewPackingFailure(candidateErr) {
				return nil, nil, "", candidateErr
			}
			omitted = append(omitted, claim.ID)
			continue
		}
		retained, plan = candidate, candidatePlan
	}
	return plan, omitted, code, nil
}

// Validate the omission record before using it to select claims. An omitted
// ID never implies rejection or support; its verdict remains absent.
func retainedPaperReviewClaims(pc *PaperCheckpoint, task string, claims []reviewClaim) ([]reviewClaim, error) {
	current := map[string]bool{}
	for _, claim := range claims {
		if claim.ID == "" || current[claim.ID] {
			return nil, paperError("invalid_checkpoint")
		}
		current[claim.ID] = true
	}
	omitted := map[string]bool{}
	for _, id := range pc.ReviewOmitted {
		if !current[id] || omitted[id] {
			return nil, paperError("invalid_checkpoint")
		}
		omitted[id] = true
	}
	if len(omitted) > 0 {
		hasIssue := false
		for _, issue := range pc.Issues {
			hasIssue = hasIssue || issue.Stage == "validating_paper" && (issue.Code == "context_too_large" || issue.Code == "budget_exhausted")
		}
		if !hasIssue {
			return nil, paperError("invalid_checkpoint")
		}
	}
	retained := make([]reviewClaim, 0, len(claims)-len(omitted))
	for _, claim := range claims {
		if !omitted[claim.ID] {
			retained = append(retained, claim)
		}
	}
	if len(retained) == 0 {
		if len(pc.ReviewPlan) != 0 {
			return nil, paperError("invalid_checkpoint")
		}
		return retained, nil
	}
	if err := validatePaperReviewPlan(pc.ReviewPlan, task, retained); err != nil {
		return nil, err
	}
	return retained, nil
}

func (s *Service) reviewPaper(ctx context.Context, r Run, cp *Checkpoint, check func(context.Context) error, claims []reviewClaim, sources map[string]Citation) (map[string]bool, error) {
	if len(claims) == 0 && cp.Paper.ReviewPlan == nil && len(cp.Paper.ReviewOmitted) == 0 {
		return map[string]bool{}, nil
	}
	if cp.Paper.ReviewPlan == nil && len(cp.Paper.ReviewOmitted) == 0 {
		plan, omitted, code, err := planPaperReviewAvailable(cp.Paper, r.Task, claims, sources)
		if err != nil {
			return nil, s.failPaperStage(ctx, r, cp, "validating_paper", err, nil)
		}
		cp.Paper.ReviewPlan, cp.Paper.ReviewOmitted = plan, omitted
		if len(omitted) > 0 {
			cp.Paper.Issues = append(cp.Paper.Issues, PaperIssue{Stage: "validating_paper", Code: code})
		}
		cp.Paper.CurrentStage = "validating_paper"
		if err := s.Store.Save(ctx, r, *cp, "validating_paper", nil); err != nil {
			return nil, err
		}
	}
	retained, err := retainedPaperReviewClaims(cp.Paper, r.Task, claims)
	if err != nil {
		return nil, s.failPaperStage(ctx, r, cp, "validating_paper", err, nil)
	}
	verdicts := map[string]bool{}
	position := 0
	for i, batch := range cp.Paper.ReviewPlan {
		batchClaims := retained[position : position+len(batch.ClaimIDs)]
		position += len(batch.ClaimIDs)
		raw, err := s.paperCall(ctx, r, cp, check, paperReviewStage(i), verdictPrompt, paperSerializedInput(batch.Request), func(raw []byte) error { _, err := decodeVerdicts(raw, batchClaims); return err })
		if err != nil {
			if paperStageIncomplete(err) {
				continue
			}
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
