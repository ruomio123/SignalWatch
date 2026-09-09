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
	message, err := RenderMessage(user, job, items)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, wanted := range []string{"Ada, Grace", "cs.AI, cs.LG", "An abstract.", "Agents", "agent", "arxiv.org"} {
		if !strings.Contains(message.Text, wanted) {
			t.Fatalf("plain message missing %q", wanted)
		}
	}
	if strings.Contains(message.HTML, `<unsafe & paper>`) || !strings.Contains(message.HTML, "&lt;unsafe &amp; paper&gt;") {
		t.Fatalf("HTML title was not escaped: %s", message.HTML)
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
