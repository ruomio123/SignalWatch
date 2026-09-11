// Package generation defines the provider-independent generation contract.
package generation

import "time"

type Result struct {
	Content      []byte
	InputTokens  int64
	OutputTokens int64
	UsageKnown   bool
}
type Failure struct {
	Code              string
	ProviderCode      string
	ProviderRequestID string
	HTTPStatus        int
	RetryAfter        time.Duration
	Retryable         bool
}

func (e *Failure) Error() string { return e.Code }

type Model struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

type Provider struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Models []Model `json:"models"`
}

const (
	UnavailableProviderDisabled = "provider_disabled"
	UnavailableModelRetired     = "model_retired"
)
