package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"signalwatch/internal/subscription"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Service) CreateConversation(ctx context.Context, uid uint64, kind string, pid *uint64) (Conversation, error) {
	if uid == 0 || (kind != "subscription" && kind != "paper") || (kind == "subscription" && pid != nil) || (kind == "paper" && (pid == nil || *pid == 0)) {
		return Conversation{}, ErrInput
	}
	title := "订阅助手"
	if pid != nil {
		p, err := s.Papers.Get(ctx, uid, *pid)
		if err != nil {
			return Conversation{}, ErrNotFound
		}
		title = p.Title
	}
	runes := []rune(title)
	if len(runes) > 160 {
		title = string(runes[:160])
	}
	now := time.Now().UTC()
	c := Conversation{ID: rand.Text(), UserID: uid, Kind: kind, PaperID: pid, Title: title, CreatedAt: now, UpdatedAt: now}
	return c, s.Store.CreateConversation(ctx, c)
}
func (s *Service) Conversation(ctx context.Context, uid uint64, id string) (Conversation, error) {
	c, err := s.Store.Conversation(ctx, uid, id)
	if err != nil {
		return c, err
	}
	if c.PaperID != nil {
		p, err := s.Papers.Get(ctx, uid, *c.PaperID)
		if err != nil {
			return Conversation{}, ErrNotFound
		}
		report, err := s.Store.LatestPaperReport(ctx, c.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Conversation{}, err
		}
		if err == nil {
			_, c.PaperReportReady = validPaperReport(report, paperSnapshotHash(p))
		}
	}
	return c, nil
}
func (s *Service) PaperReport(ctx context.Context, uid uint64, id string) (PaperReportResponse, error) {
	c, err := s.Store.Conversation(ctx, uid, id)
	if err != nil {
		return PaperReportResponse{}, err
	}
	if c.Kind != "paper" || c.PaperID == nil {
		return PaperReportResponse{}, ErrNotFound
	}
	p, err := s.Papers.Get(ctx, uid, *c.PaperID)
	if err != nil {
		return PaperReportResponse{}, ErrNotFound
	}
	report, err := s.Store.LatestPaperReport(ctx, c.ID)
	if errors.Is(err, ErrNotFound) {
		return PaperReportResponse{}, nil
	}
	if err != nil {
		return PaperReportResponse{}, err
	}
	_, matches := validPaperReport(report, paperSnapshotHash(p))
	return PaperReportResponse{Report: &report, MatchesCurrentPaper: matches}, nil
}
func (s *Service) Conversations(ctx context.Context, uid uint64, kind string, pid *uint64, page int) ([]Conversation, bool, error) {
	if page < 1 || page > 100000 || (kind != "" && kind != "paper" && kind != "subscription") {
		return nil, false, ErrInput
	}
	rows, hasMore, err := s.Store.Conversations(ctx, uid, kind, pid, page)
	if err != nil {
		return nil, false, err
	}
	out := []Conversation{}
	for _, c := range rows {
		if c.PaperID != nil {
			var err error
			c, err = s.Conversation(ctx, uid, c.ID)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return nil, false, err
			}
		}
		out = append(out, c)
	}
	return out, hasMore, nil
}
func (s *Service) Submit(ctx context.Context, uid uint64, id string, input SubmitInput) (Run, error) {
	c, err := s.Conversation(ctx, uid, id)
	if err != nil {
		return Run{}, err
	}
	workflow := ""
	if c.Kind == "paper" {
		if input.Task == "" {
			input.Task = TaskPaperFollowup
		}
		if input.Task != TaskPaperReport && input.Task != TaskPaperFollowup && input.Task != TaskPaperReproduction {
			return Run{}, ErrInput
		}
		if input.Task == TaskPaperReport {
			input.Question = PaperGoal
		}
		if input.Task == TaskPaperReproduction {
			input.Question = PaperReproductionGoal
		}
		workflow = PaperWorkflowVersion
	} else if input.Task != "" {
		return Run{}, ErrInput
	}
	input.Question = strings.TrimSpace(input.Question)
	for _, ch := range input.IdempotencyKey {
		if ch < 33 || ch > 126 {
			return Run{}, ErrInput
		}
	}
	if !utf8.ValidString(input.Question) || utf8.RuneCountInString(input.Question) < 1 || utf8.RuneCountInString(input.Question) > 2000 || len(input.IdempotencyKey) < 8 || len(input.IdempotencyKey) > 64 || len(input.Provider) > 32 || len(input.Model) > 64 || len(input.CredentialID) > 64 {
		return Run{}, ErrInput
	}
	if input.ContextMode == "" {
		input.ContextMode = "fulltext"
	}
	if input.ContextMode != "fulltext" && input.ContextMode != "abstract" {
		return Run{}, ErrInput
	}
	choice, err := s.Gateway.Selection(ctx, uid, input.Provider, input.Model, input.CredentialID)
	if err != nil {
		return Run{}, err
	}
	now := time.Now().UTC()
	cp, _ := json.Marshal(Checkpoint{Phase: "ready", Observations: []Observation{}, Evidence: []Citation{}})
	r := Run{Task: input.Task, WorkflowVersion: workflow, ID: rand.Text(), ConversationID: c.ID, UserID: uid, Question: input.Question, Provider: input.Provider, Model: input.Model, Generation: choice.Generation, Version: choice.Version, IdempotencyKey: input.IdempotencyKey, InputHash: submissionHash(input, workflow), ContextMode: input.ContextMode, State: "pending", Progress: "queued", Checkpoint: cp, CreatedAt: now, UpdatedAt: now}
	return s.Store.Submit(ctx, r, input)
}

// Keep the original serialized shape so a normalized request can be checked
// against the workflow version that originally owned an idempotency key.
func submissionHash(input SubmitInput, workflow string) string {
	raw, _ := json.Marshal(struct {
		SubmitInput
		WorkflowVersion string `json:"workflow_version,omitempty"`
	}{input, workflow})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (s *Service) RunByID(ctx context.Context, uid uint64, id string) (Run, error) {
	r, err := s.Store.RunByID(ctx, uid, id)
	if err != nil {
		return r, err
	}
	_, err = s.Conversation(ctx, uid, r.ConversationID)
	var cp Checkpoint
	if err == nil && json.Unmarshal(r.Checkpoint, &cp) == nil && cp.Paper != nil {
		if r.State == "failed" || r.State == "unknown" || r.State == "cancelled" {
			stage := cp.Paper.CurrentStage
			if _, registered := paperStageContracts[stage]; registered || stage == "retrieving_evidence" || stage == "retrieving_supplement" {
				r.FailureStage = stage
			}
		}
		r.RetrievalSummary = paperRetrievalSummary(cp.Paper.QA)
		if repair := cp.Paper.Repair; repair != nil && paperLabels[repair.Field] != "" {
			state := repair.State
			switch state {
			case "pending", "calling", "completed", "failed", "budget_exceeded":
				if (r.State == "failed" || r.State == "unknown" || r.State == "cancelled") && (state == "pending" || state == "calling") {
					state = "failed"
				}
				r.RepairSummary = &PaperRepairSummary{Field: repair.Field, State: state, Attempted: repair.Attempted}
			}
		}
		if r.State == "failed" && cp.Paper.Failure != nil && cp.Paper.Failure.Code == r.FailureCode {
			r.FailureDetail = cp.Paper.Failure
		}
		if len(cp.Paper.ReviewPlan) > 0 {
			progress := &PaperReviewProgress{Total: len(cp.Paper.ReviewPlan)}
			for i := range cp.Paper.ReviewPlan {
				if _, ok := cp.Paper.Outputs[paperReviewStage(i)]; ok {
					progress.Completed++
				}
			}
			r.ReviewProgress = progress
		}
	}
	return r, err
}
func (s *Service) Confirm(ctx context.Context, uid uint64, id string, version uint32) (subscription.PublicSubscription, error) {
	d, err := s.Store.Draft(ctx, uid, id)
	if err != nil {
		return subscription.PublicSubscription{}, err
	}
	if _, err = s.Conversation(ctx, uid, d.ConversationID); err != nil {
		return subscription.PublicSubscription{}, err
	}
	return s.Subscriptions.ConfirmDraft(ctx, uid, id, version)
}
