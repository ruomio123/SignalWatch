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

// OCR is optional. Implementations must honor cancellation and return one text
// entry per PDF page; the adapter still checks all size and quality bounds.
type OCR interface {
	ExtractPages(context.Context, []byte) ([]string, error)
}
type Extractor struct {
	OCR     OCR
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
		return domain.Extracted{}, &domain.ExtractionError{Code: "document_version_unavailable"}
	}
	raw, err := e.get(ctx, "https://arxiv.org/pdf/"+id, 25<<20)
	if err != nil {
		return domain.Extracted{}, &domain.ExtractionError{Code: "document_download_failed"}
	}
	if !bytes.HasPrefix(raw, []byte("%PDF-")) {
		return domain.Extracted{}, &domain.ExtractionError{Code: "invalid_pdf"}
	}
	dir, err := os.MkdirTemp("", "signalwatch-pdf-")
	if err != nil {
		return domain.Extracted{}, &domain.ExtractionError{Code: "document_extraction_failed"}
	}
	defer os.RemoveAll(dir)
	input, output := filepath.Join(dir, "input.pdf"), filepath.Join(dir, "text.txt")
	if os.WriteFile(input, raw, 0600) != nil {
		return domain.Extracted{}, &domain.ExtractionError{Code: "document_extraction_failed"}
	}
	fallback := func(code string, count int) (domain.Extracted, error) {
		if ctx.Err() != nil {
			return domain.Extracted{}, ctx.Err()
		}
		if e.OCR == nil {
			return domain.Extracted{}, &domain.ExtractionError{Code: code}
		}
		pages, err := e.OCR.ExtractPages(ctx, raw)
		if err != nil || len(pages) == 0 || len(pages) > 200 || (count > 0 && len(pages) != count) || len(strings.Join(pages, "")) > 4<<20 || len(strings.TrimSpace(strings.Join(pages, ""))) < 80 {
			return domain.Extracted{}, &domain.ExtractionError{Code: "ocr_failed"}
		}
		return domain.Extracted{Pages: pages, SourceVersion: id}, nil
	}
	info, err := run(ctx, "pdfinfo", input)
	if err != nil {
		return fallback("text_extraction_failed", 0)
	}
	count := 0
	for _, line := range strings.Split(string(info), "\n") {
		if strings.HasPrefix(line, "Pages:") {
			count, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pages:")))
		}
	}
	if count < 1 || count > 200 {
		return domain.Extracted{}, &domain.ExtractionError{Code: "document_resource_limit"}
	}
	// Use reading order. Physical layout places adjacent columns on the same
	// line; flattening those lines would mix unrelated sentences in evidence.
	if _, err = run(ctx, "pdftotext", "-enc", "UTF-8", input, output); err != nil {
		return fallback("text_extraction_failed", count)
	}
	file, err := os.Open(output)
	if err != nil {
		return fallback("text_extraction_failed", count)
	}
	defer file.Close()
	text, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		return fallback("text_extraction_failed", count)
	}
	if len(text) > 4<<20 {
		return domain.Extracted{}, &domain.ExtractionError{Code: "document_resource_limit"}
	}
	pages := strings.Split(string(text), "\f")
	if len(pages) > 0 && strings.TrimSpace(pages[len(pages)-1]) == "" {
		pages = pages[:len(pages)-1]
	}
	if len(pages) != count {
		return fallback("text_extraction_failed", count)
	}
	if len(strings.TrimSpace(string(text))) < 80 {
		return fallback("ocr_required", count)
	}
	// An empty page containing raster images may be a scanned page. Blank PDF
	// pages without images are allowed, including trailing blank pages.
	images, err := run(ctx, "pdfimages", "-list", input)
	if err != nil {
		return fallback("text_extraction_failed", count)
	}
	for _, line := range strings.Split(string(images), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		page, err := strconv.Atoi(fields[0])
		if err == nil && page > 0 && page <= count && len(strings.TrimSpace(pages[page-1])) < 80 {
			return fallback("ocr_required", count)
		}
	}
	return domain.Extracted{Pages: pages, SourceVersion: id}, nil
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
