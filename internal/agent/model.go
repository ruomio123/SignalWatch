// Package agent implements bounded, persistent tool-using conversations.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"signalwatch/internal/document"
	"signalwatch/internal/generation"
	"signalwatch/internal/paper"
	"signalwatch/internal/source"
	"signalwatch/internal/subscription"
	"time"
)

var (
	ErrNotFound       = errors.New("AGENT_NOT_FOUND")
	ErrInput          = errors.New("AGENT_INVALID_INPUT")
	ErrConflict       = errors.New("AGENT_CONFLICT")
	ErrLease          = errors.New("AGENT_LEASE_LOST")
	ErrBudget         = errors.New("AGENT_BUDGET_EXHAUSTED")
	ErrOutput         = errors.New("AGENT_INVALID_OUTPUT")
	ErrReportRequired = errors.New("PAPER_REPORT_REQUIRED")
)

type Conversation struct {
	PaperReportReady bool      `json:"paper_report_ready" gorm:"-"`
	ID               string    `json:"id"`
	UserID           uint64    `json:"-"`
	Kind             string    `json:"kind"`
	PaperID          *uint64   `json:"paper_id,omitempty"`
	Title            string    `json:"title"`
	ActiveRunID      *string   `json:"active_run_id,omitempty"`
	LatestDraftID    string    `json:"latest_draft_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (Conversation) TableName() string { return "agent_conversations" }

type Run struct {
	ReviewProgress       *PaperReviewProgress `json:"review_progress,omitempty" gorm:"-"`
	FailureDetail        *PaperFailure        `json:"failure_detail,omitempty" gorm:"-"`
	Task                 string               `json:"task,omitempty"`
	WorkflowVersion      string               `json:"workflow_version,omitempty"`
	EffectiveContextMode string               `json:"effective_context_mode,omitempty"`
	FallbackReason       string               `json:"fallback_reason,omitempty"`
	BatchTotal           int                  `json:"batch_total"`
	BatchCompleted       int                  `json:"batch_completed"`
	ID                   string               `json:"id"`
	ConversationID       string               `json:"conversation_id"`
	UserID               uint64               `json:"-"`
	IdempotencyKey       string               `json:"-"`
	InputHash            string               `json:"-"`
	Question             string               `json:"-"`
	Provider             string               `json:"provider"`
	Model                string               `json:"model"`
	Generation           string               `json:"-"`
	Version              uint64               `json:"-"`
	ContextMode          string               `json:"context_mode"`
	State                string               `json:"state"`
	Progress             string               `json:"progress"`
	FailureCode          string               `json:"failure_code,omitempty"`
	Checkpoint           json.RawMessage      `json:"-"`
	LeaseOwner           string               `json:"-"`
	Epoch                uint64               `json:"-"`
	LeaseUntil           *time.Time           `json:"-"`
	Deadline             *time.Time           `json:"deadline,omitempty"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
}

func (Run) TableName() string { return "agent_runs" }

type Citation struct {
	ID          string `json:"id"`
	DocumentID  string `json:"document_id"`
	ContentHash string `json:"content_hash"`
	Page        int    `json:"page"`
	Quote       string `json:"quote"`
	URL         string `json:"url"`
}
type Message struct {
	Result         json.RawMessage `json:"result,omitempty"`
	ID             uint64          `json:"id"`
	ConversationID string          `json:"conversation_id"`
	RunID          string          `json:"run_id"`
	Role           string          `json:"role"`
	Content        string          `json:"content"`
	Provider       string          `json:"provider"`
	Model          string          `json:"model"`
	Citations      json.RawMessage `json:"citations"`
	DraftID        string          `json:"draft_id,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

type PaperReportResponse struct {
	Report              *Message `json:"report"`
	MatchesCurrentPaper bool     `json:"matches_current_paper"`
}

func (Message) TableName() string { return "agent_messages" }

type Step struct {
	RunID        string    `json:"run_id"`
	Sequence     int       `json:"sequence"`
	Kind         string    `json:"kind"`
	Tool         string    `json:"tool,omitempty"`
	CallID       string    `json:"call_id,omitempty"`
	DurationMS   int64     `json:"duration_ms"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	FailureCode  string    `json:"failure_code,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func (Step) TableName() string { return "agent_steps" }

type SubmitInput struct {
	Task           string `json:"task,omitempty"`
	CredentialID   string `json:"credential_id,omitempty"`
	Question       string `json:"question"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	IdempotencyKey string `json:"idempotency_key"`
	ContextMode    string `json:"context_mode"`
}
type Selection struct {
	Generation string
	Version    uint64
}
type ModelRequest struct {
	Schema    *generation.Schema
	MaxTokens int
	Run       Run
	Feature   string
	System    string
	Input     []byte
	Before    func(context.Context) error
	Validate  func(generation.Result) error
}
type Gateway interface {
	Selection(context.Context, uint64, string, string, string) (Selection, error)
	Generate(context.Context, ModelRequest) (generation.Result, error)
}
type PaperReader interface {
	Get(context.Context, uint64, uint64) (paper.PublicPaper, error)
}
type SourceReader interface {
	List(context.Context) ([]source.PublicSource, error)
	Get(context.Context, uint64) (source.PublicSource, error)
}
type Subscriptions interface {
	ValidateDraft(context.Context, uint64, subscription.CreateInput) (subscription.CreateInput, error)
	ConfirmDraft(context.Context, uint64, string, uint32) (subscription.PublicSubscription, error)
}
type Store interface {
	Stats(context.Context) (map[string]int, error)
	EditDraft(context.Context, uint64, string, uint32, subscription.CreateInput) (subscription.Draft, error)
	CreateConversation(context.Context, Conversation) error
	Conversations(context.Context, uint64, string, *uint64, int) ([]Conversation, bool, error)
	Conversation(context.Context, uint64, string) (Conversation, error)
	DeleteConversation(context.Context, uint64, string) error
	Messages(context.Context, uint64, string, uint64) ([]Message, bool, error)
	History(context.Context, string) ([]Message, error)
	PaperHistory(context.Context, string, string, string) (PaperConversationContext, error)
	LatestPaperReport(context.Context, string) (Message, error)
	Submit(context.Context, Run, SubmitInput) (Run, error)
	RunByID(context.Context, uint64, string) (Run, error)
	Cancel(context.Context, uint64, string) error
	Claim(context.Context, string) (Run, error)
	Renew(context.Context, Run) error
	Check(context.Context, Run) error
	Save(context.Context, Run, Checkpoint, string, *Step) error
	Finish(context.Context, Run, string, string, *Message) error
	SaveDraft(context.Context, Run, subscription.CreateInput) (subscription.Draft, error)
	Draft(context.Context, uint64, string) (subscription.Draft, error)
	Preview(context.Context, uint64, subscription.RulesInput) (Preview, error)
	DigestLimit(context.Context, uint64) (uint16, error)
	Steps(context.Context, uint64, string) ([]Step, error)
}
type Preview struct {
	Count    int                 `json:"count"`
	Items    []paper.PublicPaper `json:"items"`
	From     time.Time           `json:"from"`
	To       time.Time           `json:"to"`
	Complete bool                `json:"complete"`
}
type Dependencies struct {
	Store         Store
	Gateway       Gateway
	Papers        PaperReader
	Sources       SourceReader
	Subscriptions Subscriptions
	Documents     document.Store
}
type Service struct {
	Dependencies
	paperPreparationTimeout time.Duration
}

func New(d Dependencies) *Service { return &Service{Dependencies: d} }

type Action struct {
	Type                 string          `json:"type"`
	Tool                 string          `json:"tool,omitempty"`
	Arguments            json.RawMessage `json:"arguments,omitempty"`
	Content              string          `json:"content,omitempty"`
	Citations            []Citation      `json:"citations,omitempty"`
	InsufficientEvidence bool            `json:"insufficient_evidence,omitempty"`
}
type Observation struct {
	Tool   string          `json:"tool"`
	Result json.RawMessage `json:"result"`
}
type Checkpoint struct {
	Paper        *PaperCheckpoint `json:"paper,omitempty"`
	Calls        int              `json:"calls"`
	Tools        int              `json:"tools"`
	Phase        string           `json:"phase"`
	Action       *Action          `json:"action,omitempty"`
	Observations []Observation    `json:"observations"`
	Evidence     []Citation       `json:"evidence"`
	DocumentID   string           `json:"document_id,omitempty"`
	DraftID      string           `json:"draft_id,omitempty"`
	Sequence     int              `json:"sequence"`
}
