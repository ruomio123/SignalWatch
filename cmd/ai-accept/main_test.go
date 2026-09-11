package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"signalwatch/internal/insight"
	"strings"
	"testing"
)

func TestAcceptanceUsesProductValidationAndNeverSavesCredential(t *testing.T) {
	const secret = "test-credential-DO-NOT-LOG"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("missing authentication")
		}
		var wire struct{ Messages []struct{ Content string } }
		if json.NewDecoder(r.Body).Decode(&wire) != nil {
			t.Error("invalid wire")
		}
		if strings.Contains(wire.Messages[0].Content, secret) || strings.Contains(wire.Messages[1].Content, secret) {
			t.Error("key in prompt")
		}
		var req struct{ Papers []insight.Paper }
		json.Unmarshal([]byte(wire.Messages[1].Content), &req)
		var content any
		if len(req.Papers) == 1 {
			content = insight.Summary{Summary: "Supported summary " + secret, Contributions: []string{}, Method: "not specified", Applications: []insight.Application{}, Evidence: []insight.Evidence{{Field: "abstract", Quote: req.Papers[0].Abstract}}, Limitations: "abstract only"}
		} else {
			content = insight.Overview{Summary: "This selection only", Themes: []insight.Theme{{Title: "theme", Description: "comparison", PaperIDs: []uint64{1, 2}}}}
		}
		raw, _ := json.Marshal(content)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(raw)}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 20}})
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "report")
	req := request{Key: secret, BaseURL: server.URL, Model: "fixture", Language: "both", Groups: 1, Output: output, Papers: []insight.Paper{{ID: 1, Title: "One", Abstract: "We study adaptation."}, {ID: 2, Title: "Two", Abstract: "We study alignment."}, {ID: 3, Title: "Three", Abstract: "We study VLM."}}}
	if err := run(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if calls != 8 {
		t.Fatalf("calls=%d", calls)
	}
	entries, _ := os.ReadDir(output)
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(output, e.Name()))
		if strings.Contains(string(raw), secret) {
			t.Fatalf("credential leaked in %s", e.Name())
		}
	}
	var r report
	raw, _ := os.ReadFile(filepath.Join(output, "report.json"))
	if json.Unmarshal(raw, &r) != nil || r.Calls != 8 || r.Ready != 8 || !strings.Contains(r.ManualReview, "pending") {
		t.Fatal("report falsely marks acceptance")
	}
	if err := run(context.Background(), req); err == nil {
		t.Fatal("overwrote prior evidence")
	}
	if calls != 8 {
		t.Fatal("called before checking output")
	}
}
func TestAuthenticationFailureStopsWithoutBodyLeak(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(401)
		w.Write([]byte("test-secret confidential diagnostic"))
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "report")
	err := run(t.Context(), request{Key: "test-secret", BaseURL: server.URL, Model: "fixture", Language: "zh", Groups: 1, Output: output, Papers: []insight.Paper{{ID: 1, Title: "a", Abstract: "b"}, {ID: 2, Title: "c", Abstract: "d"}}})
	if err == nil || calls != 1 {
		t.Fatalf("didn't stop: %v %d", err, calls)
	}
	raw, _ := os.ReadFile(filepath.Join(output, "report.json"))
	if strings.Contains(string(raw), "confidential") || strings.Contains(string(raw), "test-secret") {
		t.Fatal("provider body leaked")
	}
}
