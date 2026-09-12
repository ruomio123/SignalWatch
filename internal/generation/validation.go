package generation

// OutputError contains only server-defined rules and paths, never model text.
// It is shared across the workflow, metering and provider-independent gateway.
type OutputError struct {
	Code string
	Path string
	Rule string
}

func (e *OutputError) Error() string { return e.Code }
func IsOutputFailure(code string) bool {
	switch code {
	case "output_language_mismatch", "output_invalid_json", "output_schema_mismatch", "output_limit_exceeded", "evidence_id_unknown", "evidence_quote_mismatch", "evidence_quote_length", "review_incomplete":
		return true
	}
	return false
}
