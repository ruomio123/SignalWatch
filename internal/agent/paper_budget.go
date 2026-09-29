package agent

import "signalwatch/internal/generation"

func paperOutputTokens(pc *PaperCheckpoint, stage string) int {
	tokens := paperStageTokens(stage)
	if pc.Limits.MaxOutputTokens > 0 {
		tokens = min(tokens, pc.Limits.MaxOutputTokens)
	}
	return tokens
}

func paperFitsSerialized(pc *PaperCheckpoint, stage, prompt string, raw []byte) error {
	if len(raw) > paperInputLimit {
		return paperError("context_too_large")
	}
	// Isolated callers without a captured model retain the byte contract. Live
	// workflows capture and validate deployment limits before preparing input.
	if pc.Limits == (generation.ModelLimits{}) {
		return nil
	}
	if !generation.RequestFits(pc.Limits, paperPolicy+"\n"+prompt, raw, paperStageSchema(stage), paperOutputTokens(pc, stage)) {
		return paperError("context_too_large")
	}
	return nil
}

func paperFitsInput(pc *PaperCheckpoint, stage, prompt string, input any) ([]byte, error) {
	raw, err := boundedPaperInput(input)
	if err != nil {
		return nil, err
	}
	if err := paperFitsSerialized(pc, stage, prompt, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// The caller orders candidates by batch and relevance. Whole immutable
// passages are admitted in that order; an oversized table cannot evict later
// usable text, and duplicate sources cannot consume the budget twice.
func packPaperReportInput(pc *PaperCheckpoint, field string, candidates []Citation) (PaperAnswerInput, error) {
	serialize := func(selected []Citation) ([]byte, error) {
		return paperFitsInput(pc, "analyzing_"+field, fieldPromptFor(field, true), paperInput(pc, field, selected))
	}
	selected := []Citation{}
	seen := map[string]bool{}
	if _, err := serialize(selected); err != nil {
		return PaperAnswerInput{}, err
	}
	for _, candidate := range candidates {
		if seen[candidate.ID] {
			continue
		}
		seen[candidate.ID] = true
		trial := append(append([]Citation(nil), selected...), candidate)
		if _, err := serialize(trial); err != nil {
			pc.Coverage = "retrieved_passages"
			if candidate.SourceType == "html" {
				pc.StructuredGap = "input_budget"
			}
			continue
		}
		selected = trial
	}
	raw, err := serialize(selected)
	return PaperAnswerInput{Request: string(raw), Evidence: selected}, err
}
