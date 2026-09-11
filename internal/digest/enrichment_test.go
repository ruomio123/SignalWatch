package digest

import (
	"context"
	"html/template"
	"signalwatch/internal/insight"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type enrichmentFunc func(context.Context, User, Job, []Item) (insight.Enrichment, error)

func (f enrichmentFunc) Lookup(c context.Context, u User, j Job, i []Item) (insight.Enrichment, error) {
	return f(c, u, j, i)
}
func TestAIEnrichmentDeadlineAndCorruptFallback(t *testing.T) {
	for _, mode := range []string{"deadline", "corrupt", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			job, u, items, now := digestFixture()
			u.DigestAILanguage = "both"
			u.DigestAIEnabled = mode != "disabled"
			p2 := items[0]
			p2.PaperID = 12
			items = append(items, p2)
			repo := &repositoryStub{user: u, items: items}
			coord := &coordinatorStub{acquired: true}
			sender := &senderStub{}
			processor, _ := newTestProcessor(repo, coord, sender, func() time.Time { return now })
			processor.enricher = enrichmentFunc(func(ctx context.Context, _ User, _ Job, _ []Item) (insight.Enrichment, error) {
				if mode == "disabled" {
					t.Fatal("disabled AI read cache")
				}
				if mode == "deadline" {
					<-ctx.Done()
					return insight.Enrichment{}, ctx.Err()
				}
				return insight.Enrichment{Digests: []insight.DigestResult{{Language: "zh", Content: insight.Overview{Summary: "untrusted"}}}}, nil
			})
			start := time.Now()
			result, err := processor.Process(t.Context(), job)
			if err != nil || result.Items != 2 || len(repo.markedIDs) != 2 || !coord.marked {
				t.Fatalf("fallback failed: %+v %v", result, err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("cache read blocked mail")
			}
			if strings.Contains(sender.message.Text, items[0].Abstract) || !strings.Contains(sender.message.Text, items[0].Title) || strings.Contains(sender.message.Text, "untrusted") {
				t.Fatal("compact fallback missing")
			}
		})
	}
}
func TestAIEnrichmentBilingualAndEscaping(t *testing.T) {
	job, u, items, _ := digestFixture()
	u.DigestAILanguage = "both"
	u.DigestAIEnabled = true
	p2 := items[0]
	p2.PaperID = 12
	items = append(items, p2)
	s := insight.Summary{Summary: "<script>alert(1)</script>", Contributions: []string{"contribution"}, Method: "not specified", Applications: []insight.Application{{Text: "scenario", Inferred: true}}, Evidence: []insight.Evidence{{Field: "abstract", Quote: items[0].Abstract}}, Limitations: "abstract only"}
	e := insight.Enrichment{Papers: map[uint64][]insight.PaperResult{11: {{Language: "zh", Content: s}}}, Digests: []insight.DigestResult{{Language: "zh", Content: insight.Overview{Summary: "<script>sample overview</script>", Themes: []insight.Theme{{Title: "theme", Description: "comparison", PaperIDs: []uint64{11, 12}}}}}}}
	m, err := RenderEnrichedMessage(u, job, items, e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sample overview", items[0].Title} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(m.HTML, "<script>") || !strings.Contains(m.HTML, "&lt;script&gt;") {
		t.Fatal("model HTML unescaped")
	}
}

func TestEnhancementTemplateFailureUsesOriginal(t *testing.T) {
	saved := digestHTMLTemplate
	digestHTMLTemplate = template.Must(template.New("broken").Parse(`{{.MissingField}}`))
	defer func() { digestHTMLTemplate = saved }()
	job, u, items, now := digestFixture()
	repo := &repositoryStub{user: u, items: items}
	sender := &senderStub{}
	coord := &coordinatorStub{acquired: true}
	p, _ := newTestProcessor(repo, coord, sender, func() time.Time { return now })
	_, err := p.Process(t.Context(), job)
	if err != nil || !coord.marked || strings.Contains(sender.message.HTML, items[0].Abstract) || !strings.Contains(sender.message.HTML, items[0].Title) {
		t.Fatalf("original template failed: %v", err)
	}
}

func TestSummaryUsesOnlyTenSelectedPapers(t *testing.T) {
	job, u, fixture, now := digestFixture()
	u.DigestAIEnabled = true
	u.DigestAILanguage = "zh"
	u.MaxItemsPerDigest = 10
	items := make([]Item, 10)
	for i := range items {
		items[i] = fixture[0]
		items[i].PaperID = uint64(i + 1)
	}
	repo := &repositoryStub{user: u, items: items, remaining: 10}
	sender := &senderStub{}
	p, _ := newTestProcessor(repo, &coordinatorStub{acquired: true}, sender, func() time.Time { return now })
	p.enricher = enrichmentFunc(func(_ context.Context, _ User, _ Job, selected []Item) (insight.Enrichment, error) {
		if len(selected) != 10 || selected[9].PaperID != 10 {
			t.Fatal("wrong AI candidates")
		}
		return insight.Enrichment{Digests: []insight.DigestResult{{Language: "zh", Content: insight.Overview{Summary: "本次研究主题", Themes: []insight.Theme{{Title: "主题", Description: "概述", PaperIDs: []uint64{1, 10}}}}}}}, nil
	})
	result, err := p.Process(t.Context(), job)
	if err != nil || result.Items != 10 || len(repo.markedIDs) != 10 || !strings.Contains(sender.message.Text, "另有 10 篇") || !strings.Contains(sender.message.Text, "本次研究主题") {
		t.Fatalf("wrong selection/delivery: %+v %v", result, err)
	}
	bad := insight.Enrichment{Digests: []insight.DigestResult{{Language: "zh", Content: insight.Overview{Summary: "unselected", Themes: []insight.Theme{{Title: "主题", Description: "概述", PaperIDs: []uint64{20}}}}}}}
	if _, err := RenderEnrichedMessage(u, job, items, bad); err == nil {
		t.Fatal("accepted reference outside email")
	}
}

func TestCompactOverviewLengthAndBilingualOrder(t *testing.T) {
	for _, tc := range []struct {
		lang, text string
		max        int
	}{
		{"zh", strings.Repeat("研究主题。", 100), 241},
		{"en", strings.Repeat("research methods ", 100), 601},
	} {
		got := compactOverview(tc.text, tc.lang)
		if !utf8.ValidString(got) || len([]rune(got)) > tc.max || !strings.HasSuffix(got, "…") {
			t.Fatal("unbounded/invalid excerpt")
		}
	}
	job, u, items, _ := digestFixture()
	u.DigestAIEnabled = true
	u.DigestAILanguage = "both"
	p2 := items[0]
	p2.PaperID = 12
	items = append(items, p2)
	e := insight.Enrichment{Digests: []insight.DigestResult{
		{Language: "en", Content: insight.Overview{Summary: "English overview", Themes: []insight.Theme{}}},
		{Language: "zh", Content: insight.Overview{Summary: "中文概览", Themes: []insight.Theme{}}},
	}}
	m, err := RenderEnrichedMessage(u, job, items, e)
	if err != nil || strings.Index(m.Text, "中文概览") > strings.Index(m.Text, "English overview") {
		t.Fatal("wrong bilingual ordering", err)
	}
	u.DigestAIEnabled = false
	m, err = RenderEnrichedMessage(u, job, items, e)
	if err != nil || strings.Contains(m.Text, "English overview") {
		t.Fatal("disabled overview shown", err)
	}
}
