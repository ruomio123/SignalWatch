package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const paperRecoveryLimit = 3

var errPaperRecoverySkipped = errors.New("paper recovery skipped before calling")

type paperStageFailure struct{ error }

func paperStageIncomplete(err error) bool {
	var failure *paperStageFailure
	return errors.As(err, &failure)
}
func (e *paperStageFailure) Unwrap() error { return e.error }

// A fence is an envelope only if it contains exactly one complete JSON object.
// Never extract braces from prose, repair syntax, or concatenate fragments.
func normalizePaperJSON(raw []byte) []byte {
	if len(raw) > paperResponseLimit {
		return raw
	}
	trimmed := bytes.TrimSpace(raw)
	if !bytes.HasPrefix(trimmed, []byte("```")) {
		return raw
	}
	first := bytes.IndexByte(trimmed, '\n')
	if first < 0 {
		return raw
	}
	opening := strings.ToLower(strings.TrimSpace(string(trimmed[:first])))
	if opening != "```" && opening != "```json" {
		return raw
	}
	closing := bytes.LastIndexByte(trimmed, '\n')
	if closing <= first || strings.TrimSpace(string(trimmed[closing+1:])) != "```" {
		return raw
	}
	body := bytes.TrimSpace(trimmed[first+1 : closing])
	if len(body) == 0 || body[0] != '{' || !json.Valid(body) {
		return raw
	}
	return append([]byte(nil), body...)
}

func paperRecoveryUsed(pc *PaperCheckpoint) int {
	used := 0
	for _, repair := range pc.Repairs {
		if repair != nil && repair.Attempted {
			used++
		}
	}
	if len(pc.Repairs) == 0 && pc.Repair != nil && pc.Repair.Attempted {
		used++
	}
	return used
}

func paperRecoveryKind(callErr, validationErr error) string {
	code := paperFailureCode(callErr)
	if code == "output_truncated" {
		return "truncated"
	}
	if validationErr == nil || code != paperFailureCode(validationErr) {
		return ""
	}
	if code == "output_limit_exceeded" && repairablePaperLimit(validationErr) {
		return "limit"
	}
	detail := paperFailureDetail(validationErr)
	if detail == nil {
		return ""
	}
	if code == "output_invalid_json" {
		return "format"
	}
	if code != "output_schema_mismatch" {
		return ""
	}
	switch detail.Rule {
	case "required_field", "expected_object", "expected_array", "expected_string", "expected_boolean", "invalid_enum", "unexpected_field", "duplicate_key":
		return "format"
	}
	return ""
}

func paperKnownOutputFailure(code string) bool {
	switch code {
	case "output_invalid_json", "output_schema_mismatch", "output_limit_exceeded", "output_truncated", "output_language_mismatch", "evidence_id_unknown", "citation_invalid", "review_incomplete":
		return true
	}
	return false
}

// Persist a known, settled stage failure without turning the whole run into a
// terminal failure. The original stage is never called again after a restart.
func (s *Service) incompletePaperStage(ctx context.Context, r Run, cp *Checkpoint, stage string, err error, step *Step) error {
	if permissionErr := ctx.Err(); permissionErr != nil {
		return s.failPaperStage(ctx, r, cp, stage, permissionErr, step)
	}
	code := paperFailureCode(err)
	original := stage
	if contract := paperContract(stage); contract.OriginalStage != "" {
		original = contract.OriginalStage
	}
	detail := paperFailureDetail(err)
	if detail == nil && cp.Paper.Failure != nil && cp.Paper.Failure.Code == code {
		detail = cp.Paper.Failure
	}
	if detail == nil {
		detail = &PaperFailure{Code: code}
	}
	if cp.Paper.StageFailures == nil {
		cp.Paper.StageFailures = map[string]*PaperFailure{}
	}
	if _, exists := cp.Paper.StageFailures[original]; !exists {
		cp.Paper.Issues = append(cp.Paper.Issues, PaperIssue{Stage: original, Code: code, Field: paperContract(original).Field})
	}
	cp.Paper.StageFailures[original] = detail
	cp.Paper.Failure, cp.Paper.CurrentStage, cp.Phase = detail, stage, "ready"
	if repair := cp.Paper.Repairs[original]; repair != nil {
		if repair.State == "calling" || repair.State == "pending" {
			repair.State = "failed"
		}
		cp.Paper.Repair = repair
	}
	if step == nil {
		cp.Sequence++
		step = &Step{RunID: r.ID, Sequence: cp.Sequence, Kind: "workflow", Tool: stage, FailureCode: code, CreatedAt: time.Now().UTC()}
	}
	if saveErr := s.Store.Save(ctx, r, *cp, stage, step); saveErr != nil {
		return saveErr
	}
	return &paperStageFailure{err}
}

// Recovery is optional: leave enough calls/time to review retained candidates.
func paperRecoveryBudget(ctx context.Context, r Run, cp *Checkpoint, stage string) bool {
	if paperRecoveryUsed(cp.Paper) >= paperRecoveryLimit {
		return false
	}
	remainingReview := 1
	contract := paperContract(stage)
	if contract.Kind == "review" {
		remainingReview = 0
	}
	if len(cp.Paper.ReviewPlan) > 0 {
		remainingReview = 0
		for i := range cp.Paper.ReviewPlan {
			name := paperReviewStage(i)
			if name != stage && cp.Paper.Outputs[name] == nil && cp.Paper.StageFailures[name] == nil {
				remainingReview++
			}
		}
	}
	remainingAnalysis := 0
	if r.Task == TaskPaperReport && contract.Kind != "review" {
		for _, field := range paperFields {
			name := "analyzing_" + field
			if name != stage && cp.Paper.Outputs[name] == nil && cp.Paper.StageFailures[name] == nil {
				remainingAnalysis++
			}
		}
	}
	if paperCallLimit(r.Task)-cp.Calls < 1+remainingAnalysis+remainingReview {
		return false
	}
	timeout := cp.Paper.CallTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline, ok := ctx.Deadline()
	if r.Deadline != nil && (!ok || r.Deadline.Before(deadline)) {
		deadline, ok = *r.Deadline, true
	}
	return !ok || time.Until(deadline) >= time.Duration(1+remainingAnalysis+remainingReview)*timeout+5*time.Second
}

func (s *Service) skipPaperRecovery(ctx context.Context, r Run, cp *Checkpoint, stage string) error {
	repair := cp.Paper.Repair
	if repair == nil || repair.Failure == nil {
		return s.failPaperStage(ctx, r, cp, stage, paperError("invalid_checkpoint"), nil)
	}
	repair.State = "budget_exceeded"
	cp.Paper.Failure = repair.Failure
	return s.incompletePaperStage(ctx, r, cp, stage, paperError(repair.Failure.Code), nil)
}

// Refusing to publish an empty partial result is a terminal decision too. Pin
// its actual failing stage instead of attributing it to a later successful call.
func (s *Service) checkPaperPublication(ctx context.Context, r Run, cp *Checkpoint, verdicts map[string]bool) error {
	if err := paperPublicationError(cp.Paper, verdicts); err != nil {
		issue := cp.Paper.Issues[0]
		cp.Paper.Failure = cp.Paper.StageFailures[issue.Stage]
		return s.failPaperStage(ctx, r, cp, issue.Stage, err, nil)
	}
	return nil
}

// Long scans yield to local retrieval while there is still budget to produce
// and review the report. A model call already in flight is never abandoned here.
func paperExtractionBudget(ctx context.Context, r Run, cp *Checkpoint) bool {
	reserveCalls := 1 + len(paperFields) + paperReviewLimit(r.Task) + max(0, paperRecoveryLimit-paperRecoveryUsed(cp.Paper))
	if paperCallLimit(r.Task)-cp.Calls < reserveCalls {
		return false
	}
	timeout := cp.Paper.CallTimeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	deadline, ok := ctx.Deadline()
	if r.Deadline != nil && (!ok || r.Deadline.Before(deadline)) {
		deadline, ok = *r.Deadline, true
	}
	return !ok || time.Until(deadline) > time.Duration(2+len(paperFields))*timeout+5*time.Second
}
