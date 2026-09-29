package agent

import (
	"fmt"
	"signalwatch/internal/generation"
)

const paperResponseLimit = 50000
const paperExtractionEvidenceLimit = 4

// Policies are returned by value. Shared schemas are constructed once at startup
// and never modified while handling requests.
type paperFieldPolicy struct {
	MaxClaims        int `json:"max_claims"`
	MaxTextBytes     int `json:"max_text_bytes"`
	MinEvidence      int `json:"min_evidence"`
	MaxEvidence      int `json:"max_evidence"`
	MaxResponseBytes int `json:"max_response_bytes"`
}

func paperFieldPolicyFor(field string) paperFieldPolicy {
	maxClaims := 6
	switch field {
	case "reproduction":
		maxClaims = 12
	case "experiments", "results":
		maxClaims = 8
	case "problem", "method", "limitations", "answer":
	default:
		panic("unregistered paper field")
	}
	return paperFieldPolicy{MaxClaims: maxClaims, MaxTextBytes: 1200, MinEvidence: 1, MaxEvidence: 3, MaxResponseBytes: paperResponseLimit}
}

func fieldPromptFor(field string, report bool) string {
	p := paperFieldPolicyFor(field)
	prompt := fmt.Sprintf(`Analyze ONLY the requested field.
Return the field analysis object specified by the output JSON Schema.
Use 1-%d claims when supported, each at most %d UTF-8 bytes after JSON decoding, and %d-%d evidence passage IDs from this input. The complete JSON response must not exceed %d UTF-8 bytes. Otherwise claims must be []. not_stated means the supplied paper material explicitly lacks the requested information; insufficient_evidence means the current evidence cannot establish it. Do not claim something is absent from the full paper in abstract or retrieved context. Preserve comparison conditions and uncertainty.`, p.MaxClaims, p.MaxTextBytes, p.MinEvidence, p.MaxEvidence, p.MaxResponseBytes)
	if report {
		return prompt + reportLanguagePrompt
	}
	return prompt + followupLanguagePrompt
}

type paperFieldSchemas struct {
	full      *generation.Schema
	structure *generation.Schema
}

var paperSchemasByField = func() map[string]paperFieldSchemas {
	out := make(map[string]paperFieldSchemas, 6)
	for _, field := range []string{"problem", "method", "experiments", "results", "limitations", "answer"} {
		p := paperFieldPolicyFor(field)
		schema := generation.SchemaFor[fieldOutput]()
		claims := schema.Properties["claims"]
		zero := 0
		claims.MinItems, claims.MaxItems = &zero, &p.MaxClaims
		evidence := claims.Items.Properties["evidence"]
		evidence.MinItems, evidence.MaxItems = &p.MinEvidence, &p.MaxEvidence
		out[field] = paperFieldSchemas{full: schema, structure: schema.Structural()}
	}
	return out
}()

func paperSchemasFor(field string) paperFieldSchemas {
	schemas, ok := paperSchemasByField[field]
	if !ok {
		panic("unregistered paper field")
	}
	return schemas
}
