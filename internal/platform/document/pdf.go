// Package document implements bounded arXiv downloads and isolated Poppler execution.
package document

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	domain "signalwatch/internal/document"
	"strconv"
	"strings"
	"time"
)

var ErrPDF = errors.New("PDF extraction unavailable")
var identifier = regexp.MustCompile(`^(?:[0-9]{4}\.[0-9]{4,5}|[a-zA-Z][a-zA-Z0-9.-]*/[0-9]{7})(?:v[1-9][0-9]*)?$`)
var versioned = regexp.MustCompile(`v[1-9][0-9]*$`)

type Limiter interface{ Wait(context.Context) error }
type Extractor struct {
	Client  *http.Client
	Limiter Limiter
}

func New(limiter Limiter) *Extractor {
	return &Extractor{Client: &http.Client{Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Limiter: limiter}
}
func (e *Extractor) get(ctx context.Context, address string, maxBytes int64) ([]byte, error) {
	if e.Limiter == nil {
		return nil, ErrPDF
	}
	if err := e.Limiter.Wait(ctx); err != nil {
		return nil, ErrPDF
	}
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, ErrPDF
	}
	req.Header.Set("User-Agent", "SignalWatch/1.0 (on-demand paper reading)")
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, ErrPDF
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, ErrPDF
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || int64(len(raw)) > maxBytes {
		return nil, ErrPDF
	}
	return raw, nil
}

// resolve pins a version. Legacy unversioned rows are checked against metadata
// before downloading so a new arXiv revision cannot masquerade as the old one.
func (e *Extractor) resolve(ctx context.Context, s domain.Source) (string, error) {
	if !identifier.MatchString(s.ArXivID) {
		return "", ErrPDF
	}
	if u, err := url.Parse(s.PDFURL); err == nil && u.Hostname() == "arxiv.org" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
		id := strings.TrimPrefix(u.Path, "/pdf/")
		id = strings.TrimSuffix(id, ".pdf")
		if identifier.MatchString(id) && versioned.MatchString(id) && versioned.ReplaceAllString(id, "") == versioned.ReplaceAllString(s.ArXivID, "") {
			return id, nil
		}
	}
	raw, err := e.get(ctx, "https://export.arxiv.org/api/query?id_list="+url.QueryEscape(s.ArXivID), 1<<20)
	if err != nil {
		return "", err
	}
	var feed struct {
		Entries []struct {
			ID      string `xml:"id"`
			Updated string `xml:"updated"`
		} `xml:"entry"`
	}
	if xml.Unmarshal(raw, &feed) != nil || len(feed.Entries) != 1 {
		return "", ErrPDF
	}
	row := feed.Entries[0]
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(row.Updated))
	if err != nil || !at.Equal(s.UpdatedAt) {
		return "", ErrPDF
	}
	id := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(row.ID), "http://arxiv.org/abs/"), "https://arxiv.org/abs/")
	if !identifier.MatchString(id) || !versioned.MatchString(id) || versioned.ReplaceAllString(id, "") != versioned.ReplaceAllString(s.ArXivID, "") {
		return "", ErrPDF
	}
	return id, nil
}
func (e *Extractor) Extract(ctx context.Context, s domain.Source) (domain.Extracted, error) {
	id, err := e.resolve(ctx, s)
	if err != nil {
		return domain.Extracted{}, err
	}
	raw, err := e.get(ctx, "https://arxiv.org/pdf/"+id, 25<<20)
	if err != nil || !bytes.HasPrefix(raw, []byte("%PDF-")) {
		return domain.Extracted{}, ErrPDF
	}
	dir, err := os.MkdirTemp("", "signalwatch-pdf-")
	if err != nil {
		return domain.Extracted{}, ErrPDF
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "input.pdf")
	output := filepath.Join(dir, "text.txt")
	if os.WriteFile(input, raw, 0600) != nil {
		return domain.Extracted{}, ErrPDF
	}
	info, err := run(ctx, "pdfinfo", input)
	if err != nil {
		return domain.Extracted{}, ErrPDF
	}
	pages := 0
	for _, line := range strings.Split(string(info), "\n") {
		if strings.HasPrefix(line, "Pages:") {
			pages, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pages:")))
		}
	}
	if pages < 1 || pages > 200 {
		return domain.Extracted{}, ErrPDF
	}
	if _, err = run(ctx, "pdftotext", "-enc", "UTF-8", "-layout", input, output); err != nil {
		return domain.Extracted{}, ErrPDF
	}
	file, err := os.Open(output)
	if err != nil {
		return domain.Extracted{}, ErrPDF
	}
	defer file.Close()
	text, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(text) > 4<<20 {
		return domain.Extracted{}, ErrPDF
	}
	values := strings.Split(string(text), "\f")
	if len(values) > 0 && strings.TrimSpace(values[len(values)-1]) == "" {
		values = values[:len(values)-1]
	}
	if len(values) != pages || len(strings.TrimSpace(string(text))) < 80 {
		return domain.Extracted{}, ErrPDF
	}
	return domain.Extracted{Pages: values, SourceVersion: id}, nil
}
func run(ctx context.Context, program string, args ...string) ([]byte, error) {
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(work, "prlimit", append([]string{"--as=536870912", "--cpu=25", "--fsize=8388608", "--nofile=64", "--", program}, args...)...)
	var stdout cappedBuffer
	command.Stdout = &stdout
	command.Stderr = io.Discard
	err := command.Run()
	return stdout.Bytes(), err
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, ErrPDF
	}
	return b.Buffer.Write(p)
}
