package generation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type schemaClaim struct {
	Text     string        `json:"text"`
	Evidence []schemaQuote `json:"evidence"`
}
type schemaQuote struct {
	Quote string `json:"quote"`
}
type schemaField struct {
	Status string        `json:"status" enum:"supported,not_stated,insufficient_evidence"`
	Claims []schemaClaim `json:"claims"`
}

func TestSchemaReportsPreciseSafePaths(t *testing.T) {
	schema := SchemaFor[schemaField]()
	for _, tc := range []struct{ raw, path, rule string }{
		{`{"status":"supported","claims":[{"text":"method","evidence":"quote"}]}`, "$.claims[0].evidence", "expected_array"},
		{`{"status":"supported","claims":[{"text":"method","evidence":[{"quote":true}]}]}`, "$.claims[0].evidence[0].quote", "expected_string"},
		{`{"status":"supported","claims":[{"evidence":[]}]}`, "$.claims[0].text", "required_field"},
		{`{"status":"unsupported","claims":[]}`, "$.status", "invalid_enum"},
		{`{"status":"supported","claims":[{"text":"method","evidence":[{"quote":"paper text","private-api-key":1}]}]}`, "$.claims[0].evidence[0]", "unexpected_field"},
		{`{"Status":"supported","claims":[]}`, "$.status", "required_field"},
		{`{"status":"not_stated","claims":null}`, "$.claims", "expected_array"},
	} {
		var failure *OutputError
		err := schema.Validate([]byte(tc.raw))
		if !errors.As(err, &failure) || failure.Path != tc.path || failure.Rule != tc.rule {
			t.Fatalf("%s: %+v", tc.raw, failure)
		}
		if strings.Contains(failure.Error()+failure.Path+failure.Rule, "private-api-key") {
			t.Fatal("unknown field name leaked")
		}
	}
	if err := schema.Validate([]byte(`{"status":"supported","claims":[{"text":"method","evidence":[{"quote":"paper text"}]}]}`)); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(schema)
	if !strings.Contains(string(raw), `"additionalProperties":false`) || !strings.Contains(string(raw), `"enum":["supported","not_stated","insufficient_evidence"]`) {
		t.Fatal(string(raw))
	}
}
