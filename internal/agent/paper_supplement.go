package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const paperSupplementStage = "analyzing_answer_supplement"
const paperSupplementPassageLimit = 8
const paperTotalPassageLimit = 32

// The original candidate lives in Outputs[analyzing_answer]. This record freezes
// the local retrieval decision and the exact second request before any call.
type PaperSupplementCheckpoint struct {
	State   string                 `json:"state"`
	Reason  string                 `json:"reason,omitempty"`
	Queries []PaperSupplementQuery `json:"queries"`
	Input   *PaperAnswerInput      `json:"input,omitempty"`
	Added   int                    `json:"added"`
}

type PaperRetrievalSummary struct {
	State    string `json:"state"`
	Selected int    `json:"selected"`
	Added    int    `json:"added"`
	Reason   string `json:"reason,omitempty"`
}

// Only an optional call that has not started can return this private sentinel.
var errPaperSupplementSkipped = errors.New("paper supplemental analysis skipped before calling")

func paperSupplementBudget(ctx context.Context, r Run, cp *Checkpoint, now time.Time) string {
	remainingCalls, remainingTime := 4, 125*time.Second
	if cp.Paper.Repair != nil && cp.Paper.Repair.Attempted {
		remainingCalls, remainingTime = 3, 95*time.Second
	}
	if paperCallLimit(r.Task)-cp.Calls < remainingCalls {
		return "call_budget"
	}
	deadline, bounded := ctx.Deadline()
	if r.Deadline != nil && (!bounded || r.Deadline.Before(deadline)) {
		deadline, bounded = *r.Deadline, true
	}
	if bounded && deadline.Sub(now) < remainingTime {
		return "time_budget"
	}
	return ""
}

func validPaperSupplementReason(reason string) bool {
	switch reason {
	case "no_queries", "abstract_only", "no_new_evidence", "input_budget", "time_budget", "call_budget":
		return true
	}
	return false
}

func paperRetrievalSummary(qa *PaperQACheckpoint) *PaperRetrievalSummary {
	if qa == nil || qa.Initial == nil || len(qa.Initial.Evidence) > paperInitialPassageLimit {
		return nil
	}
	public := &PaperRetrievalSummary{State: "initial_ready", Selected: len(qa.Initial.Evidence)}
	if supplement := qa.Supplement; supplement != nil {
		switch supplement.State {
		case "pending", "ready", "calling", "completed", "skipped":
			public.State = supplement.State
		default:
			return public
		}
		if supplement.State == "skipped" {
			if validPaperSupplementReason(supplement.Reason) {
				public.Reason = supplement.Reason
			}
		} else if supplement.Input != nil && supplement.Added > 0 && supplement.Added <= paperSupplementPassageLimit && len(supplement.Input.Evidence) <= paperTotalPassageLimit {
			public.Selected, public.Added = len(supplement.Input.Evidence), supplement.Added
		}
	}
	return public
}

func validatePaperAnswerInput(input *PaperAnswerInput, questions []PaperQuestion) error {
	if input == nil || input.Request == "" || len(input.Request) > paperInputLimit || len(input.Evidence) > paperTotalPassageLimit {
		return paperError("invalid_checkpoint")
	}
	var request struct {
		Questions []PaperQuestion   `json:"questions"`
		Evidence  []evidencePassage `json:"evidence"`
	}
	if json.Unmarshal([]byte(input.Request), &request) != nil {
		return paperError("invalid_checkpoint")
	}
	same := func(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return string(x) == string(y) }
	if !same(request.Questions, questions) || !same(request.Evidence, evidencePassages(input.Evidence)) {
		return paperError("invalid_checkpoint")
	}
	seen := map[string]bool{}
	for _, source := range input.Evidence {
		if source.ID == "" || source.Quote == "" || seen[source.ID] {
			return paperError("invalid_checkpoint")
		}
		seen[source.ID] = true
	}
	return nil
}

// Protect every passage cited by the complete draft. History is reduced first,
// followed by uncited old passages, then low-priority new passages. Neither the
// draft nor any retained passage is truncated to fit the serialized byte budget.
func packPaperSupplementInput(r Run, pc *PaperCheckpoint, questions []PaperQuestion, initial PaperAnswerInput, candidate []byte, additions []Citation) (PaperAnswerInput, int, error) {
	refs, err := paperAnswerCandidateEvidence(candidate)
	if err != nil {
		return PaperAnswerInput{}, 0, err
	}
	pinnedIDs := map[string]bool{}
	for _, ref := range refs {
		pinnedIDs[ref.ID] = true
	}
	pinned, optional, fresh := []Citation{}, []Citation{}, []Citation{}
	seen := map[string]bool{}
	for _, source := range initial.Evidence {
		if seen[source.ID] {
			continue
		}
		seen[source.ID] = true
		if pinnedIDs[source.ID] {
			pinned = append(pinned, source)
		} else {
			optional = append(optional, source)
		}
	}
	if len(pinned) != len(pinnedIDs) {
		return PaperAnswerInput{}, 0, paperError("invalid_checkpoint")
	}
	for _, source := range additions {
		if !seen[source.ID] && len(fresh) < paperSupplementPassageLimit && len(initial.Evidence)+len(fresh) < paperTotalPassageLimit {
			fresh = append(fresh, source)
			seen[source.ID] = true
		}
	}
	for len(fresh) > 0 {
		selected := append(append(append([]Citation{}, pinned...), fresh...), optional...)
		request := paperInput(pc, "answer", selected)
		request["question"], request["original_question"] = r.Question, r.Question
		request["questions"], request["coverage"] = questions, "retrieved_passages"
		request["initial_answer"] = json.RawMessage(candidate)
		newIDs := make([]string, 0, len(fresh))
		for _, source := range fresh {
			newIDs = append(newIDs, source.ID)
		}
		request["new_evidence_ids"] = newIDs
		projected, err := paperInputWithContext(request, pc.ConversationContext)
		if err == nil {
			raw, err := boundedPaperInput(projected)
			return PaperAnswerInput{Request: string(raw), Evidence: selected}, len(fresh), err
		}
		if len(optional) > 0 {
			optional = optional[:len(optional)-1]
		} else {
			fresh = fresh[:len(fresh)-1]
		}
	}
	return PaperAnswerInput{}, 0, paperError("context_too_large")
}

func (s *Service) supplementPaperAnswer(ctx context.Context, r Run, cp *Checkpoint, check func(context.Context) error, allEvidence []Citation, candidate []byte, initialAnalysis PaperAnswerAnalysis) (PaperAnswerAnalysis, *PaperAnswerInput, error) {
	qa := cp.Paper.QA
	fail := func(err error) (PaperAnswerAnalysis, *PaperAnswerInput, error) {
		return PaperAnswerAnalysis{}, nil, err
	}
	save := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := check(ctx); err != nil {
			return err
		}
		cp.Paper.CurrentStage = "retrieving_supplement"
		return s.Store.Save(ctx, r, *cp, "retrieving_supplement", nil)
	}
	skip := func(reason string) (PaperAnswerAnalysis, *PaperAnswerInput, error) {
		qa.Supplement.State, qa.Supplement.Reason = "skipped", reason
		if err := save(); err != nil {
			return fail(err)
		}
		return initialAnalysis, qa.Initial, nil
	}
	if qa.Supplement == nil {
		qa.Supplement = &PaperSupplementCheckpoint{State: "pending", Queries: append([]PaperSupplementQuery{}, initialAnalysis.SupplementalQueries...)}
		if len(qa.Supplement.Queries) == 0 {
			return skip("no_queries")
		}
		if cp.Paper.Mode != "fulltext" {
			return skip("abstract_only")
		}
		if reason := paperSupplementBudget(ctx, r, cp, time.Now()); reason != "" {
			return skip(reason)
		}
		if err := save(); err != nil {
			return fail(err)
		}
	}
	supplement := qa.Supplement
	wantQueries, _ := json.Marshal(initialAnalysis.SupplementalQueries)
	actualQueries, _ := json.Marshal(supplement.Queries)
	if string(wantQueries) != string(actualQueries) {
		return fail(paperError("invalid_checkpoint"))
	}
	if supplement.State == "skipped" {
		if !validPaperSupplementReason(supplement.Reason) {
			return fail(paperError("invalid_checkpoint"))
		}
		return initialAnalysis, qa.Initial, nil
	}
	if supplement.State == "pending" {
		if reason := paperSupplementBudget(ctx, r, cp, time.Now()); reason != "" {
			return skip(reason)
		}
		queries := make([]string, 0, len(supplement.Queries))
		for _, query := range supplement.Queries {
			queries = append(queries, query.Query)
		}
		old := evidenceIndex(qa.Initial.Evidence)
		fresh := []Citation{}
		for _, source := range rankPaperEvidence(allEvidence, queries, paperTotalPassageLimit) {
			if _, exists := old[source.ID]; !exists && len(fresh) < paperSupplementPassageLimit {
				fresh = append(fresh, source)
			}
		}
		if len(fresh) == 0 {
			return skip("no_new_evidence")
		}
		input, added, err := packPaperSupplementInput(r, cp.Paper, qa.Questions, *qa.Initial, candidate, fresh)
		if err != nil {
			if paperFailureCode(err) == "context_too_large" {
				return skip("input_budget")
			}
			return fail(err)
		}
		supplement.Input, supplement.Added, supplement.State = &input, added, "ready"
		if err := save(); err != nil {
			return fail(err)
		}
	}
	if supplement.State != "ready" && supplement.State != "completed" {
		return fail(paperError("invalid_checkpoint"))
	}
	if err := validatePaperAnswerInput(supplement.Input, qa.Questions); err != nil {
		return fail(err)
	}
	input := supplement.Input
	raw, err := s.paperCall(ctx, r, cp, check, paperSupplementStage, paperSupplementAnswerPrompt, paperSerializedInput(input.Request), func(raw []byte) error {
		return paperContract(paperSupplementStage).Validate(raw, paperStageValidation{Evidence: input.Evidence, Questions: qa.Questions})
	})
	if errors.Is(err, errPaperSupplementSkipped) {
		return skip(supplement.Reason)
	}
	if err != nil {
		return fail(err)
	}
	analysis, err := decodePaperSupplementAnswer(raw, input.Evidence, qa.Questions)
	return analysis, input, err
}
