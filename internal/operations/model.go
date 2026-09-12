package operations

import "time"

type Failure struct {
	Stage   string    `json:"stage"`
	Summary string    `json:"summary"`
	At      time.Time `json:"at"`
}

type QueueSnapshot struct {
	Skipped    uint64 `json:"skipped"`
	Locked     uint64 `json:"lease_conflicts"`
	Retried    uint64 `json:"retried"`
	Depth      int    `json:"depth"`
	Capacity   int    `json:"capacity"`
	Workers    int    `json:"workers"`
	Processing int64  `json:"processing"`
	Succeeded  uint64 `json:"succeeded"`
	Failed     uint64 `json:"failed"`
}

type WorkerSnapshot struct {
	InstanceID       string        `json:"instance_id"`
	State            string        `json:"state"`
	StartedAt        time.Time     `json:"started_at"`
	LastHeartbeatAt  time.Time     `json:"last_heartbeat_at"`
	HeartbeatSeconds int64         `json:"heartbeat_interval_seconds"`
	MatcherQueue     QueueSnapshot `json:"matcher_queue"`
	MailQueue        QueueSnapshot `json:"mail_queue"`
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
