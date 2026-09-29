package bootstrap

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"signalwatch/internal/agent"
	"signalwatch/internal/ai"
	"signalwatch/internal/document"
	"signalwatch/internal/generation"
	"signalwatch/internal/paper"
	pdf "signalwatch/internal/platform/document"
	"signalwatch/internal/source"
	"signalwatch/internal/subscription"
	"time"
)

func OpenAgent(database *gorm.DB, service *ai.Service, limiter pdf.Limiter) (*agent.Service, *document.Service) {
	if service.Configurations() == nil {
		return nil, nil
	}
	documents := document.NewMySQLStore(database)
	sources := source.NewService(source.NewRepository(database))
	a := agent.New(agent.Dependencies{Store: agent.NewMySQLStore(database), Gateway: agentGateway{service.Configurations()}, Papers: paper.NewQueryService(paper.NewQueryRepository(database)), Sources: sources, Subscriptions: subscription.NewService(subscription.NewRepository(database), sources, service.Configurations()), Documents: documents, Structured: document.NewStructuredService(documents, pdf.New(limiter))})
	return a, &document.Service{Store: documents, Extractor: pdf.New(limiter)}
}

type agentGateway struct{ configuration *ai.ConfigurationService }

func (g agentGateway) Selection(ctx context.Context, u uint64, p, m, id string) (agent.Selection, error) {
	c, err := g.configuration.SelectionForCredential(ctx, u, id, p, m)
	if err != nil {
		return agent.Selection{}, agentError(err)
	}
	return agent.Selection{Generation: c.Generation, Version: c.Version, Limits: g.configuration.ModelLimits(p, m), CallTimeout: g.configuration.PaperCallTimeout()}, nil
}
func (g agentGateway) Generate(ctx context.Context, r agent.ModelRequest) (generation.Result, error) {
	value, err := g.configuration.GenerateForCredentialLimit(ctx, r.Run.UserID, r.Run.Provider, r.Run.Model, r.Run.Generation, r.Run.Version, r.Feature, r.Run.ID, r.System, r.Input, r.MaxTokens, r.Before, func(value generation.Result) error {
		return agentValidationError(r.Validate(value))
	}, r.Schema)
	if err != nil {
		return value, agentError(err)
	}
	return value, nil
}
func agentValidationError(err error) error {
	if err == nil {
		return nil
	}
	var output *generation.OutputError
	if errors.As(err, &output) && generation.IsOutputFailure(output.Code) {
		return &generation.Failure{Code: output.Code, ValidationPath: output.Path, ValidationRule: output.Rule}
	}
	return &generation.Failure{Code: "invalid_output"}
}

func agentError(err error) error {
	// Before callbacks run after admission but before the provider call. Preserve
	// workflow ownership, access and budget failures so they keep their meaning
	// when the paper workflow chooses its final failure and repair eligibility.
	for _, sentinel := range []error{agent.ErrLease, agent.ErrConflict, agent.ErrNotFound, agent.ErrBudget, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	var model *agent.ModelError
	if errors.As(err, &model) {
		return err
	}
	var call *ai.CallError
	if errors.As(err, &call) {
		e := &agent.ModelError{Code: call.Code, Admission: call.CallID == "" && (call.Code == "rate_limited" || call.Code == "call_in_progress")}
		if call.RetryAt != nil {
			e.RetryAfter = time.Until(*call.RetryAt)
		}
		return e
	}
	code := "AI_UNAVAILABLE"
	switch {
	case errors.Is(err, ai.ErrConfigurationRequired):
		code = "AI_CONFIGURATION_REQUIRED"
	case errors.Is(err, ai.ErrConfigurationInvalid):
		code = "AI_CONFIGURATION_INVALID"
	case errors.Is(err, ai.ErrModelUnavailable), errors.Is(err, ai.ErrProviderDisabled):
		code = "AI_MODEL_UNAVAILABLE"
	case errors.Is(err, ai.ErrConfigurationConflict):
		code = "AI_CONFIGURATION_VERSION_CONFLICT"
	}
	return &agent.ModelError{Code: code}
}
