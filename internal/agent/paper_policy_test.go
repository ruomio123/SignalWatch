package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"signalwatch/internal/generation"
	"strings"
	"testing"
)

func policyClaims(count int, text string) fieldOutput {
	wire := fieldOutput{Status: "supported", Claims: make([]claimOutput, count)}
	for i := range wire.Claims {
		wire.Claims[i] = claimOutput{Text: text, Evidence: []evidenceOutput{{ID: "source"}}}
	}
	return wire
}

func TestPaperFieldPoliciesBoundEveryFieldAndKeepSchemaImmutable(t *testing.T) {
	evidence := []Citation{{ID: "source", Quote: "The experiment found a result."}}
	for _, field := range []string{"problem", "method", "experiments", "results", "limitations", "answer"} {
		t.Run(field, func(t *testing.T) {
			want := 6
			if field == "experiments" || field == "results" {
				want = 8
			}
			policy := paperFieldPolicyFor(field)
			if policy.MaxClaims != want {
				t.Fatalf("policy: %+v", policy)
			}
			for _, count := range []int{5, want, want + 1} {
				raw, _ := json.Marshal(policyClaims(count, "该实验结果受材料支持。"))
				got, err := decodeFieldFor(raw, evidence, field, field != "answer")
				if count <= want {
					if err != nil || len(got.Claims) != count {
						t.Fatalf("count %d: %+v %v", count, got, err)
					}
				} else {
					assertPaperLimit(t, err, "$.claims", count, want, "claims", true)
				}
			}
			schema := paperStageSchema("analyzing_" + field)
			if schema != paperStageSchema("analyzing_"+field) || schema != paperStageSchema("repairing_"+field) {
				t.Fatal("schema rebuilt per request")
			}
			before, _ := json.Marshal(schema)
			if schema.Properties["claims"].MaxItems == nil || *schema.Properties["claims"].MaxItems != want {
				t.Fatal("schema missing claim bound")
			}
			refs := schema.Properties["claims"].Items.Properties["evidence"]
			if refs.MinItems == nil || *refs.MinItems != 1 || refs.MaxItems == nil || *refs.MaxItems != 3 {
				t.Fatal("schema missing evidence bounds")
			}
			if strings.Contains(string(before), "maxLength") {
				t.Fatal("schema character count used for UTF-8 byte limit")
			}
			prompt := fieldPromptFor(field, field != "answer")
			if !strings.Contains(prompt, fmt.Sprintf("1-%d claims", want)) || !strings.Contains(prompt, "1200 UTF-8 bytes") || !strings.Contains(prompt, "50000 UTF-8 bytes") {
				t.Fatal(prompt)
			}
			_ = paperStageSchema("analyzing_results").Structural()
			after, _ := json.Marshal(schema)
			if string(before) != string(after) {
				t.Fatal("shared schema mutated")
			}
		})
	}
}

func assertPaperLimit(t *testing.T, err error, path string, count, limit int, unit string, repairable bool) {
	t.Helper()
	var failure *generation.OutputError
	if !errors.Is(err, ErrOutput) || !errors.As(err, &failure) || failure.Code != "output_limit_exceeded" || failure.Path != path || failure.Count == nil || *failure.Count != count || failure.Limit == nil || *failure.Limit != limit || failure.Unit != unit || repairablePaperLimit(err) != repairable {
		t.Fatalf("invalid limit detail: %+v, repairable=%t, err=%v", failure, repairablePaperLimit(err), err)
	}
}

func TestPaperTextUTF8ByteBoundariesIncludeDecodedEscapes(t *testing.T) {
	evidence := []Citation{{ID: "source", Quote: "source text"}}
	for _, tc := range []struct{ name, text string }{
		{"english", strings.Repeat("a", 1200)},
		{"chinese", strings.Repeat("中", 400)},
		{"emoji", strings.Repeat("😀", 300)},
		{"json escapes", strings.Repeat("\"\\\n", 400)},
		{"mixed", strings.Repeat("中a😀", 150)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, extra := range []string{"", "a"} {
				raw, _ := json.Marshal(policyClaims(1, tc.text+extra))
				got, err := decodeFieldFor(raw, evidence, "answer", false)
				if extra == "" {
					if err != nil || got.Claims[0].Text != tc.text {
						t.Fatalf("%+v %v", got, err)
					}
				} else {
					assertPaperLimit(t, err, "$.claims[0].text", 1201, 1200, "utf8_bytes", true)
				}
			}
		})
	}
	// Six ASCII escape bytes represent three UTF-8 bytes after JSON decoding.
	raw := []byte(`{"status":"supported","claims":[{"text":"` + strings.Repeat(`\u4e2d`, 400) + `","evidence":[{"id":"source"}]}]}`)
	if _, err := decodeFieldFor(raw, evidence, "results", true); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "a"} {
		raw := []byte(`{"status":"supported","claims":[{"text":"` + strings.Repeat(`\ud83d\ude00`, 300) + suffix + `","evidence":[{"id":"source"}]}]}`)
		_, err := decodeFieldFor(raw, evidence, "answer", false)
		if suffix == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			assertPaperLimit(t, err, "$.claims[0].text", 1201, 1200, "utf8_bytes", true)
		}
	}
}

func TestPaperFieldEvidenceCountBoundaries(t *testing.T) {
	evidence := []Citation{{ID: "source"}, {ID: "second"}, {ID: "third"}, {ID: "fourth"}}
	for _, count := range []int{1, 3, 4} {
		wire := policyClaims(1, "有证据支持的结论。")
		wire.Claims[0].Evidence = nil
		for _, ref := range evidence[:count] {
			wire.Claims[0].Evidence = append(wire.Claims[0].Evidence, evidenceOutput{ID: ref.ID})
		}
		raw, _ := json.Marshal(wire)
		got, err := decodeFieldFor(raw, evidence, "results", true)
		if count <= 3 {
			if err != nil || len(got.Claims[0].Evidence) != count {
				t.Fatalf("%+v %v", got, err)
			}
		} else {
			assertPaperLimit(t, err, "$.claims[0].evidence", count, 3, "evidence", false)
		}
	}
}

func TestPaperRepairEligibilityDoesNotHideLaterInvalidContent(t *testing.T) {
	evidence := []Citation{{ID: "source", Quote: "source"}, {ID: "second"}, {ID: "third"}, {ID: "fourth"}}
	for _, tc := range []struct {
		name   string
		mutate func(*fieldOutput)
		code   string
	}{
		{"unknown reference", func(w *fieldOutput) { w.Claims[8].Evidence[0].ID = "unknown" }, "evidence_id_unknown"},
		{"empty reference", func(w *fieldOutput) { w.Claims[8].Evidence[0].ID = "" }, "evidence_id_unknown"},
		{"duplicate reference", func(w *fieldOutput) { w.Claims[8].Evidence = []evidenceOutput{{ID: "source"}, {ID: "source"}} }, "output_schema_mismatch"},
		{"empty text", func(w *fieldOutput) { w.Claims[8].Text = " " }, "output_schema_mismatch"},
		{"missing evidence", func(w *fieldOutput) { w.Claims[8].Evidence = []evidenceOutput{} }, "output_schema_mismatch"},
		{"excess evidence", func(w *fieldOutput) {
			w.Claims[8].Evidence = []evidenceOutput{{ID: "source"}, {ID: "second"}, {ID: "third"}, {ID: "fourth"}}
		}, "output_limit_exceeded"},
		{"wrong report language", func(w *fieldOutput) { w.Claims[8].Text = "This is only an English explanation." }, "output_language_mismatch"},
		{"status relation", func(w *fieldOutput) { w.Status = "not_stated" }, "output_schema_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := policyClaims(9, "中文结论")
			wire.Claims[0].Text = strings.Repeat("中", 401)
			tc.mutate(&wire)
			raw, _ := json.Marshal(wire)
			_, err := decodeFieldFor(raw, evidence, "results", true)
			var failure *generation.OutputError
			if !errors.As(err, &failure) || failure.Code != tc.code || repairablePaperLimit(err) {
				t.Fatalf("%+v %v", failure, err)
			}
		})
	}
	valid, _ := json.Marshal(policyClaims(9, "中文结论"))
	for _, raw := range [][]byte{
		[]byte(strings.Replace(string(valid), `"text":"中文结论"`, `"text":true`, 1)),
		[]byte(strings.Replace(string(valid), `"status":"supported"`, `"status":"supported","status":"supported"`, 1)),
		[]byte(strings.Replace(string(valid), `"evidence":`, `"unexpected":true,"evidence":`, 1)),
		append(valid, []byte(` {}`)...),
	} {
		if _, err := decodeFieldFor(raw, evidence, "results", true); err == nil || repairablePaperLimit(err) {
			t.Fatalf("accepted repairable malformed output: %v", err)
		}
	}
}

func TestPaperCompleteResponseByteLimitIsNotRepairable(t *testing.T) {
	evidence := []Citation{{ID: "source", Quote: "source"}}
	raw, _ := json.Marshal(policyClaims(1, "中文"))
	for _, size := range []int{paperResponseLimit, paperResponseLimit + 1} {
		padded := append(append([]byte(nil), raw...), []byte(strings.Repeat(" ", size-len(raw)))...)
		_, err := decodeFieldFor(padded, evidence, "results", true)
		if size == paperResponseLimit {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			assertPaperLimit(t, err, "$", size, paperResponseLimit, "utf8_bytes", false)
		}
	}
}
