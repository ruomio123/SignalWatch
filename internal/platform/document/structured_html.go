package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	domain "signalwatch/internal/document"

	"golang.org/x/net/html"
)

// ExtractStructured fetches only the already pinned version. It never follows
// a latest-version redirect, external assets or links supplied by paper text.
func (e *Extractor) ExtractStructured(ctx context.Context, source domain.StructuredSource) (domain.StructuredMaterial, error) {
	if err := domain.ValidateStructuredSource(source); err != nil {
		return domain.StructuredMaterial{}, err
	}
	if e == nil || e.Limiter == nil || e.Client == nil {
		return domain.StructuredMaterial{}, structuredFailure("structured_html_unavailable")
	}
	if err := e.Limiter.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return domain.StructuredMaterial{}, ctx.Err()
		}
		return domain.StructuredMaterial{}, structuredFailure("structured_html_unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, domain.StructuredURL(source.SourceVersion), nil)
	if err != nil {
		return domain.StructuredMaterial{}, structuredFailure("structured_html_unavailable")
	}
	request.Header.Set("User-Agent", "SignalWatch/1.0 (on-demand structured paper reading)")
	client := *e.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return domain.StructuredMaterial{}, ctx.Err()
		}
		return domain.StructuredMaterial{}, structuredFailure("structured_html_unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return domain.StructuredMaterial{}, structuredFailure("structured_html_unavailable")
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || (mediaType != "text/html" && mediaType != "application/xhtml+xml") {
		return domain.StructuredMaterial{}, structuredFailure("structured_html_invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, domain.StructuredHTMLLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			return domain.StructuredMaterial{}, ctx.Err()
		}
		return domain.StructuredMaterial{}, structuredFailure("structured_html_unavailable")
	}
	if err := ctx.Err(); err != nil {
		return domain.StructuredMaterial{}, err
	}
	return ParseStructuredHTML(source, raw)
}

func structuredFailure(code string) error { return &domain.ExtractionError{Code: code} }

// ParseStructuredHTML is also the offline fixture entry point. It executes no
// HTML, TeX, JavaScript, external URLs or model calls.
func ParseStructuredHTML(source domain.StructuredSource, raw []byte) (domain.StructuredMaterial, error) {
	var material domain.StructuredMaterial
	if err := domain.ValidateStructuredSource(source); err != nil {
		return material, err
	}
	if len(raw) == 0 || len(raw) > domain.StructuredHTMLLimit || !boundedHTMLTokens(raw) {
		return material, structuredFailure("structured_resource_limit")
	}
	root, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return material, structuredFailure("structured_html_invalid")
	}
	nodes := htmlNodes(root)
	var article *html.Node
	var watermark *html.Node
	for _, node := range nodes {
		if node.Type == html.ElementNode && node.Data == "article" && htmlClass(node, "ltx_document") {
			if article != nil {
				return material, structuredFailure("structured_html_invalid")
			}
			article = node
		}
		if node.Type == html.ElementNode && htmlAttribute(node, "id") == "watermark-tr" {
			if watermark != nil {
				return material, structuredFailure("structured_source_mismatch")
			}
			watermark = node
		}
	}
	if article == nil {
		return material, structuredFailure("structured_html_invalid")
	}
	if watermark == nil {
		return material, structuredFailure("structured_source_mismatch")
	}
	for parent := watermark.Parent; parent != nil; parent = parent.Parent {
		if parent == article {
			return material, structuredFailure("structured_source_mismatch")
		}
	}
	versionFields := strings.Fields(htmlText(watermark, false))
	if len(versionFields) == 0 || versionFields[0] != "arXiv:"+source.SourceVersion {
		return material, structuredFailure("structured_source_mismatch")
	}
	for _, field := range versionFields[1:] {
		if strings.HasPrefix(field, "arXiv:") {
			return material, structuredFailure("structured_source_mismatch")
		}
	}
	hash := sha256.Sum256(raw)
	material = domain.StructuredMaterial{DocumentID: source.DocumentID, SourceVersion: source.SourceVersion, ParserVersion: domain.StructuredParserVersion, ContentHash: hex.EncodeToString(hash[:]), SourceURL: domain.StructuredURL(source.SourceVersion), Elements: []domain.StructuredElement{}, Gaps: []domain.StructuredGap{}}
	seen := map[string]bool{}
	add := func(element domain.StructuredElement, reason string) error {
		if element.Anchor == "" {
			return structuredFailure("structured_html_invalid")
		}
		element.ID = domain.StructuredElementID(element.Kind, element.Anchor)
		if seen[element.Anchor] {
			return structuredFailure("structured_html_invalid")
		}
		seen[element.Anchor] = true
		element.Quote = domain.StructuredQuote(element)
		encoded, _ := json.Marshal(element)
		if len(element.Quote) > domain.StructuredElementLimit || len(encoded) > domain.StructuredElementLimit {
			reason = "element_budget"
		}
		if reason == "" && domain.ValidateStructuredElement(element) != nil {
			reason = "unsupported_structure"
		}
		if reason == "" {
			material.Elements = append(material.Elements, element)
		} else {
			material.Gaps = append(material.Gaps, domain.StructuredGap{Kind: element.Kind, Anchor: element.Anchor, Reason: reason})
		}
		if len(material.Elements)+len(material.Gaps) > domain.StructuredElementCount {
			return structuredFailure("structured_resource_limit")
		}
		return nil
	}
	for _, node := range htmlNodes(article) {
		if node.Type != html.ElementNode {
			continue
		}
		if node.Data == "figure" && htmlClass(node, "ltx_table") {
			if hasNestedTableFigure(node) {
				continue // panels carry their own anchors and inherit parent caption
			}
			element, reason := parseHTMLTable(node, root)
			if err := add(element, reason); err != nil {
				return domain.StructuredMaterial{}, err
			}
		} else if htmlClass(node, "ltx_equation") && htmlAttribute(node, "id") != "" && !hasEquationAncestor(node) {
			element, reason := parseHTMLFormula(node)
			if err := add(element, reason); err != nil {
				return domain.StructuredMaterial{}, err
			}
		}
	}
	if err := domain.ValidateStructuredMaterial(source, material); err != nil {
		return domain.StructuredMaterial{}, err
	}
	return material, nil
}

// Reject extreme token counts/nesting before allocating a DOM. The format is
// bounded independently of the HTTP response and of each extracted element.
func boundedHTMLTokens(raw []byte) bool {
	tokens := html.NewTokenizer(bytes.NewReader(raw))
	depth, count := 0, 0
	for {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			return tokens.Err() == io.EOF
		}
		count++
		if count > 200000 {
			return false
		}
		if kind == html.StartTagToken {
			name, _ := tokens.TagName()
			switch string(name) {
			case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
			default:
				depth++
			}
		} else if kind == html.EndTagToken {
			depth = max(0, depth-1)
		}
		if depth > 256 {
			return false
		}
	}
}

func htmlAttribute(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}
	return ""
}

func htmlClass(node *html.Node, class string) bool {
	for _, value := range strings.Fields(htmlAttribute(node, "class")) {
		if value == class {
			return true
		}
	}
	return false
}

func htmlNodes(root *html.Node) []*html.Node {
	result := []*html.Node{}
	stack := []*html.Node{root}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		result = append(result, node)
		for child := node.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, child)
		}
	}
	return result
}

func htmlTeX(node *html.Node) string {
	if text := htmlAttribute(node, "alttext"); strings.TrimSpace(text) != "" {
		return text
	}
	for _, child := range htmlNodes(node) {
		if child.Type == html.ElementNode && child.Data == "annotation" && htmlAttribute(child, "encoding") == "application/x-tex" {
			var text strings.Builder
			for _, token := range htmlNodes(child) {
				if token.Type == html.TextNode {
					text.WriteString(token.Data)
				}
			}
			return text.String()
		}
	}
	return ""
}

// Read text without executing markup or double-counting MathML presentation
// and its TeX annotation. skipEquations is used for prose around a formula.
func htmlText(root *html.Node, skipEquations bool) string {
	if root == nil {
		return ""
	}
	var text strings.Builder
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
			return
		}
		if node.Type == html.ElementNode {
			switch node.Data {
			case "script", "style", "nav", "button", "input", "textarea", "annotation":
				return
			case "math":
				text.WriteString("$" + htmlTeX(node) + "$")
				return
			}
			if skipEquations && htmlClass(node, "ltx_equation") {
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
		if node.Data == "br" || node.Data == "p" || node.Data == "div" || node.Data == "figcaption" {
			text.WriteByte(' ')
		}
	}
	visit(root)
	return strings.Join(strings.Fields(text.String()), " ")
}

func hasNestedTableFigure(node *html.Node) bool {
	for _, child := range htmlNodes(node)[1:] {
		if child.Type == html.ElementNode && child.Data == "figure" && htmlClass(child, "ltx_table") {
			return true
		}
	}
	return false
}

func hasEquationAncestor(node *html.Node) bool {
	for parent := node.Parent; parent != nil; parent = parent.Parent {
		if htmlClass(parent, "ltx_equation") && htmlAttribute(parent, "id") != "" {
			return true
		}
	}
	return false
}
