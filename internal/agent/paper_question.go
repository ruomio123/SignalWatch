package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"signalwatch/internal/document"
	"time"
)

const paperRetrieverVersion = "passage-structured-bm25-rrf60-v3"
const paperInitialPassageLimit = 24

type PaperAnswerInput struct {
	Request  string     `json:"request"`
	Evidence []Citation `json:"evidence"`
}

type PaperQACheckpoint struct {
	RetrieverVersion     string                     `json:"retriever_version"`
	NormalizationRequest string                     `json:"normalization_request"`
	Questions            []PaperQuestion            `json:"questions"`
	Initial              *PaperAnswerInput          `json:"initial,omitempty"`
	Supplement           *PaperSupplementCheckpoint `json:"supplement,omitempty"`
}

// Priorities are preserved when packing; the selected passages themselves are
// immutable server-owned text, never model-generated or clipped quotations.
func rankPaperEvidence(evidence []Citation, queries []string, limit int) []Citation {
	return rankPaperEvidenceForTask(TaskPaperFollowup, evidence, queries, limit)
}

func rankPaperEvidenceForTask(task string, evidence []Citation, queries []string, limit int) []Citation {
	limit = min(limit, paperTotalPassageLimit)
	if limit <= 0 {
		return []Citation{}
	}
	passages := make([]document.SearchPassage, 0, len(evidence))
	index := evidenceIndex(evidence)
	for _, source := range evidence {
		var page, chunk, start int
		if source.SourceType != "html" {
			if _, err := fmt.Sscanf(source.ID, "p%d-c%d-s%d", &page, &chunk, &start); err != nil {
				continue
			}
		}
		passages = append(passages, document.SearchPassage{ID: source.ID, Page: page, Chunk: chunk, Start: start, Text: source.Quote})
	}
	out := []Citation{}
	seen := map[string]bool{}
	add := func(source Citation) {
		if !seen[source.ID] && len(out) < limit {
			out = append(out, source)
			seen[source.ID] = true
		}
	}
	for _, source := range explicitPaperStructures(evidence, queries) {
		add(source)
	}
	var ranked []document.SearchPassage
	if task == TaskPaperReproduction {
		ranked = document.RankReproductionPassages(passages, queries, limit)
	} else {
		ranked = document.RankPassages(passages, queries, limit)
	}
	for _, passage := range ranked {
		add(index[passage.ID])
	}
	return out
}

func packPaperAnswerInput(r Run, pc *PaperCheckpoint, questions []PaperQuestion, candidates []Citation) (PaperAnswerInput, error) {
	field := paperQuestionPolicy(r.Task).Field
	serialize := func(selected []Citation) ([]byte, error) {
		request := paperInput(pc, field, selected)
		request["question"], request["original_question"] = r.Question, r.Question
		request["questions"], request["coverage"] = questions, "retrieved_passages"
		projected, err := paperInputWithContext(request, pc.ConversationContext)
		if err != nil {
			return nil, err
		}
		return boundedPaperInput(projected)
	}
	selected := []Citation{}
	// An indivisible unit that cannot fit by itself must not evict all usable
	// lower-priority evidence. History is reduced by the same serializer first.
	for _, candidate := range candidates {
		if len(selected) == paperInitialPassageLimit {
			break
		}
		if _, err := serialize([]Citation{candidate}); err != nil {
			if candidate.SourceType == "html" {
				pc.StructuredGap = "input_budget"
			}
			continue
		}
		selected = append(selected, candidate)
	}
	for {
		raw, err := serialize(selected)
		if err == nil {
			return PaperAnswerInput{Request: string(raw), Evidence: selected}, nil
		}
		if len(selected) == 0 {
			return PaperAnswerInput{}, err
		}
		if selected[len(selected)-1].SourceType == "html" {
			pc.StructuredGap = "input_budget"
		}
		selected = selected[:len(selected)-1]
	}
}

func (s *Service) processPaperQuestion(ctx context.Context, r Run, c Conversation, cp *Checkpoint, check func(context.Context) error, evidence []Citation) error {
	workflow := paperQuestionPolicy(r.Task)
	pc := cp.Paper
	if !pc.ContextCaptured {
		if len(pc.Outputs) > 0 {
			return paperError("invalid_checkpoint")
		}
		history, err := s.Store.PaperHistory(ctx, c.ID, pc.PaperHash, r.ID)
		if err != nil {
			return err
		}
		if err := check(ctx); err != nil {
			return err
		}
		pc.ConversationContext = boundedPaperConversationContext(history)
		pc.ContextCaptured = true
		if err := s.Store.Save(ctx, r, *cp, "planning_paper", nil); err != nil {
			return err
		}
	}
	if pc.QA == nil {
		pc.QA = &PaperQACheckpoint{RetrieverVersion: paperRetrieverVersion}
	}
	qa := pc.QA
	if qa.RetrieverVersion != paperRetrieverVersion {
		return paperError("invalid_checkpoint")
	}
	if qa.NormalizationRequest == "" {
		input, err := paperInputWithContext(map[string]any{"paper": pc.Context, "question": r.Question}, pc.ConversationContext)
		if err != nil {
			return err
		}
		raw, err := boundedPaperInput(input)
		if err != nil {
			return err
		}
		qa.NormalizationRequest = string(raw)
		if err := s.Store.Save(ctx, r, *cp, workflow.PlanStage, nil); err != nil {
			return err
		}
	}
	raw, err := s.paperCall(ctx, r, cp, check, workflow.PlanStage, workflow.PlanPrompt, paperSerializedInput(qa.NormalizationRequest), func(raw []byte) error {
		return paperContract(workflow.PlanStage).Validate(raw, paperStageValidation{})
	})
	if err != nil {
		return err
	}
	questions, err := workflow.DecodePlan(raw)
	if err != nil {
		return err
	}
	if qa.Questions == nil {
		qa.Questions = questions
	} else {
		want, _ := json.Marshal(questions)
		got, _ := json.Marshal(qa.Questions)
		if string(want) != string(got) {
			return paperError("invalid_checkpoint")
		}
	}
	if qa.Initial == nil {
		selected := evidence
		if pc.Mode == "fulltext" {
			queries := make([]string, 0, len(questions))
			for _, question := range questions {
				queries = append(queries, question.Question+" "+question.Query)
			}
			selected = rankPaperEvidenceForTask(r.Task, evidence, queries, paperInitialPassageLimit)
		}
		input, err := packPaperAnswerInput(r, pc, questions, selected)
		if err != nil {
			return s.failPaperStage(ctx, r, cp, workflow.AnalysisStage, err, nil)
		}
		qa.Initial = &input
		pc.CurrentStage = "retrieving_evidence"
		if err := check(ctx); err != nil {
			return err
		}
		if err := s.Store.Save(ctx, r, *cp, "retrieving_evidence", nil); err != nil {
			return err
		}
	}
	input := qa.Initial
	if err := validatePaperAnswerInput(input, questions); err != nil {
		return err
	}
	raw, err = s.paperCall(ctx, r, cp, check, workflow.AnalysisStage, workflow.AnalysisPrompt, paperSerializedInput(input.Request), func(raw []byte) error {
		return paperContract(workflow.AnalysisStage).Validate(raw, paperStageValidation{Evidence: input.Evidence, Questions: questions})
	})
	if err != nil {
		return err
	}
	analysis, err := workflow.DecodeAnalysis(raw, input.Evidence, questions, true)
	if err != nil {
		return err
	}
	analysis, input, err = s.supplementPaperAnswer(ctx, r, cp, check, evidence, raw, analysis)
	if err != nil {
		return err
	}
	claims := paperAnswerReviewClaims(analysis)
	verdicts, err := s.reviewPaper(ctx, r, cp, check, claims, evidenceIndex(input.Evidence))
	if err != nil {
		return err
	}
	content, result, citations := workflow.Render(r, cp, questions, analysis, input.Evidence, verdicts)
	if err := check(ctx); err != nil {
		return err
	}
	encoded, _ := json.Marshal(result)
	refs, _ := json.Marshal(citations)
	message := Message{ConversationID: c.ID, RunID: r.ID, Role: "assistant", Content: content, Result: encoded, Citations: refs, Provider: r.Provider, Model: r.Model, CreatedAt: time.Now().UTC()}
	return s.Store.Finish(ctx, r, "completed", "", &message)
}
