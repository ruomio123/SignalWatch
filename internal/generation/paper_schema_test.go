package generation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type boundedSchemaField struct {
	Status string               `json:"status" enum:"supported,not_stated,insufficient_evidence"`
	Claims []boundedSchemaClaim `json:"claims"`
}
type boundedSchemaClaim struct {
	Text     string                  `json:"text"`
	Evidence []boundedSchemaEvidence `json:"evidence"`
}
type boundedSchemaEvidence struct {
	Quote string `json:"quote"`
}

func TestArrayBoundsAndProviderStructuralProjection(t *testing.T) {
	schema := SchemaFor[boundedSchemaField]()
	minimum, maximum, evidenceMax := 0, 2, 1
	schema.Properties["claims"].MinItems = &minimum
	schema.Properties["claims"].MaxItems = &maximum
	schema.Properties["claims"].Items.Properties["evidence"].MaxItems = &evidenceMax
	projection := schema.Structural()
	raw, _ := json.Marshal(projection)
	if strings.Contains(string(raw), "minItems") || strings.Contains(string(raw), "maxItems") || !strings.Contains(string(raw), `"additionalProperties":false`) {
		t.Fatalf("invalid provider projection: %s", raw)
	}
	excess := []byte(`{"status":"supported","claims":[{"text":"one","evidence":[]},{"text":"two","evidence":[]},{"text":"three","evidence":[]}]}`)
	var failure *OutputError
	if err := schema.Validate(excess); !errors.As(err, &failure) || failure.Code != "output_limit_exceeded" || failure.Path != "$.claims" || failure.Count == nil || *failure.Count != 3 || failure.Limit == nil || *failure.Limit != 2 {
		t.Fatalf("missing array diagnostic: %+v", err)
	}
	if err := projection.Validate(excess); err != nil {
		t.Fatal(err)
	}
	// Projection consumers cannot alter any nested metadata in the source schema.
	projection.Properties["status"].Enum[0] = "changed"
	projection.Required[0] = "changed"
	*projection.AdditionalProperties = true
	projection.Properties["claims"].Items.Properties["text"].Type = "boolean"
	if schema.Properties["status"].Enum[0] != "supported" || schema.Required[0] != "status" || *schema.AdditionalProperties || schema.Properties["claims"].Items.Properties["text"].Type != "string" || *schema.Properties["claims"].MaxItems != maximum {
		t.Fatal("projection mutated the complete schema")
	}
	// Structural failures within an excess array remain identifiable.
	bad := []byte(`{"status":"supported","claims":[{"text":"one","evidence":[]},{"text":"two","evidence":[]},{"text":true,"evidence":[]}]}`)
	if err := schema.Validate(bad); !errors.As(err, &failure) || failure.Rule != "expected_string" {
		t.Fatalf("%+v", err)
	}
}
