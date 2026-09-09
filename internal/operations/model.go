package operations

import "time"

type Failure struct {
	Stage   string    `json:"stage"`
	Summary string    `json:"summary"`
	At      time.Time `json:"at"`
}

type QueueSnapshot struct {
	Depth      int    `json:"depth"`
	Capacity   int    `json:"capacity"`
	Workers    int    `json:"workers"`
	Processing int64  `json:"processing"`
	Succeeded  uint64 `json:"succeeded"`
	Failed     uint64 `json:"failed"`
}

type WorkerSnapshot struct {
	InstanceID        string        `json:"instance_id"`
	State             string        `json:"state"`
	StartedAt         time.Time     `json:"started_at"`
	LastHeartbeatAt   time.Time     `json:"last_heartbeat_at"`
	HeartbeatInterval time.Duration `json:"-"`
	HeartbeatSeconds  int64         `json:"heartbeat_interval_seconds"`
	MatcherQueue      QueueSnapshot `json:"matcher_queue"`
	MailQueue         QueueSnapshot `json:"mail_queue"`
	OnlineState       string        `json:"online_state,omitempty"`
}

type TaskSnapshot struct {
	Task          string         `json:"task"`
	InstanceID    string         `json:"worker_instance_id,omitempty"`
	State         string         `json:"state"`
	Trigger       string         `json:"trigger,omitempty"`
	Mode          string         `json:"mode,omitempty"`
	AttemptID     string         `json:"attempt_id,omitempty"`
	DueAt         *time.Time     `json:"due_at,omitempty"`
	StartedAt     *time.Time     `json:"started_at,omitempty"`
	UpdatedAt     time.Time      `json:"updated_at"`
	LastSuccessAt *time.Time     `json:"last_success_at,omitempty"`
	NextRetryAt   *time.Time     `json:"next_retry_at,omitempty"`
	Metrics       map[string]int `json:"metrics,omitempty"`
	Failure       *Failure       `json:"failure,omitempty"`
}

type SourceAttempt struct {
	SourceID      uint64         `json:"source_id"`
	State         string         `json:"state"`
	Mode          string         `json:"mode"`
	AttemptID     string         `json:"attempt_id"`
	WindowFrom    *time.Time     `json:"window_from,omitempty"`
	WindowTo      *time.Time     `json:"window_to,omitempty"`
	StartedAt     time.Time      `json:"started_at"`
	CompletedAt   *time.Time     `json:"completed_at,omitempty"`
	LastSuccessAt *time.Time     `json:"last_success_at,omitempty"`
	Metrics       map[string]int `json:"metrics,omitempty"`
	Failure       *Failure       `json:"failure,omitempty"`
}

type DependencyStatus struct {
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
}

type StatusResponse struct {
	GeneratedAt  time.Time                   `json:"generated_at"`
	Status       string                      `json:"status"`
	Dependencies map[string]DependencyStatus `json:"dependencies"`
	Workers      []WorkerSnapshot            `json:"workers"`
	Tasks        []TaskSnapshot              `json:"tasks"`
	Sources      SourceSummary               `json:"sources"`
}

type SourceSummary struct {
	Enabled     int64 `json:"enabled"`
	Current     int64 `json:"current"`
	Stale       int64 `json:"stale"`
	NeverSynced int64 `json:"never_synced"`
}

type SourceStatus struct {
	ID                   uint64         `json:"id"`
	SourceKey            string         `json:"source_key"`
	Name                 string         `json:"name"`
	Enabled              bool           `json:"enabled"`
	AllowedCategories    []string       `json:"allowed_categories"`
	PaperCount           int64          `json:"paper_count"`
	LatestPaperAt        *time.Time     `json:"latest_paper_at,omitempty"`
	LastSuccessfulSyncAt *time.Time     `json:"last_successful_sync_at,omitempty"`
	CheckpointState      string         `json:"checkpoint_state"`
	OperationalState     string         `json:"operational_state"`
	LatestAttempt        *SourceAttempt `json:"latest_attempt,omitempty"`
}

type SourcesResponse struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Items       []SourceStatus `json:"items"`
}
