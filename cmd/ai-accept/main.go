// ai-accept receives credentials only on stdin. Use scripts/accept-ai-local.py.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"signalwatch/internal/ai"
	"signalwatch/internal/digest"
	"signalwatch/internal/insight"
	"signalwatch/internal/platform/llm"
)

type request struct {
	Key      string          `json:"key"`
	BaseURL  string          `json:"base_url"`
	Model    string          `json:"model"`
	Papers   []insight.Paper `json:"papers"`
	Language string          `json:"language"`
	Groups   int             `json:"groups"`
	Output   string          `json:"output"`
	Mailpit  string          `json:"mailpit"`
}
type result struct {
	Kind         string   `json:"kind"`
	Language     string   `json:"language"`
	IDs          []uint64 `json:"paper_ids"`
	State        string   `json:"state"`
	Error        string   `json:"error,omitempty"`
	LatencyMS    int64    `json:"latency_ms"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	UsageKnown   bool     `json:"usage_known"`
	Content      any      `json:"content,omitempty"`
}
type report struct {
	Model        string    `json:"model"`
	Schema       string    `json:"schema"`
	GeneratedAt  time.Time `json:"generated_at"`
	ManualReview string    `json:"manual_review"`
	Calls        int       `json:"calls"`
	Ready        int       `json:"ready"`
	Results      []result  `json:"results"`
	Mailpit      []string  `json:"mailpit,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var req request
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 4<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil {
		fmt.Fprintln(os.Stderr, "invalid acceptance input")
		os.Exit(1)
	}
	if err := run(ctx, req); err != nil {
		fmt.Fprintln(os.Stderr, "验收未全部通过，请查看已生成报告；未输出底层错误或密钥。")
		os.Exit(1)
	}
}
func run(ctx context.Context, req request) error {
	languages := insight.Languages(req.Language)
	if len(languages) == 0 || len(req.Papers) < 2 || len(req.Papers) > 100 || req.Groups < 1 || req.Groups > (len(req.Papers)+2)/3 || (len(req.Papers)+req.Groups)*len(languages) > 200 || req.Output == "" {
		return fmt.Errorf("invalid options")
	}
	ids := map[uint64]bool{}
	for _, p := range req.Papers {
		if p.ID == 0 || ids[p.ID] || strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Abstract) == "" {
			return fmt.Errorf("invalid papers")
		}
		ids[p.ID] = true
	}
	client, err := llm.New(req.BaseURL, req.Model, req.Key)
	if err != nil {
		return err
	}
	var sender *digest.SMTPSender
	if req.Mailpit != "" {
		host, _, err := net.SplitHostPort(req.Mailpit)
		if err != nil {
			return fmt.Errorf("invalid Mailpit")
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("Mailpit must be local")
		}
		sender, err = digest.NewSMTPSender(digest.SMTPConfig{Addr: req.Mailpit, From: "SignalWatch <acceptance@example.test>", Timeout: 5 * time.Second}, time.Now)
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(req.Output), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(req.Output, 0700); err != nil {
		return err
	} // Never overwrite a prior run.
	write := func(name string, b []byte) error {
		return os.WriteFile(filepath.Join(req.Output, name), []byte(strings.ReplaceAll(string(b), req.Key, "[REDACTED]")), 0600)
	}
	r := report{Model: req.Model, Schema: insight.SchemaVersion, GeneratedAt: time.Now().UTC(), ManualReview: "pending: schema success does not establish factual quality", Results: []result{}}
	save := func() error { b, _ := json.MarshalIndent(r, "", "  "); return write("report.json", b) }
	data, _ := json.MarshalIndent(req.Papers, "", "  ")
	if err := write("papers.json", data); err != nil {
		return err
	}
	enrichment := insight.Enrichment{Papers: map[uint64][]insight.PaperResult{}}
	call := func(kind, lang string, papers []insight.Paper) result {
		out := result{Kind: kind, Language: lang, State: "failed", IDs: []uint64{}}
		for _, p := range papers {
			out.IDs = append(out.IDs, p.ID)
		}
		raw, _ := json.Marshal(struct {
			Language string          `json:"language"`
			Papers   []insight.Paper `json:"papers"`
		}{lang, papers})
		started := time.Now()
		timeout, cancel := context.WithTimeout(ctx, 20*time.Second)
		response, err := client.Generate(timeout, ai.GenerationPrompt(kind), raw)
		cancel()
		out.LatencyMS = time.Since(started).Milliseconds()
		out.InputTokens = response.InputTokens
		out.OutputTokens = response.OutputTokens
		out.UsageKnown = response.UsageKnown
		if err != nil {
			out.Error = "model_request_failed"
			if f, ok := err.(*llm.Failure); ok {
				out.Error = f.Code
			}
		} else if kind == ai.PaperKind {
			var summary insight.Summary
			if insight.Decode(response.Content, &summary) != nil || insight.ValidateSummary(summary, papers[0]) != nil {
				out.Error = "invalid_summary"
			} else {
				out.Content = summary
				out.State = "ready"
				enrichment.Papers[papers[0].ID] = append(enrichment.Papers[papers[0].ID], insight.PaperResult{Language: lang, Content: summary})
			}
		} else {
			var overview insight.Overview
			if insight.Decode(response.Content, &overview) != nil || insight.ValidateOverview(overview, papers) != nil {
				out.Error = "invalid_overview"
			} else {
				out.Content = overview
				out.State = "ready"
				enrichment.Digests = append(enrichment.Digests, insight.DigestResult{Language: lang, Content: overview})
			}
		}
		r.Calls++
		if out.State == "ready" {
			r.Ready++
		}
		r.Results = append(r.Results, out)
		fmt.Printf("调用 %d：%s / %s / %s（%dms）\n", r.Calls, kind, lang, out.State, out.LatencyMS)
		return out
	}
	for _, p := range req.Papers {
		for _, lang := range languages {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			out := call(ai.PaperKind, lang, []insight.Paper{p})
			if err := save(); err != nil {
				return err
			}
			if out.Error == "credential_rejected" || out.Error == "model_access_denied" {
				return fmt.Errorf("authentication failed")
			}
		}
	}
	for group := 0; group < req.Groups; group++ {
		start := group * 3
		end := min(start+3, len(req.Papers))
		if end-start < 2 {
			start = end - 2
		}
		papers := req.Papers[start:end]
		enrichment.Digests = nil
		for _, lang := range languages {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			call(ai.DigestKind, lang, papers)
			if err := save(); err != nil {
				return err
			}
		}
		u := digest.User{ID: 1, SubscriptionID: uint64(group + 1), SubscriptionName: fmt.Sprintf("AI 验收样本 %02d", group+1), Email: "acceptance@example.test", Timezone: "UTC", MaxItemsPerDigest: uint16(len(papers)), DigestAIEnabled: true, DigestAILanguage: req.Language}
		job := digest.Job{UserID: u.ID, SubscriptionID: u.SubscriptionID, LocalDate: r.GeneratedAt.Format("2006-01-02")}
		items := make([]digest.Item, len(papers))
		for i, p := range papers {
			items[i] = digest.Item{PaperID: p.ID, Title: p.Title, Abstract: p.Abstract, Authors: []string{"验收输入未提供作者"}, Categories: []string{}, PublishedAt: r.GeneratedAt, ArXivURL: "", Matches: []digest.Match{{SubscriptionID: u.SubscriptionID, SubscriptionName: u.SubscriptionName}}}
		}
		message, err := digest.RenderEnrichedMessage(u, job, items, enrichment)
		if err != nil {
			message, err = digest.RenderMessage(u, job, items)
		}
		if err != nil {
			return err
		}
		// Redact before any file or optional local SMTP output, including model echoes.
		message.Text = strings.ReplaceAll(message.Text, req.Key, "[REDACTED]")
		message.HTML = strings.ReplaceAll(message.HTML, req.Key, "[REDACTED]")
		if err := write(fmt.Sprintf("digest-%02d.html", group+1), []byte(message.HTML)); err != nil {
			return err
		}
		if err := write(fmt.Sprintf("digest-%02d.txt", group+1), []byte(message.Text)); err != nil {
			return err
		}
		fallback, err := digest.RenderMessage(u, job, items)
		if err != nil {
			return err
		}
		if err := write(fmt.Sprintf("fallback-%02d.txt", group+1), []byte(fallback.Text)); err != nil {
			return err
		}
		if err := write(fmt.Sprintf("fallback-%02d.html", group+1), []byte(fallback.HTML)); err != nil {
			return err
		}
		if sender != nil {
			status := "sent"
			if sender.Send(ctx, message) != nil {
				status = "failed"
			}
			r.Mailpit = append(r.Mailpit, status)
		}
		if err := save(); err != nil {
			return err
		}
	}
	note := fmt.Sprintf("# 本地 AI 验收\n\n调用 %d 次，结构校验通过 %d 次。人工质量审查：待完成。\n\n对照 papers.json 与 report.json 检查：贡献/方法有无依据、是否编造实验数字、推断是否标记、主题是否只概括本次样本。\n\n逐组打开 digest-XX.html 和 fallback-XX.html 对比带研究主题与无 AI 总结的精简邮件。\n\n这是直接模型与模板验收，不覆盖 Worker 调度、持久任务、共享额度或真实投递标记。输入样本按文件/数据库顺序每三篇分组；不足两篇时与前组重叠，正式审查请检查样本主题覆盖。\n", r.Calls, r.Ready)
	if err := write("REVIEW.md", []byte(note)); err != nil {
		return err
	}
	fmt.Println("报告和邮件预览已生成。结构校验通过不代表人工验收通过。")
	if r.Ready != r.Calls {
		return fmt.Errorf("generation failures")
	}
	for _, status := range r.Mailpit {
		if status != "sent" {
			return fmt.Errorf("Mailpit failed")
		}
	}
	return nil
}
