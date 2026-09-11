package digest

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"net/url"
	"signalwatch/internal/insight"
	"strings"
	"time"
)

type Message struct {
	ID      string
	To      string
	Subject string
	Text    string
	HTML    string
}

// RenderOptions contains presentation data only; it never changes AI candidates.
type RenderOptions struct {
	PublicBaseURL string
	Remaining     int64
}

type messageView struct {
	Subscription, Overview, Date, SubscriptionURL string
	Remaining                                     int64
	Items                                         []messageItemView
}

type messageItemView struct {
	Number                                            int
	Title, URL, Keywords, Authors, Comments, Subjects string
}

var digestHTMLTemplate = template.Must(template.New("digest").Parse(`<!doctype html>
<html lang="zh"><body style="margin:0;padding:20px;font-family:Arial,sans-serif;color:#17211d;line-height:1.6">
<div style="max-width:640px;margin:0 auto">
<h1 style="font-size:22px">SignalWatch 论文速递</h1>{{if .Subscription}}<p><strong>订阅：{{.Subscription}}</strong></p>{{end}}<p>{{.Date}} · 本次收录 {{len .Items}} 篇</p>
{{if .Overview}}<section style="padding:16px;background:#f3f6f4;border-radius:8px"><h2 style="font-size:18px;margin-top:0">本次论文的研究主题</h2><p style="white-space:pre-wrap;margin-bottom:0">{{.Overview}}</p></section>{{end}}
{{range .Items}}<article style="margin:20px 0;padding-bottom:16px;border-bottom:1px solid #ddd">
<h2 style="font-size:17px;overflow-wrap:anywhere">{{.Number}}. {{.Title}}</h2>
<p style="margin:5px 0"><strong>作者：</strong>{{.Authors}}</p>
<p style="margin:5px 0"><strong>Comments：</strong>{{.Comments}}</p>
<p style="margin:5px 0"><strong>Subjects：</strong>{{.Subjects}}</p>
<p style="margin:8px 0">命中：{{.Keywords}}</p><a href="{{.URL}}">查看论文详情</a>
</article>{{end}}
{{if gt .Remaining 0}}<p>另有 {{.Remaining}} 篇待投递（发送前统计）。{{if .SubscriptionURL}}<a href="{{.SubscriptionURL}}">查看本订阅论文</a>{{else}}可登录 SignalWatch 查看本订阅论文。{{end}}</p>{{end}}
<p style="font-size:13px;color:#66736b">完整摘要与 AI 解读可在 SignalWatch 论文详情页查看。</p>
</div></body></html>`))

// A broken enhancement template must still fall back to the compact format.
var originalHTMLTemplate = template.Must(digestHTMLTemplate.Clone())

func RenderMessage(user User, job Job, items []Item, options ...RenderOptions) (Message, error) {
	return renderMessage(user, job, items, insight.Enrichment{}, originalHTMLTemplate, options...)
}

func RenderEnrichedMessage(user User, job Job, items []Item, enrichment insight.Enrichment, options ...RenderOptions) (Message, error) {
	return renderMessage(user, job, items, enrichment, digestHTMLTemplate, options...)
}

func renderMessage(user User, job Job, items []Item, enrichment insight.Enrichment, htmlTemplate *template.Template, options ...RenderOptions) (Message, error) {
	if user.ID == 0 || strings.TrimSpace(user.Email) == "" || len(items) == 0 {
		return Message{}, errors.New("invalid digest message input")
	}
	var opts RenderOptions
	if len(options) > 0 {
		opts = options[0]
	}
	base := strings.TrimRight(opts.PublicBaseURL, "/")
	if base != "" {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return Message{}, errors.New("invalid public base URL")
		}
	}
	var overview string
	var err error
	if user.DigestAIEnabled && len(items) >= 2 {
		language := user.DigestAILanguage
		overview, err = overviewText(items, language, enrichment.Digests)
		if err != nil {
			return Message{}, err
		}
	}
	view := messageView{Subscription: user.SubscriptionName, Overview: overview, Date: job.LocalDate, Remaining: opts.Remaining, Items: make([]messageItemView, 0, len(items))}
	if base != "" {
		view.SubscriptionURL = fmt.Sprintf("%s/papers?subscription_id=%d", base, job.SubscriptionID)
	}
	var plain strings.Builder
	fmt.Fprintf(&plain, "SignalWatch 论文速递\n%s · 本次收录 %d 篇\n\n", job.LocalDate, len(items))
	if user.SubscriptionName != "" {
		fmt.Fprintf(&plain, "订阅：%s\n\n", user.SubscriptionName)
	}
	if overview != "" {
		fmt.Fprintf(&plain, "本次论文的研究主题\n%s\n\n", overview)
	}
	for index, item := range items {
		var keywords []string
		seen := map[string]bool{}
		for _, match := range item.Matches {
			for _, word := range match.MatchedKeywords {
				if !seen[word] {
					keywords = append(keywords, word)
					seen[word] = true
				}
			}
		}
		reason := strings.Join(keywords, ", ")
		if reason == "" {
			reason = "分类匹配"
		}
		link := item.ArXivURL
		if base != "" {
			link = fmt.Sprintf("%s/papers?paper=%d", base, item.PaperID)
		}
		authors := metadataList(item.Authors)
		comments := strings.TrimSpace(item.Comments)
		if comments == "" {
			comments = "未提供"
		}
		subjects := metadataList(item.Categories)
		view.Items = append(view.Items, messageItemView{
			Number: index + 1, Title: item.Title, URL: link, Keywords: reason,
			Authors: authors, Comments: comments, Subjects: subjects,
		})
		fmt.Fprintf(&plain, "%d. %s\n作者：%s\nComments：%s\nSubjects：%s\n命中：%s\n查看论文详情：%s\n\n", index+1, item.Title, authors, comments, subjects, reason, link)
	}
	if view.Remaining > 0 {
		fmt.Fprintf(&plain, "另有 %d 篇待投递（发送前统计）。", view.Remaining)
		if view.SubscriptionURL != "" {
			fmt.Fprintf(&plain, "查看本订阅论文：%s\n", view.SubscriptionURL)
		} else {
			plain.WriteString("可登录 SignalWatch 查看本订阅论文。\n")
		}
	}
	plain.WriteString("完整摘要与 AI 解读可在 SignalWatch 论文详情页查看。\n")
	var html bytes.Buffer
	if err := htmlTemplate.Execute(&html, view); err != nil {
		return Message{}, fmt.Errorf("render digest html: %w", err)
	}
	return Message{To: user.Email, Subject: fmt.Sprintf("SignalWatch · %s · %s", strings.Join(strings.Fields(user.SubscriptionName), " "), job.LocalDate), Text: plain.String(), HTML: html.String()}, nil
}

func metadataList(values []string) string {
	if len(values) == 0 {
		return "未提供"
	}
	return strings.Join(values, ", ")
}

func BuildRFCMessage(from string, message Message, now time.Time) ([]byte, string, error) {
	fromAddress, err := mail.ParseAddress(from)
	if err != nil {
		return nil, "", errors.New("invalid sender address")
	}
	toAddress, err := mail.ParseAddress(message.To)
	if err != nil {
		return nil, "", errors.New("invalid recipient address")
	}
	if strings.ContainsAny(message.Subject+message.ID, "\r\n") {
		return nil, "", errors.New("invalid message subject")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	headers := []string{
		"From: " + fromAddress.String(),
		"To: " + toAddress.String(),
		"Date: " + now.UTC().Format(time.RFC1123Z),
		"Subject: " + mime.QEncoding.Encode("UTF-8", message.Subject),
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=" + writer.Boundary(),
	}
	if message.ID != "" {
		headers = append(headers, "Message-ID: <"+message.ID+">")
	}
	for _, header := range headers {
		body.WriteString(header + "\r\n")
	}
	body.WriteString("\r\n")
	if err := writeMessagePart(writer, "text/plain; charset=UTF-8", message.Text); err != nil {
		return nil, "", err
	}
	if err := writeMessagePart(writer, "text/html; charset=UTF-8", message.HTML); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close MIME message: %w", err)
	}
	return body.Bytes(), fromAddress.Address, nil
}

func writeMessagePart(writer *multipart.Writer, contentType, value string) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", contentType)
	header.Set("Content-Transfer-Encoding", "quoted-printable")
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("create MIME part: %w", err)
	}
	encoded := quotedprintable.NewWriter(part)
	if _, err := encoded.Write([]byte(value)); err != nil {
		return fmt.Errorf("write MIME part: %w", err)
	}
	if err := encoded.Close(); err != nil {
		return fmt.Errorf("close MIME part: %w", err)
	}
	return nil
}
