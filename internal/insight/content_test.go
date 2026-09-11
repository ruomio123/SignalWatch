package insight

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummaryEvidenceAndSchema(t *testing.T) {
	p := Paper{ID: 1, Title: "VLM adaptation", Abstract: "We adapt visual language models."}
	s := Summary{Summary: "适配视觉语言模型。", Contributions: []string{}, Method: "摘要未说明", Applications: []Application{{Text: "图像理解", Inferred: true}}, Evidence: []Evidence{{Field: "abstract", Quote: p.Abstract}}, Limitations: "仅基于标题与摘要"}
	raw, _ := json.Marshal(s)
	var decoded Summary
	if err := Decode(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSummary(decoded, p); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(raw), p.Abstract, "invented 99% accuracy", 1), strings.Replace(string(raw), `"inferred":true`, `"inferred":null`, 1), strings.Replace(string(raw), `"inferred":true`, `"unknown":true`, 1), string(raw) + `{}`, strings.Replace(string(raw), `"summary":`, `"extra":1,"summary":`, 1)} {
		var value Summary
		if Decode([]byte(bad), &value) == nil && ValidateSummary(value, p) == nil {
			t.Fatalf("accepted invalid output: %s", bad)
		}
	}
	p2 := p
	p2.ID = 2
	if PaperHash(p) != PaperHash(p2) {
		t.Fatal("content hash includes identity")
	}
	p2.Abstract += " Updated."
	if PaperHash(p) == PaperHash(p2) {
		t.Fatal("content change didn't invalidate")
	}
}
func TestOverviewReferences(t *testing.T) {
	p := []Paper{{ID: 1}, {ID: 2}}
	s := Overview{Summary: "本次订阅样本", Themes: []Theme{{Title: "主题", Description: "适配", PaperIDs: []uint64{1, 2}}}}
	if err := ValidateOverview(s, p); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]uint64{{1, 3}, {1, 1}, {}} {
		s.Themes[0].PaperIDs = ids
		if ValidateOverview(s, p) == nil {
			t.Fatalf("accepted %v", ids)
		}
	}
	if ValidateOverview(Overview{Summary: "one", Themes: []Theme{}}, p[:1]) == nil {
		t.Fatal("accepted single paper")
	}
}
