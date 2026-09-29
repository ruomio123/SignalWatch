package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"signalwatch/internal/generation"
	"signalwatch/internal/paper"
	"time"
)

const (
	TaskPaperReport       = "paper_report"
	TaskPaperFollowup     = "paper_followup"
	TaskPaperReproduction = "paper_reproduction"
	PaperWorkflowVersion  = "paper-fixed-v14"
	PaperReportMessage    = "快速了解论文"
	PaperGoal             = "帮助用户快速了解当前论文"
	paperInputLimit       = 64 << 10
)

var paperFields = []string{"problem", "method", "experiments", "results", "limitations"}
var paperLabels = map[string]string{"problem": "论文问题", "method": "核心方法", "experiments": "实验验证", "results": "主要结果", "limitations": "局限性", "answer": "回答", "reproduction": "复现清单"}

func runDuration(r Run) time.Duration {
	if r.Task == TaskPaperReproduction {
		return 300 * time.Second
	}
	if r.Task == TaskPaperReport {
		return 15 * time.Minute
	}
	return 180 * time.Second
}

type PaperContext struct {
	Title    string   `json:"title"`
	Authors  []string `json:"authors"`
	Abstract string   `json:"abstract"`
	ArXivID  string   `json:"arxiv_id"`
}

func contextOf(p paper.PublicPaper) PaperContext {
	return PaperContext{p.Title, p.Authors, p.Abstract, p.ArXivID}
}
func paperSnapshotHash(p paper.PublicPaper) string {
	raw, _ := json.Marshal(struct {
		Context PaperContext
		URL     string
		Updated time.Time
	}{contextOf(p), p.PDFURL, p.ArXivUpdatedAt.UTC()})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func publicPaperSnapshot(p paper.Paper) paper.PublicPaper {
	var authors []string
	_ = json.Unmarshal(p.AuthorsJSON, &authors)
	return paper.PublicPaper{Title: p.Title, Authors: authors, Abstract: p.Abstract, ArXivID: p.ArXivID, PDFURL: p.PDFURL, ArXivUpdatedAt: p.ArXivUpdatedAt}
}

type PaperFailure struct {
	Code  string `json:"code"`
	Path  string `json:"path"`
	Rule  string `json:"rule"`
	Count *int   `json:"count,omitempty"`
	Limit *int   `json:"limit,omitempty"`
	Unit  string `json:"unit,omitempty"`
}

type PaperReviewBatch struct {
	ClaimIDs []string `json:"claim_ids"`
	Request  string   `json:"request"`
}

type PaperReviewProgress struct {
	Completed int `json:"completed"`
	Total     int `json:"total"`
}

// Candidate and Request stay private in checkpoint JSON. Public status is a
// separate allowlisted projection, never this internal recovery record.
type PaperRepair struct {
	Kind            string          `json:"kind,omitempty"`
	RawCandidate    string          `json:"raw_candidate,omitempty"`
	OriginalRequest string          `json:"original_request,omitempty"`
	Prompt          string          `json:"prompt,omitempty"`
	Stage           string          `json:"stage"`
	Field           string          `json:"field"`
	Candidate       json.RawMessage `json:"candidate,omitempty"`
	Request         string          `json:"request,omitempty"`
	Failure         *PaperFailure   `json:"failure"`
	State           string          `json:"state"`
	Attempted       bool            `json:"attempted"`
}

type PaperRepairSummary struct {
	Stage     string `json:"stage,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Used      int    `json:"used"`
	Limit     int    `json:"limit"`
	Field     string `json:"field"`
	State     string `json:"state"`
	Attempted bool   `json:"attempted"`
}

type PaperCheckpoint struct {
	ExtractionFallback  map[string]bool             `json:"extraction_fallback,omitempty"`
	ReviewOmitted       []string                    `json:"review_omitted,omitempty"`
	Limits              generation.ModelLimits      `json:"limits"`
	CallTimeout         time.Duration               `json:"call_timeout"`
	BudgetVersion       string                      `json:"budget_version,omitempty"`
	Repairs             map[string]*PaperRepair     `json:"repairs,omitempty"`
	StageFailures       map[string]*PaperFailure    `json:"stage_failures,omitempty"`
	Issues              []PaperIssue                `json:"issues,omitempty"`
	ReportInputs        map[string]PaperAnswerInput `json:"report_inputs,omitempty"`
	Coverage            string                      `json:"coverage,omitempty"`
	StructuredCaptured  bool                        `json:"structured_captured,omitempty"`
	StructuredEvidence  []Citation                  `json:"structured_evidence,omitempty"`
	StructuredGap       string                      `json:"structured_gap,omitempty"`
	QA                  *PaperQACheckpoint          `json:"qa,omitempty"`
	CurrentStage        string                      `json:"current_stage,omitempty"`
	Repair              *PaperRepair                `json:"repair,omitempty"`
	TerminalFailure     string                      `json:"terminal_failure,omitempty"`
	ReviewPlan          []PaperReviewBatch          `json:"review_plan,omitempty"`
	ContextCaptured     bool                        `json:"context_captured"`
	ConversationContext PaperConversationContext    `json:"conversation_context"`
	PreparationDeadline *time.Time                  `json:"preparation_deadline,omitempty"`
	Failure             *PaperFailure               `json:"failure,omitempty"`
	Sections            json.RawMessage             `json:"sections,omitempty"`
	Context             PaperContext                `json:"context"`
	PaperHash           string                      `json:"paper_hash"`
	Mode                string                      `json:"mode"`
	FallbackReason      string                      `json:"fallback_reason,omitempty"`
	SourceVersion       string                      `json:"source_version,omitempty"`
	ContentHash         string                      `json:"content_hash,omitempty"`
	BatchTotal          int                         `json:"batch_total"`
	BatchCompleted      int                         `json:"batch_completed"`
	Outputs             map[string]json.RawMessage  `json:"outputs"`
}

type EvidenceRef struct {
	ID    string `json:"id"`
	Quote string `json:"quote"`
}
type PaperClaim struct {
	Text     string        `json:"text"`
	Evidence []EvidenceRef `json:"evidence"`
}
type FieldAnalysis struct {
	Status string       `json:"status"`
	Claims []PaperClaim `json:"claims"`
}
type PaperReport struct {
	Problem     string `json:"problem"`
	Method      string `json:"method"`
	Experiments string `json:"experiments"`
	Results     string `json:"results"`
	Limitations string `json:"limitations"`
}
type PaperFieldResult struct {
	GapReason   string   `json:"gap_reason,omitempty"`
	Status      string   `json:"status"`
	CitationIDs []string `json:"citation_ids"`
}
type PaperResult struct {
	Outcome          string                      `json:"outcome,omitempty"`
	Issues           []PaperIssue                `json:"issues,omitempty"`
	Reproduction     *PaperReproduction          `json:"reproduction,omitempty"`
	OriginalQuestion string                      `json:"original_question,omitempty"`
	PaperTitle       string                      `json:"paper_title,omitempty"`
	StructuredGap    string                      `json:"structured_gap,omitempty"`
	Answer           *PaperAnswer                `json:"answer,omitempty"`
	Report           *PaperReport                `json:"report,omitempty"`
	Fields           map[string]PaperFieldResult `json:"fields"`
	ContextMode      string                      `json:"context_mode"`
	FallbackReason   string                      `json:"fallback_reason,omitempty"`
	DocumentID       string                      `json:"document_id,omitempty"`
	SourceVersion    string                      `json:"source_version,omitempty"`
	ContentHash      string                      `json:"content_hash,omitempty"`
	PaperHash        string                      `json:"paper_hash"`
	WorkflowVersion  string                      `json:"workflow_version"`
	Coverage         string                      `json:"coverage"`
}

// Only server-defined identifiers belong in public issues. Candidates and
// evidence snapshots are retained exclusively in the private checkpoint.
type PaperIssue struct {
	Stage       string   `json:"stage"`
	Code        string   `json:"code"`
	Field       string   `json:"field,omitempty"`
	QuestionIDs []string `json:"question_ids,omitempty"`
}

func validPaperReport(message Message, hash string) (PaperResult, bool) {
	var result PaperResult
	err := json.Unmarshal(message.Result, &result)
	return result, err == nil && result.Report != nil && result.PaperHash == hash && (result.WorkflowVersion == PaperWorkflowVersion || result.WorkflowVersion == "paper-fixed-v1" || result.WorkflowVersion == "paper-fixed-v2" || result.WorkflowVersion == "paper-fixed-v3" || result.WorkflowVersion == "paper-fixed-v4" || result.WorkflowVersion == "paper-fixed-v5" || result.WorkflowVersion == "paper-fixed-v6" || result.WorkflowVersion == "paper-fixed-v7" || result.WorkflowVersion == "paper-fixed-v8" || result.WorkflowVersion == "paper-fixed-v9" || result.WorkflowVersion == "paper-fixed-v10" || result.WorkflowVersion == "paper-fixed-v11" || result.WorkflowVersion == "paper-fixed-v12" || result.WorkflowVersion == "paper-fixed-v13") && (result.ContextMode == "abstract" || result.ContextMode == "fulltext")
}
