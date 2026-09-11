package digest

import (
	"strings"
	"testing"
	"time"
)

func TestRenderAndBuildMessageContainsRequiredPaperFieldsAndEscapesHTML(t *testing.T) {
	job, user, items, now := digestFixture()
	items[0].Title = `<unsafe & paper>`
	items[0].Authors = []string{"Ada", "Grace"}
	items[0].Categories = []string{"cs.AI", "cs.LG"}
	items[0].Comments = `12 pages, <draft & review>`
	message, err := RenderMessage(user, job, items)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, wanted := range []string{"<unsafe & paper>", "作者：Ada, Grace", "Comments：12 pages, <draft & review>", "Subjects：cs.AI, cs.LG", "agent", "arxiv.org"} {
		if !strings.Contains(message.Text, wanted) {
			t.Fatalf("plain message missing %q", wanted)
		}
	}
	for _, absent := range []string{"An abstract.", "PDF："} {
		if strings.Contains(message.Text, absent) || strings.Contains(message.HTML, absent) {
			t.Fatalf("compact email contains %q", absent)
		}
	}
	if strings.Contains(message.HTML, `<unsafe & paper>`) || !strings.Contains(message.HTML, "&lt;unsafe &amp; paper&gt;") {
		t.Fatalf("HTML title was not escaped: %s", message.HTML)
	}
	if strings.Contains(message.HTML, `<draft & review>`) || !strings.Contains(message.HTML, "&lt;draft &amp; review&gt;") {
		t.Fatalf("HTML comments were not escaped: %s", message.HTML)
	}
	raw, envelopeFrom, err := BuildRFCMessage(
		"SignalWatch <digest@signalwatch.local>", message, now,
	)
	if err != nil {
		t.Fatalf("build RFC message: %v", err)
	}
	if envelopeFrom != "digest@signalwatch.local" ||
		!strings.Contains(string(raw), "multipart/alternative") ||
		!strings.Contains(string(raw), "Subject: =?UTF-8?q?") {
		t.Fatalf("unexpected RFC message: from=%q raw=%s", envelopeFrom, raw)
	}
}

func TestBuildRFCMessageRejectsHeaderInjection(t *testing.T) {
	_, _, err := BuildRFCMessage("digest@example.test", Message{
		To: "reader@example.test", Subject: "valid\r\nBcc: attacker@example.test",
	}, time.Now())
	if err == nil {
		t.Fatal("expected header injection rejection")
	}
}

func TestCompactLinksAndRemaining(t *testing.T) {
	job, user, items, _ := digestFixture()
	for _, base := range []string{"", "https://watch.example.test"} {
		for _, remaining := range []int64{0, 10} {
			m, err := RenderMessage(user, job, items, RenderOptions{PublicBaseURL: base, Remaining: remaining})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(m.Text, "另有 10 篇") != (remaining == 10) {
				t.Fatal("incorrect footer", m.Text)
			}
			if base != "" && (!strings.Contains(m.Text, base+"/papers?paper=11") || !strings.Contains(m.HTML, base+"/papers?paper=11")) {
				t.Fatal("missing detail link")
			}
			if base != "" && remaining > 0 && !strings.Contains(m.Text, base+"/papers?subscription_id=3") {
				t.Fatal("missing subscription link")
			}
		}
	}
	items[0].Matches[0].MatchedKeywords = nil
	items[0].Authors = nil
	items[0].Categories = nil
	items[0].Comments = ""
	m, err := RenderMessage(user, job, items)
	if err != nil || !strings.Contains(m.Text, "分类匹配") || strings.Count(m.Text, "未提供") != 3 {
		t.Fatal("category-only match missing", err)
	}
}
