package agent

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	paperConversationContextLimit = 24 << 10
	paperHistoryQuestionLimit     = 2 << 10
	paperHistoryAnswerLimit       = 4 << 10
	paperReportFieldLimit         = 400
	paperContextTruncation        = "\n[已截断]"
)

type PaperConversationTurn struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type PaperConversationContext struct {
	ReportOutcome string                  `json:"report_outcome,omitempty"`
	Turns         []PaperConversationTurn `json:"turns"`
	Report        *PaperReport            `json:"report,omitempty"`
	Truncated     bool                    `json:"truncated,omitempty"`
}

func copyPaperConversationContext(value PaperConversationContext) PaperConversationContext {
	copy := PaperConversationContext{ReportOutcome: value.ReportOutcome, Turns: append([]PaperConversationTurn{}, value.Turns...), Truncated: value.Truncated}
	if value.Report != nil {
		report := *value.Report
		copy.Report = &report
	}
	return copy
}

func truncatePaperContext(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= limit {
		return value
	}
	if limit < len(paperContextTruncation) {
		return ""
	}
	end := limit - len(paperContextTruncation)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + paperContextTruncation
}

func paperReportFields(report *PaperReport) []*string {
	return []*string{&report.Problem, &report.Method, &report.Experiments, &report.Results, &report.Limitations}
}

// Historical messages are context for understanding the question, not evidence.
// Keep complete pairs, prefer recent turns, and budget the actual serialized
// JSON because escaping can be substantially larger than the source strings.
func boundedPaperConversationContext(value PaperConversationContext) PaperConversationContext {
	context := copyPaperConversationContext(value)
	if len(context.Turns) > 3 {
		context.Turns = context.Turns[len(context.Turns)-3:]
		context.Truncated = true
	}
	for i := range context.Turns {
		turn := &context.Turns[i]
		question := truncatePaperContext(turn.Question, paperHistoryQuestionLimit)
		answer := truncatePaperContext(turn.Answer, paperHistoryAnswerLimit)
		context.Truncated = context.Truncated || question != turn.Question || answer != turn.Answer
		turn.Question, turn.Answer = question, answer
	}
	if context.Report != nil {
		for _, field := range paperReportFields(context.Report) {
			clipped := truncatePaperContext(*field, paperReportFieldLimit)
			context.Truncated = context.Truncated || clipped != *field
			*field = clipped
		}
	}
	for {
		raw, _ := json.Marshal(context)
		if len(raw) <= paperConversationContextLimit || !reducePaperConversationContext(&context) {
			return context
		}
	}
}

// Reduce one deterministic step. Only the copied context is ever changed: old
// complete turns go first, followed by shorter report fields, then no report.
func reducePaperConversationContext(context *PaperConversationContext) bool {
	if len(context.Turns) > 0 {
		context.Turns = context.Turns[1:]
		context.Truncated = true
		return true
	}
	if context.Report == nil {
		return false
	}
	reduced := false
	for _, field := range paperReportFields(context.Report) {
		if len(*field) > len(paperContextTruncation) {
			*field = truncatePaperContext(*field, max(len(*field)/2, len(paperContextTruncation)))
			reduced = true
		}
	}
	if !reduced {
		context.Report = nil
		context.ReportOutcome = ""
	}
	context.Truncated = true
	return true
}

// Each stage gets a deterministic projection of the same persisted snapshot.
// Never trim the current question or evidence to make room for conversation
// context, and never replace the saved snapshot with this smaller projection.
func paperInputWithContext(input map[string]any, snapshot PaperConversationContext) (map[string]any, error) {
	return paperInputWithContextBudget(input, snapshot, boundedPaperInput)
}

// The caller supplies the complete request budget, including the stage prompt,
// schema and reserved output. Only optional historical context is reduced here.
func paperInputWithContextBudget(input map[string]any, snapshot PaperConversationContext, serialize func(any) ([]byte, error)) (map[string]any, error) {
	request := make(map[string]any, len(input)+1)
	for key, value := range input {
		request[key] = value
	}
	context := copyPaperConversationContext(snapshot)
	for {
		request["conversation_context"] = context
		if _, err := serialize(request); err == nil {
			return request, nil
		}
		if !reducePaperConversationContext(&context) {
			// An empty optional object need not make an otherwise bounded
			// question fail; mandatory content still has the same hard limit.
			delete(request, "conversation_context")
			_, err := serialize(request)
			return request, err
		}
	}
}
