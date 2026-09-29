package generation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPaperBudgetIncludesSchemaOutputAndUTF8Escaping(t *testing.T) {
	limits := DefaultModelLimits()
	schema := SchemaFor[struct {
		Answer string `json:"answer"`
	}]()
	input, _ := json.Marshal(map[string]string{"question": "中文🙂<>&\\\""})
	system := "Analyze the paper."
	native, _ := json.Marshal(schema.Structural())
	want := len(system) + len(input) + len(schema.Instructions()) + len(native) + 256
	if got := EstimateRequestTokens(system, input, schema); got != want {
		t.Fatalf("estimate=%d, want=%d", got, want)
	}
	room := limits.ContextTokens - max(1024, limits.ContextTokens/20) - 8192 - EstimateRequestTokens(system, nil, schema)
	if !RequestFits(limits, system, []byte(strings.Repeat("x", room)), schema, 8192) || RequestFits(limits, system, []byte(strings.Repeat("x", room+1)), schema, 8192) {
		t.Fatal("request budget must accept the exact boundary and reject one extra byte")
	}
	if RequestFits(limits, system, input, schema, 8193) || RequestFits(ModelLimits{}, system, input, schema, 4096) {
		t.Fatal("invalid limits or output reservation accepted")
	}
}

func TestPaperBudgetModelOverridesChangeAvailableInput(t *testing.T) {
	input := []byte(strings.Repeat("证据", 4000))
	if RequestFits(DefaultModelLimits(), "Analyze", input, nil, 8192) {
		t.Fatal("default limit did not reserve output space")
	}
	if !RequestFits(ModelLimits{ContextTokens: 65536, MaxOutputTokens: 8192}, "Analyze", input, nil, 8192) {
		t.Fatal("configured larger context was not used")
	}
	for _, limits := range []ModelLimits{{4096, 4096}, {32768, 8193}, {32768, 0}, {1 << 22, 8192}} {
		if limits.Valid() {
			t.Fatalf("invalid model limits accepted: %+v", limits)
		}
	}
}
