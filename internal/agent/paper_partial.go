package agent

// Only independently reviewed facts make a technically incomplete result
// publishable. Missing material by itself remains a normal, valid answer.
func paperPublicationError(pc *PaperCheckpoint, verdicts map[string]bool) error {
	if len(pc.Issues) == 0 {
		return nil
	}
	for _, supported := range verdicts {
		if supported {
			return nil
		}
	}
	return paperError(pc.Issues[0].Code)
}

func applyPaperPartial(result *PaperResult, pc *PaperCheckpoint) {
	result.Outcome = "complete"
	if len(pc.Issues) > 0 {
		result.Outcome = "partial"
		result.Issues = append([]PaperIssue(nil), pc.Issues...)
	}
}
