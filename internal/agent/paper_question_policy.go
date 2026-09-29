package agent

// Every question-like task selects explicit stage contracts and output policy.
// Reproduction never falls through to the ordinary six-claim QA budget.
type paperQuestionWorkflow struct {
	Field            string
	PlanStage        string
	PlanPrompt       string
	AnalysisStage    string
	AnalysisPrompt   string
	SupplementStage  string
	SupplementPrompt string
	DecodePlan       func([]byte) ([]PaperQuestion, error)
	DecodeAnalysis   func([]byte, []Citation, []PaperQuestion, bool) (PaperAnswerAnalysis, error)
	Render           func(Run, *Checkpoint, []PaperQuestion, PaperAnswerAnalysis, []Citation, map[string]bool) (string, PaperResult, []Citation)
}

func paperQuestionPolicy(task string) paperQuestionWorkflow {
	switch task {
	case "", TaskPaperFollowup:
		return paperQuestionWorkflow{Field: "answer", PlanStage: "normalizing_question", PlanPrompt: paperQuestionPrompt, AnalysisStage: "analyzing_answer", AnalysisPrompt: paperAnswerPrompt, SupplementStage: paperSupplementStage, SupplementPrompt: paperSupplementAnswerPrompt, DecodePlan: decodePaperQuestions, DecodeAnalysis: decodePaperAnswerFor, Render: renderPaperAnswer}
	case TaskPaperReproduction:
		return paperQuestionWorkflow{Field: "reproduction", PlanStage: "planning_reproduction", PlanPrompt: paperReproductionPlanPrompt, AnalysisStage: "analyzing_reproduction", AnalysisPrompt: paperReproductionPromptFor(false), SupplementStage: paperReproductionSupplementStage, SupplementPrompt: paperReproductionPromptFor(true), DecodePlan: decodePaperReproductionPlan, DecodeAnalysis: decodePaperReproduction, Render: renderPaperReproduction}
	default:
		panic("unregistered question workflow")
	}
}
func paperSupplementAnalysisStage(stage string) bool {
	return stage == paperSupplementStage || stage == paperReproductionSupplementStage
}
