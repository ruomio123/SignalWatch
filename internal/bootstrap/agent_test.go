package bootstrap

import (
	"errors"
	"fmt"
	"signalwatch/internal/agent"
	"signalwatch/internal/ai"
	"signalwatch/internal/generation"
	"testing"
)

func TestAgentValidationPreservesSafeRuleAndPath(t *testing.T) {
	source := fmt.Errorf("%w: %w", agent.ErrOutput, &generation.OutputError{Code: "evidence_quote_mismatch", Path: "$.method[0].quote"})
	failure, ok := agentValidationError(source).(*generation.Failure)
	if !ok || failure.Code != "evidence_quote_mismatch" || failure.ValidationPath != "$.method[0].quote" {
		t.Fatal(failure)
	}
	mapped := agentError(&ai.CallError{Code: failure.Code, CallID: "fixture-call"})
	var model *agent.ModelError
	if !errors.As(mapped, &model) || model.Code != failure.Code {
		t.Fatal(mapped)
	}
	legacy := agentValidationError(errors.New("private model output"))
	if legacy.Error() != "invalid_output" || agentValidationError(nil) != nil {
		t.Fatal("unsafe fallback")
	}
}
