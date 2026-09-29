package generation

import "encoding/json"

// ModelLimits are deployment-owned application budgets, not promises about a
// vendor's context window. Operators can set the limits of their selected model.
type ModelLimits struct {
	ContextTokens   int `json:"context_tokens"`
	MaxOutputTokens int `json:"max_output_tokens"`
}

const BudgetEstimatorVersion = "utf8-bytes-v1"

func DefaultModelLimits() ModelLimits {
	return ModelLimits{ContextTokens: 32768, MaxOutputTokens: 8192}
}

func (limits ModelLimits) Valid() bool {
	return limits.ContextTokens >= 4096 && limits.ContextTokens <= 2<<20 &&
		limits.MaxOutputTokens >= 1 && limits.MaxOutputTokens <= 8192 &&
		limits.MaxOutputTokens+max(1024, limits.ContextTokens/20)+256 < limits.ContextTokens
}

// EstimateRequestTokens deliberately counts one token per UTF-8 byte rather
// than assuming English character/token ratios. Include both the schema prompt
// and native structural schema: non-native providers are conservatively budgeted
// the same way. system is the business prompt before schema instructions.
func EstimateRequestTokens(system string, input []byte, schema *Schema) int {
	estimate := len(system) + len(input) + 256
	if schema != nil {
		native, _ := json.Marshal(schema.Structural())
		estimate += len(schema.Instructions()) + len(native)
	}
	return estimate
}

// RequestFits keeps an independent output reserve and safety margin. Byte-size
// limits on serialized business input must still be enforced by the caller.
func RequestFits(limits ModelLimits, system string, input []byte, schema *Schema, maxOutputTokens int) bool {
	if !limits.Valid() || maxOutputTokens < 1 || maxOutputTokens > limits.MaxOutputTokens {
		return false
	}
	return EstimateRequestTokens(system, input, schema)+maxOutputTokens+max(1024, limits.ContextTokens/20) <= limits.ContextTokens
}
