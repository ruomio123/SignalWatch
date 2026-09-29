package document

import (
	"strconv"
	"strings"

	domain "signalwatch/internal/document"

	"golang.org/x/net/html"
)

func directCaption(figure *html.Node) *html.Node {
	for child := figure.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "figcaption" {
			return child
		}
	}
	return nil
}

func firstTaggedText(root *html.Node) string {
	if root != nil {
		for _, child := range htmlNodes(root) {
			if htmlClass(child, "ltx_tag") {
				return strings.TrimSpace(strings.TrimSuffix(htmlText(child, false), ":"))
			}
		}
	}
	return ""
}

func tableCaption(figure *html.Node) (string, string) {
	caption := directCaption(figure)
	label, text := firstTaggedText(caption), htmlText(caption, false)
	for parent := figure.Parent; parent != nil; parent = parent.Parent {
		if parent.Data == "figure" && htmlClass(parent, "ltx_table") {
			parentCaption := directCaption(parent)
			parentLabel := firstTaggedText(parentCaption)
			label = strings.TrimSpace(parentLabel + " " + label)
			text = strings.TrimSpace(htmlText(parentCaption, false) + " " + text)
			break
		}
	}
	if label == "" {
		label = "Table " + htmlAttribute(figure, "id")
	}
	return label, text
}

type structuredHTMLCell struct {
	text   string
	header bool
}

func closestHTMLTable(node *html.Node) *html.Node {
	for parent := node.Parent; parent != nil; parent = parent.Parent {
		if parent.Type == html.ElementNode && parent.Data == "table" {
			return parent
		}
	}
	return nil
}

func cellSpan(cell *html.Node, name string, limit int) (int, bool) {
	raw := htmlAttribute(cell, name)
	if raw == "" {
		return 1, true
	}
	value, err := strconv.Atoi(raw)
	return value, err == nil && value >= 1 && value <= limit
}

func parseHTMLTable(figure, root *html.Node) (domain.StructuredElement, string) {
	label, caption := tableCaption(figure)
	element := domain.StructuredElement{Kind: "table", Anchor: htmlAttribute(figure, "id"), Label: label}
	if hasMissingHTMLTeX(figure, false) {
		return element, "missing_tex"
	}
	tables := []*html.Node{}
	for _, child := range htmlNodes(figure) {
		if child.Data == "table" && htmlClass(child, "ltx_tabular") {
			tables = append(tables, child)
		}
	}
	if len(tables) != 1 {
		return element, "unsupported_structure"
	}
	tableNode := tables[0]
	rows := []*html.Node{}
	for _, child := range htmlNodes(tableNode) {
		if child.Data == "tr" && closestHTMLTable(child) == tableNode {
			rows = append(rows, child)
		}
	}
	if len(rows) == 0 || len(rows) > domain.StructuredTableRows+8 {
		return element, "element_budget"
	}
	grid := make([]map[int]structuredHTMLCell, len(rows))
	headerRows := make([]bool, len(rows))
	for i := range grid {
		grid[i] = map[int]structuredHTMLCell{}
	}
	width := 0
	for rowIndex, row := range rows {
		column, cells := 0, 0
		headerRows[rowIndex] = true
		for cell := row.FirstChild; cell != nil; cell = cell.NextSibling {
			if cell.Type != html.ElementNode || (cell.Data != "th" && cell.Data != "td") {
				continue
			}
			cells++
			for _, child := range htmlNodes(cell)[1:] {
				if child.Data == "table" {
					return element, "unsupported_structure"
				}
			}
			header := cell.Data == "th" && !htmlClass(cell, "ltx_th_row")
			if row.Parent != nil && row.Parent.Data == "thead" {
				header = true
			}
			headerRows[rowIndex] = headerRows[rowIndex] && header
			rowspan, rowsOK := cellSpan(cell, "rowspan", domain.StructuredTableRows)
			colspan, colsOK := cellSpan(cell, "colspan", domain.StructuredTableColumns)
			if !rowsOK || !colsOK || rowIndex+rowspan > len(rows) {
				return element, "unsupported_structure"
			}
			for {
				if _, occupied := grid[rowIndex][column]; !occupied {
					break
				}
				column++
			}
			if column+colspan > domain.StructuredTableColumns {
				return element, "element_budget"
			}
			value := structuredHTMLCell{text: htmlText(cell, false), header: header}
			// A body cell spanning numerical columns cannot safely be flattened
			// into repeated values. Retain it as a controlled structural gap.
			if !header && colspan > 1 && value.text != "" {
				return element, "unsupported_structure"
			}
			for y := rowIndex; y < rowIndex+rowspan; y++ {
				for x := column; x < column+colspan; x++ {
					if _, occupied := grid[y][x]; occupied {
						return element, "unsupported_structure"
					}
					grid[y][x] = value
				}
			}
			column += colspan
		}
		if cells == 0 && len(grid[rowIndex]) == 0 {
			return element, "unsupported_structure"
		}
		width = max(width, len(grid[rowIndex]))
	}
	headerCount := 0
	for headerCount < len(rows) && headerRows[headerCount] {
		headerCount++
	}
	if headerCount == 0 || headerCount == len(rows) || headerCount > 8 || len(rows)-headerCount > domain.StructuredTableRows {
		return element, "unsupported_structure"
	}
	table := &domain.StructuredTable{Caption: caption, Headers: make([]string, width), Rows: [][]string{}, Notes: []string{}}
	for column := 0; column < width; column++ {
		path := []string{}
		for row := 0; row < headerCount; row++ {
			cell, ok := grid[row][column]
			if !ok {
				return element, "unsupported_structure"
			}
			if cell.text != "" && (len(path) == 0 || path[len(path)-1] != cell.text) {
				path = append(path, cell.text)
			}
		}
		table.Headers[column] = strings.Join(path, " / ")
		if table.Headers[column] == "" {
			return element, "unsupported_structure"
		}
	}
	for i := headerCount; i < len(grid); i++ {
		values := make([]string, width)
		for column := 0; column < width; column++ {
			cell, ok := grid[i][column]
			if !ok {
				return element, "unsupported_structure"
			}
			values[column] = cell.text
		}
		table.Rows = append(table.Rows, values)
	}
	table.Notes = htmlTableNotes(figure, root)
	element.Table = table
	return element, ""
}

func htmlTableNotes(figure, root *html.Node) []string {
	notes := []string{}
	seen := map[string]bool{}
	appendNote := func(node *html.Node) {
		value := htmlText(node, false)
		if value != "" && !seen[value] {
			notes = append(notes, value)
			seen[value] = true
		}
	}
	byID := map[string]*html.Node{}
	for _, node := range htmlNodes(root) {
		if id := htmlAttribute(node, "id"); id != "" {
			byID[id] = node
		}
	}
	for scope := figure; scope != nil; scope = scope.Parent {
		if scope.Data != "figure" || !htmlClass(scope, "ltx_table") {
			continue
		}
		for _, node := range htmlNodes(scope) {
			owner := node.Parent
			for owner != nil && owner != scope && !(owner.Data == "figure" && htmlClass(owner, "ltx_table")) {
				owner = owner.Parent
			}
			if owner != scope {
				continue // Do not inherit the notes of a sibling panel.
			}
			if htmlClass(node, "ltx_note_outer") || htmlClass(node, "ltx_tablenotes") {
				appendNote(node)
			}
			if node.Data == "a" && (htmlClass(node, "ltx_note_mark") || (node.Parent != nil && htmlClass(node.Parent, "ltx_note_mark"))) {
				href := htmlAttribute(node, "href")
				if target := byID[strings.TrimPrefix(href, "#")]; strings.HasPrefix(href, "#") && target != nil {
					appendNote(target)
				}
			}
		}
	}
	return notes
}

func parseHTMLFormula(node *html.Node) (domain.StructuredElement, string) {
	label := firstTaggedText(node)
	if label != "" {
		label = "Equation " + strings.Trim(label, "() ")
	} else {
		label = "Equation " + htmlAttribute(node, "id")
	}
	element := domain.StructuredElement{Kind: "formula", Anchor: htmlAttribute(node, "id"), Label: label}
	maths := []*html.Node{}
	for _, child := range htmlNodes(node) {
		if child.Type == html.ElementNode && child.Data == "math" {
			maths = append(maths, child)
		}
	}
	if len(maths) != 1 {
		return element, "unsupported_structure"
	}
	tex := htmlTeX(maths[0])
	if strings.TrimSpace(tex) == "" {
		return element, "missing_tex"
	}
	if len(tex) > domain.StructuredFormulaLimit {
		return element, "element_budget"
	}
	contexts := []string{}
	var paragraph *html.Node
	for parent := node.Parent; parent != nil; parent = parent.Parent {
		if htmlClass(parent, "ltx_para") {
			paragraph = parent
			break
		}
	}
	if paragraph != nil {
		if hasMissingHTMLTeX(paragraph, true) {
			return element, "missing_tex"
		}
		if own := htmlText(paragraph, true); own != "" {
			contexts = append(contexts, own)
		}
		// One complete adjacent paragraph carries definitions or conditions.
		// Extra context can be omitted, but no retained paragraph is clipped.
		for next := paragraph.NextSibling; next != nil; next = next.NextSibling {
			if next.Type != html.ElementNode {
				continue
			}
			if htmlClass(next, "ltx_para") {
				value := htmlText(next, true)
				if value != "" && len(strings.Join(contexts, "\n"))+len(value)+1 <= 8192 {
					if hasMissingHTMLTeX(next, true) {
						return element, "missing_tex"
					}
					contexts = append(contexts, value)
				}
			}
			break
		}
	}
	context := strings.Join(contexts, "\n")
	if len(context) > 8192 {
		return element, "element_budget"
	}
	element.Formula = &domain.StructuredFormula{TeX: tex, Context: context}
	return element, ""
}

func hasMissingHTMLTeX(node *html.Node, skipEquations bool) bool {
	if skipEquations && htmlClass(node, "ltx_equation") {
		return false
	}
	if node.Type == html.ElementNode && node.Data == "math" {
		return strings.TrimSpace(htmlTeX(node)) == ""
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if hasMissingHTMLTeX(child, skipEquations) {
			return true
		}
	}
	return false
}
