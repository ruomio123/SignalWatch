package document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	domain "signalwatch/internal/document"
)

var structuredHTMLSource = domain.StructuredSource{DocumentID: "document-1", SourceVersion: "2609.31620v1"}

func structuredHTMLPage(body string) string {
	return `<!doctype html><html><body><div id="watermark-tr">arXiv:2609.31620v1 [cs.CV] 25 Sep 2026</div><article class="ltx_document">` + body + `</article></body></html>`
}

const structuredHTMLTable = `<figure id="S5.T1" class="ltx_table"><figcaption><span class="ltx_tag">Table 1:</span> Grouped evaluation.</figcaption><table class="ltx_tabular"><thead><tr><th rowspan="2">Decoder</th><th colspan="2">fusion <math alttext="k=23"><mi>k</mi></math></th></tr><tr><th>PSNR<math alttext="\uparrow"><mo>↑</mo></math></th><th>rFID<math alttext="\downarrow"><mo>↓</mo></math></th></tr></thead><tbody><tr><th class="ltx_th_row" rowspan="2">FuseReg</th><td>27.52</td><td>0.42</td></tr><tr><td>26.01</td><td>0.51</td></tr></tbody></table><div class="ltx_tablenotes">Same evaluation split.</div></figure>`

func requireStructuredFailure(t *testing.T, err error, code string) {
	t.Helper()
	var failure *domain.ExtractionError
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("error=%v want %s", err, code)
	}
}

func TestStructuredHTMLTableHeadersSpansAndNotes(t *testing.T) {
	raw := structuredHTMLPage(structuredHTMLTable)
	material, err := ParseStructuredHTML(structuredHTMLSource, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(material.Elements) != 1 || len(material.Gaps) != 0 {
		t.Fatalf("material=%+v", material)
	}
	element := material.Elements[0]
	if element.ID != "h-table-S5.T1" || element.Label != "Table 1" {
		t.Fatalf("element=%+v", element)
	}
	wantHeaders := []string{"Decoder", `fusion $k=23$ / PSNR$\uparrow$`, `fusion $k=23$ / rFID$\downarrow$`}
	if !reflect.DeepEqual(element.Table.Headers, wantHeaders) {
		t.Fatalf("headers=%q", element.Table.Headers)
	}
	wantRows := [][]string{{"FuseReg", "27.52", "0.42"}, {"FuseReg", "26.01", "0.51"}}
	if !reflect.DeepEqual(element.Table.Rows, wantRows) || !reflect.DeepEqual(element.Table.Notes, []string{"Same evaluation split."}) {
		t.Fatalf("table=%+v", element.Table)
	}
	if strings.Count(element.Quote, "27.52") != 1 || element.Quote != domain.StructuredQuote(element) {
		t.Fatal("quote lost or duplicated cells")
	}
	hash := sha256.Sum256([]byte(raw))
	if material.ContentHash != hex.EncodeToString(hash[:]) || material.SourceURL != "https://arxiv.org/html/2609.31620v1" || material.ParserVersion != domain.StructuredParserVersion {
		t.Fatalf("provenance=%+v", material)
	}
}

func TestStructuredHTMLFormulaExactTeXAndContext(t *testing.T) {
	tex := `\begin{gathered}\widetilde m_k\sim\mathrm{Bernoulli}(1-p),\\m\sim\operatorname{Law}(\widetilde m\mid\mathbf1^\top\widetilde m>0),\quad z_m=\frac{\sum_k m_kh_k}{\sum_k m_k}.\end{gathered}`
	body := `<div class="ltx_para">Condition on a nonempty mask.<table class="ltx_equation ltx_eqn_table" id="S3.E2"><tr><td><span class="ltx_tag">(2)</span><math alttext="` + tex + `"><semantics><mi>WRONG PRESENTATION</mi><annotation encoding="application/x-tex">duplicate</annotation></semantics></math></td></tr></table></div><div class="ltx_para">At inference all layers are retained.</div>`
	material, err := ParseStructuredHTML(structuredHTMLSource, []byte(structuredHTMLPage(body)))
	if err != nil {
		t.Fatal(err)
	}
	if len(material.Elements) != 1 || len(material.Gaps) != 0 {
		t.Fatalf("material=%+v", material)
	}
	element := material.Elements[0]
	if element.Formula.TeX != tex || element.Label != "Equation 2" || element.ID != "h-formula-S3.E2" {
		t.Fatalf("formula=%+v", element)
	}
	if element.Formula.Context != "Condition on a nonempty mask.\nAt inference all layers are retained." || strings.Contains(element.Quote, "WRONG") || strings.Contains(element.Quote, "duplicate") {
		t.Fatalf("context=%q", element.Formula.Context)
	}
	annotation := `<span class="ltx_equation" id="S3.E3"><math><semantics><mi>x</mi><annotation encoding="application/x-tex">x=\alpha &gt; 0</annotation></semantics></math></span>`
	material, err = ParseStructuredHTML(structuredHTMLSource, []byte(structuredHTMLPage(annotation)))
	if err != nil || len(material.Elements) != 1 || material.Elements[0].Formula.TeX != `x=\alpha > 0` {
		t.Fatalf("annotation=%+v error=%v", material, err)
	}
}

func TestStructuredHTMLVersionCannotComeFromPaperText(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"wrong_banner_quoted_expected", strings.Replace(structuredHTMLPage(`<p>arXiv:2609.31620v1 is cited here.</p>`), `id="watermark-tr">arXiv:2609.31620v1`, `id="watermark-tr">arXiv:2609.31620v2`, 1)},
		{"inside_article", `<article class="ltx_document"><div id="watermark-tr">arXiv:2609.31620v1</div></article>`},
		{"missing_banner", `<article class="ltx_document">arXiv:2609.31620v1</article>`},
		{"duplicate_banner", structuredHTMLPage(`<div id="watermark-tr">arXiv:2609.31620v1</div>`)},
		{"conflicting_banner", strings.Replace(structuredHTMLPage(""), `[cs.CV]`, `arXiv:2609.31620v2`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseStructuredHTML(structuredHTMLSource, []byte(tc.raw))
			requireStructuredFailure(t, err, "structured_source_mismatch")
		})
	}
}

func TestStructuredHTMLPanelsAndControlledGaps(t *testing.T) {
	panel := `<figure class="ltx_table" id="S5.T2"><figcaption><span class="ltx_tag">Table 2:</span> Panel experiment.</figcaption>` + strings.Replace(strings.Replace(structuredHTMLTable, `id="S5.T1"`, `id="S5.T2.a"`, 1), `Table 1:`, `(a)`, 1) + `</figure>`
	material, err := ParseStructuredHTML(structuredHTMLSource, []byte(structuredHTMLPage(panel)))
	if err != nil || len(material.Elements) != 1 {
		t.Fatalf("material=%+v error=%v", material, err)
	}
	if material.Elements[0].Anchor != "S5.T2.a" || material.Elements[0].Label != "Table 2 (a)" || !strings.Contains(material.Elements[0].Table.Caption, "Panel experiment.") {
		t.Fatalf("panel=%+v", material.Elements[0])
	}
	cases := []struct{ name, body, reason string }{
		{"ambiguous_colspan", strings.Replace(structuredHTMLTable, `<td>27.52</td><td>0.42</td>`, `<td colspan="2">27.52</td>`, 1), "unsupported_structure"},
		{"bad_span", strings.Replace(structuredHTMLTable, `rowspan="2"`, `rowspan="0"`, 1), "unsupported_structure"},
		{"missing_tex", `<span class="ltx_equation" id="S3.E4"><math><mi>x</mi></math></span>`, "missing_tex"},
		{"table_missing_tex", strings.Replace(structuredHTMLTable, `alttext="k=23"`, "", 1), "missing_tex"},
		{"multi_math", `<span class="ltx_equation" id="S3.E4"><math alttext="x"/><math alttext="y"/></span>`, "unsupported_structure"},
		{"formula_budget", `<span class="ltx_equation" id="S3.E4"><math alttext="` + strings.Repeat("x", domain.StructuredFormulaLimit+1) + `"/></span>`, "element_budget"},
		{"serialized_element_budget", `<span class="ltx_equation" id="S3.E4"><math alttext="` + strings.Repeat(`\`, 9000) + `"/></span>`, "element_budget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ParseStructuredHTML(structuredHTMLSource, []byte(structuredHTMLPage(tc.body)))
			if err != nil || len(m.Elements) != 0 || len(m.Gaps) != 1 || m.Gaps[0].Reason != tc.reason {
				t.Fatalf("material=%+v error=%v", m, err)
			}
		})
	}
	_, err = ParseStructuredHTML(structuredHTMLSource, []byte(structuredHTMLPage(structuredHTMLTable+structuredHTMLTable)))
	requireStructuredFailure(t, err, "structured_html_invalid")
	_, err = ParseStructuredHTML(structuredHTMLSource, []byte(structuredHTMLPage(strings.Repeat("<div>", 257)+strings.Repeat("</div>", 257))))
	requireStructuredFailure(t, err, "structured_resource_limit")
}

func TestStructuredHTMLTableNotesAndLongEvidenceAreComplete(t *testing.T) {
	longCaption := strings.Repeat("完整上下文", 100)
	body := strings.Replace(structuredHTMLTable, "Grouped evaluation.", longCaption+`<a class="ltx_note_mark" href="#note-1">1</a>`, 1) + `<div id="note-1" class="ltx_note_outer">One exact linked note.</div>`
	material, err := ParseStructuredHTML(structuredHTMLSource, []byte(structuredHTMLPage(body)))
	if err != nil || len(material.Elements) != 1 {
		t.Fatalf("material=%+v error=%v", material, err)
	}
	element := material.Elements[0]
	if len(element.Quote) <= 1000 || !strings.Contains(element.Table.Caption, longCaption) || !reflect.DeepEqual(element.Table.Notes, []string{"One exact linked note.", "Same evaluation split."}) {
		t.Fatalf("caption or notes truncated: %+v", element.Table)
	}
}

type structuredRoundTripper func(*http.Request) (*http.Response, error)

func (f structuredRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type structuredLimiter struct {
	err   error
	calls int
}

func (l *structuredLimiter) Wait(context.Context) error { l.calls++; return l.err }

func TestStructuredHTMLFetchPinnedBoundedAndNoRedirect(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		status                      int
		contentType, body, wantCode string
	}{
		{"ok", 200, "text/html; charset=utf-8", structuredHTMLPage(structuredHTMLTable), ""},
		{"redirect", 302, "text/html", structuredHTMLPage(structuredHTMLTable), "structured_html_unavailable"},
		{"not_found", 404, "text/html", "missing", "structured_html_unavailable"},
		{"wrong_content", 200, "application/pdf", "pdf", "structured_html_invalid"},
		{"too_big", 200, "text/html", strings.Repeat("x", domain.StructuredHTMLLimit+1), "structured_resource_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			limiter := &structuredLimiter{}
			extractor := &Extractor{Limiter: limiter, Client: &http.Client{Transport: structuredRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != domain.StructuredURL(structuredHTMLSource.SourceVersion) || r.Method != "GET" {
					t.Fatalf("request=%v", r)
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.contentType}, "Location": {"https://arxiv.org/html/2609.31620v2"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}}
			material, err := extractor.ExtractStructured(context.Background(), structuredHTMLSource)
			if tc.wantCode == "" {
				if err != nil || len(material.Elements) != 1 {
					t.Fatalf("material=%+v error=%v", material, err)
				}
			} else {
				requireStructuredFailure(t, err, tc.wantCode)
			}
			if calls != 1 || limiter.calls != 1 {
				t.Fatalf("calls=%d limiter=%d", calls, limiter.calls)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	extractor := &Extractor{Limiter: &structuredLimiter{err: context.Canceled}, Client: &http.Client{}}
	_, err := extractor.ExtractStructured(ctx, structuredHTMLSource)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
