package config

import (
	"signalwatch/internal/generation"
	"testing"
	"time"
)

func TestPaperModelLimitsConfiguration(t *testing.T) {
	for _, empty := range []string{"", "  ", `{}`} {
		limits, err := parseModelLimits(empty)
		if err != nil || len(limits) != 0 {
			t.Fatalf("empty configuration=%v err=%v", limits, err)
		}
	}
	limits, err := parseModelLimits(`{"qwen/qwen3.8-flash":{"context_tokens":65536,"max_output_tokens":4096}}`)
	if err != nil || limits["qwen/qwen3.8-flash"] != (generation.ModelLimits{ContextTokens: 65536, MaxOutputTokens: 4096}) {
		t.Fatalf("override=%v err=%v", limits, err)
	}
	for _, invalid := range []string{
		`null`, `[]`, `{} {}`, `{"unknown/model":{"context_tokens":32768,"max_output_tokens":8192}}`,
		`{"qwen/qwen3.8-flash":{"context_tokens":32768,"max_output_tokens":8193}}`,
		`{"qwen/qwen3.8-flash":{"context_tokens":4096,"max_output_tokens":4096}}`,
		`{"qwen/qwen3.8-flash":{"context_tokens":32768}}`,
		`{"qwen/qwen3.8-flash":{"context_tokens":32768,"max_output_tokens":8192,"secret":"private-value"}}`,
	} {
		if _, err := parseModelLimits(invalid); err == nil || err.Error() != "invalid AI_MODEL_LIMITS_JSON" {
			t.Fatalf("invalid configuration was accepted or exposed: %v", err)
		}
	}
}

func TestPaperCallTimeoutConfiguration(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": time.Minute, "10s": 10 * time.Second, "60s": time.Minute, "120s": 2 * time.Minute} {
		if got, err := parsePaperCallTimeout(raw); err != nil || got != want {
			t.Fatalf("timeout %q=%s err=%v", raw, got, err)
		}
	}
	for _, raw := range []string{"0", "9s", "121s", "wrong", "-1s"} {
		if _, err := parsePaperCallTimeout(raw); err == nil {
			t.Fatalf("invalid timeout %q accepted", raw)
		}
	}
}
