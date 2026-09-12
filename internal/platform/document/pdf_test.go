package document

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	domain "signalwatch/internal/document"
	"strings"
	"testing"
	"time"
)

type noopLimiter struct{}

func (noopLimiter) Wait(context.Context) error { return nil }

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestResolverRejectsUntrustedIDsAndPinsMetadata(t *testing.T) {
	e := New(noopLimiter{})
	calls := 0
	e.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "export.arxiv.org" {
			t.Fatal(r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`<feed><entry><id>http://arxiv.org/abs/1706.03762v2</id><updated>2020-01-01T00:00:00Z</updated></entry></feed>`))}, nil
	})}
	if _, err := e.resolve(context.Background(), domain.Source{ArXivID: "../../etc/passwd"}); err == nil || calls != 0 {
		t.Fatal("untrusted ID accepted")
	}
	src := domain.Source{ArXivID: "1706.03762", PDFURL: "http://127.0.0.1/private", UpdatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	id, err := e.resolve(context.Background(), src)
	if err != nil || id != "1706.03762v2" {
		t.Fatalf("%s %v", id, err)
	}
	src.UpdatedAt = src.UpdatedAt.Add(time.Hour)
	if _, err := e.resolve(context.Background(), src); err == nil {
		t.Fatal("version mismatch accepted")
	}
}
func fixturePDF() []byte {
	text := "BT /F1 12 Tf 40 700 Td (A text extraction fixture with enough words to verify page references and isolate the PDF process without network access.) Tj ET"
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(text), text)}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, v := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, v)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, n := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", n)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return []byte(b.String())
}
func TestRealPopplerExtractionAndInvalidPDF(t *testing.T) {
	for _, tool := range []string{"pdftotext", "pdfinfo", "prlimit"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	e := New(noopLimiter{})
	raw := fixturePDF()
	e.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://arxiv.org/pdf/1706.03762v1" {
			t.Fatal(r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	src := domain.Source{ArXivID: "1706.03762", PDFURL: "https://arxiv.org/pdf/1706.03762v1"}
	pages, err := e.Extract(context.Background(), src)
	if err != nil || len(pages.Pages) != 1 || !strings.Contains(pages.Pages[0], "extraction fixture") {
		t.Fatalf("%v %v", pages, err)
	}
	raw = []byte("not a PDF")
	if _, err := e.Extract(context.Background(), src); err == nil {
		t.Fatal("non-PDF accepted")
	}
}
