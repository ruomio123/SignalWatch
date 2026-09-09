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
	"strings"
	"time"
)

type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

type messageView struct {
	Date  string
	Items []messageItemView
}

type messageItemView struct {
	Title       string
	Authors     string
	Categories  string
	PublishedAt string
	Abstract    string
	ArXivURL    string
	PDFURL      string
	Matches     string
}

var digestHTMLTemplate = template.Must(template.New("digest").Parse(`<!doctype html>
<html><body style="font-family:Arial,sans-serif;color:#17211d">
<h1>SignalWatch 论文摘要</h1><p>{{.Date}} · {{len .Items}} 篇新论文</p>
{{range .Items}}<article style="margin:24px 0;padding-bottom:20px;border-bottom:1px solid #ddd">
<h2>{{.Title}}</h2><p><strong>作者：</strong>{{.Authors}}<br>
<strong>分类：</strong>{{.Categories}}<br><strong>发布时间：</strong>{{.PublishedAt}}<br>
<strong>命中：</strong>{{.Matches}}</p><p>{{.Abstract}}</p>
<p><a href="{{.ArXivURL}}">arXiv 页面</a>{{if .PDFURL}} · <a href="{{.PDFURL}}">PDF</a>{{end}}</p>
</article>{{end}}</body></html>`))

func RenderMessage(user User, job Job, items []Item) (Message, error) {
	if user.ID == 0 || strings.TrimSpace(user.Email) == "" || len(items) == 0 {
		return Message{}, errors.New("invalid digest message input")
	}
	location, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return Message{}, fmt.Errorf("load message timezone: %w", err)
	}
	view := messageView{Date: job.LocalDate, Items: make([]messageItemView, 0, len(items))}
	var plain strings.Builder
	fmt.Fprintf(&plain, "SignalWatch 论文摘要\n%s · %d 篇新论文\n\n", job.LocalDate, len(items))
	for index, item := range items {
		matches := make([]string, 0, len(item.Matches))
		for _, match := range item.Matches {
			reason := match.SubscriptionName + " [" + match.Category + "]"
			if len(match.MatchedKeywords) > 0 {
				reason += "：" + strings.Join(match.MatchedKeywords, ", ")
			}
			matches = append(matches, reason)
		}
		published := item.PublishedAt.In(location).Format("2006-01-02 15:04 MST")
		view.Items = append(view.Items, messageItemView{
			Title: item.Title, Authors: strings.Join(item.Authors, ", "),
			Categories: strings.Join(item.Categories, ", "), PublishedAt: published,
			Abstract: item.Abstract, ArXivURL: item.ArXivURL, PDFURL: item.PDFURL,
			Matches: strings.Join(matches, "；"),
		})
		fmt.Fprintf(&plain, "%d. %s\n作者：%s\n分类：%s\n发布时间：%s\n命中：%s\n%s\n%s\n",
			index+1, item.Title, strings.Join(item.Authors, ", "),
			strings.Join(item.Categories, ", "), published, strings.Join(matches, "；"),
			item.Abstract, item.ArXivURL,
		)
		if item.PDFURL != "" {
			fmt.Fprintf(&plain, "PDF：%s\n", item.PDFURL)
		}
		plain.WriteString("\n")
	}
	var html bytes.Buffer
	if err := digestHTMLTemplate.Execute(&html, view); err != nil {
		return Message{}, fmt.Errorf("render digest html: %w", err)
	}
	return Message{
		To: user.Email, Subject: fmt.Sprintf("SignalWatch 每日论文摘要 · %s", job.LocalDate),
		Text: plain.String(), HTML: html.String(),
	}, nil
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
	if strings.ContainsAny(message.Subject, "\r\n") {
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
