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
	return singlePagePDF(text)
}

func singlePagePDF(text string) []byte {
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
	for _, tool := range []string{"pdftotext", "pdfinfo", "pdfimages", "prlimit"} {
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

type ocrFunc func(context.Context, []byte) ([]string, error)

func (f ocrFunc) ExtractPages(ctx context.Context, raw []byte) ([]string, error) { return f(ctx, raw) }

// Tiny generated PDFs exercise Poppler rather than a mocked parser. Image pages
// contain an actual raster XObject; blank pages have neither text nor images.
func multiPagePDF(kinds []string) []byte {
	objects := []string{"", ""}
	kids := []string{}
	for _, kind := range kinds {
		page := len(objects) + 1
		contents := page + 1
		font := page + 2
		img := page + 3
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
		resources := fmt.Sprintf("/Font << /F1 %d 0 R >>", font)
		text := ""
		if kind == "text" {
			text = "BT /F1 12 Tf 40 700 Td (A retrieval experiment uses a held out evaluation dataset with enough text to verify complete page extraction and stable evidence.) Tj ET"
		}
		if kind == "image" {
			resources += fmt.Sprintf(" /XObject << /Im1 %d 0 R >>", img)
			text = "q 100 0 0 100 40 500 cm /Im1 Do Q"
		}
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << %s >> /Contents %d 0 R >>", resources, contents), fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(text), text), "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", "<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 1 >>\nstream\n0\nendstream")
	}
	objects[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kinds))
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return []byte(b.String())
}
func TestMixedScannedBlankAndOCRFallback(t *testing.T) {
	for _, tool := range []string{"pdftotext", "pdfinfo", "pdfimages", "prlimit"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " missing")
		}
	}
	for _, tc := range []struct {
		name      string
		kinds     []string
		ocr       bool
		wantError bool
	}{
		{"blank", []string{"text", "blank"}, false, false},
		{"mixed", []string{"text", "image"}, false, true},
		{"scan", []string{"image"}, false, true},
		{"ocr", []string{"image"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(noopLimiter{})
			raw := multiPagePDF(tc.kinds)
			e.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			calls := 0
			if tc.ocr {
				e.OCR = ocrFunc(func(context.Context, []byte) ([]string, error) {
					calls++
					return []string{strings.Repeat("Recognized paper text. ", 10)}, nil
				})
			}
			result, err := e.Extract(context.Background(), domain.Source{ArXivID: "1706.03762", PDFURL: "https://arxiv.org/pdf/1706.03762v1"})
			if (err != nil) != tc.wantError {
				t.Fatalf("%+v %v", result, err)
			}
			if tc.wantError && err.Error() != "ocr_required" {
				t.Fatal(err)
			}
			if !tc.wantError && len(result.Pages) != len(tc.kinds) {
				t.Fatal("lost blank page")
			}
			if tc.ocr && calls != 1 {
				t.Fatal("OCR fallback not invoked")
			}
		})
	}
}

// Layout text looks correct on screen, but flattening it interleaves columns.
// This exercises the PDF adapter through domain chunking, as report inputs do.
func TestTwoColumnPaperKeepsReadingOrder(t *testing.T) {
	raw := singlePagePDF(`BT /F1 10 Tf 40 720 Td (1 Introduction) Tj ET
BT /F1 10 Tf 40 700 Td (Traditional deterministic methods) Tj 0 -14 Td (regress a single solution that ignores) Tj 0 -14 Td (uncertainty in ambiguous scenarios.) Tj ET
BT /F1 10 Tf 330 720 Td (2 Experiments) Tj ET
BT /F1 10 Tf 330 700 Td (The held out evaluation dataset) Tj 0 -14 Td (contains severe occlusions and is used) Tj 0 -14 Td (to verify the recovery accuracy.) Tj ET`)
	e := New(noopLimiter{})
	e.Client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	result, err := e.Extract(t.Context(), domain.Source{ArXivID: "1706.03762", PDFURL: "https://arxiv.org/pdf/1706.03762v1"})
	if err != nil {
		t.Fatal(err)
	}
	chunks := domain.Split("fixture", result.Pages)
	if len(chunks) != 1 {
		t.Fatalf("unexpected chunks: %d", len(chunks))
	}
	for _, sentence := range []string{"Traditional deterministic methods regress a single solution that ignores uncertainty in ambiguous scenarios.", "The held out evaluation dataset contains severe occlusions and is used to verify the recovery accuracy."} {
		if !strings.Contains(chunks[0].Text, sentence) {
			t.Fatalf("column reading order lost: %s", chunks[0].Text)
		}
	}
	sections := domain.IdentifySections(result.Pages)
	if len(sections) != 2 {
		t.Fatalf("section headings mixed: %+v", sections)
	}
}
