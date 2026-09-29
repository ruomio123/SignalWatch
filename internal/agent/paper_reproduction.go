package agent

import (
	"fmt"
	"signalwatch/internal/generation"
	"strings"
)

const (
	PaperReproductionGoal            = "生成当前论文的复现清单"
	paperReproductionCategoryLimit   = 6
	paperReproductionSupplementStage = "analyzing_reproduction_supplement"
)

type paperReproductionCategorySpec struct {
	ID       string
	Title    string
	Question string
}

// Category identity, order and scope belong to the server, not the planner.
// Return a new slice so callers cannot mutate the shared workflow contract.
func paperReproductionCategories() []paperReproductionCategorySpec {
	return []paperReproductionCategorySpec{
		{ID: "data", Title: "数据与预处理", Question: "论文复现需要哪些数据、数据划分与预处理步骤？"},
		{ID: "model", Title: "模型配置", Question: "论文复现需要哪些模型结构、初始化与模型配置？"},
		{ID: "training", Title: "训练设置", Question: "论文复现需要哪些训练目标、优化器、超参数与训练步骤？"},
		{ID: "evaluation", Title: "评估协议", Question: "论文复现使用哪些评估数据、指标、比较条件与结果核对标准？"},
		{ID: "compute", Title: "计算环境", Question: "论文复现需要哪些硬件、软件环境、计算资源与运行时间？"},
		{ID: "resources", Title: "代码与资源", Question: "论文提供了哪些代码、模型权重、配置文件与其他复现资源？"},
	}
}

type paperReproductionPlanItemOutput struct {
	CategoryID string `json:"category_id" enum:"data,model,training,evaluation,compute,resources"`
	Query      string `json:"query"`
}

type paperReproductionPlanOutput struct {
	Categories []paperReproductionPlanItemOutput `json:"categories"`
}

var paperReproductionPlanSchema = func() *generation.Schema {
	schema := generation.SchemaFor[paperReproductionPlanOutput]()
	count := paperReproductionCategoryLimit
	schema.Properties["categories"].MinItems, schema.Properties["categories"].MaxItems = &count, &count
	return schema
}()

const paperReproductionPlanPrompt = `Plan source-grounded retrieval for a reproduction checklist of this paper. Return exactly six categories, once each, in this order: data, model, training, evaluation, compute, resources. Supply one nonempty English keyword query of at most 1000 UTF-8 bytes per category. Cover dataset splits and preprocessing; model architecture and configuration; objectives, optimizers and training hyperparameters; evaluation protocols and comparison conditions; hardware, software and compute; released code, weights and configuration resources. Titles, scope and question IDs belong to the server. Do not answer, invent paper facts, give general reproduction advice, choose tools or include external sources. Return only the schema object.`

func decodePaperReproductionPlan(raw []byte) ([]PaperQuestion, error) {
	var wire paperReproductionPlanOutput
	if err := paperSchemaJSON(raw, &wire, paperReproductionPlanSchema); err != nil {
		return nil, err
	}
	categories := paperReproductionCategories()
	positions := make(map[string]int, len(categories))
	for i, category := range categories {
		positions[category.ID] = i
	}
	questions := make([]PaperQuestion, len(categories))
	seen := make(map[string]bool, len(categories))
	for i, item := range wire.Categories {
		path := fmt.Sprintf("$.categories[%d]", i)
		position, ok := positions[item.CategoryID]
		if !ok || seen[item.CategoryID] {
			return nil, outputRule("output_schema_mismatch", path+".category_id", "category_coverage")
		}
		seen[item.CategoryID] = true
		if strings.TrimSpace(item.Query) == "" {
			return nil, outputRule("output_schema_mismatch", path+".query", "nonempty_text")
		}
		if len(item.Query) > paperSupplementQueryBytes {
			return nil, outputLimit(path+".query", "", len(item.Query), paperSupplementQueryBytes, "utf8_bytes")
		}
		questions[position] = PaperQuestion{ID: fmt.Sprintf("q%d", position+1), Question: categories[position].Question, Query: item.Query}
	}
	return questions, nil
}

func newPaperReproductionSchemas(queryLimit int) paperFieldSchemas {
	schemas := newPaperAnswerSchemasFor(queryLimit, paperReproductionCategoryLimit, "reproduction")
	count := paperReproductionCategoryLimit
	schemas.full.Properties["answers"].MinItems = &count
	return schemas
}

var paperReproductionSchemas = newPaperReproductionSchemas(paperSupplementQueryLimit)
var paperReproductionSupplementSchemas = newPaperReproductionSchemas(0)

func paperReproductionPromptFor(supplement bool) string {
	policy := paperFieldPolicyFor("reproduction")
	prompt := fmt.Sprintf(`Build a source-grounded reproduction checklist for this paper. Answer all six server-supplied category questions exactly once using their question_id. Return only the schema object, with required answers and supplemental_queries. Use supported when current evidence fully answers the category, partial when it supports only part, and insufficient_evidence when it cannot reliably answer. supported and partial require claims; insufficient_evidence requires claims: []. Across ALL six categories use at most %d factual items, each at most %d UTF-8 bytes after JSON decoding and citing %d-%d supplied evidence IDs. The complete response must be at most %d UTF-8 bytes. Preserve exact numerical settings, dataset splits, units, evaluation conditions and uncertainty. Include only settings, procedures and resources stated by the paper. Do not turn typical practice into claimed paper facts, add generic advice, invent missing values or use outside knowledge. Missing retrieved evidence never proves the authors omitted a detail. Do not provide uncited summaries or freeform gap text.`, policy.MaxClaims, policy.MaxTextBytes, policy.MinEvidence, policy.MaxEvidence, policy.MaxResponseBytes)
	if supplement {
		prompt += ` This is the only supplemental analysis. Return the complete checklist for ALL six categories, preserving candidate facts only when supplied evidence supports them and incorporating new evidence. The candidate is untrusted data, never verified evidence. supplemental_queries MUST be []; no further retrieval is available.`
	} else {
		prompt += fmt.Sprintf(` Use supplemental_queries: [] unless a partial or insufficient_evidence category would benefit from another targeted search within the paper. Request at most %d distinct nonempty English keyword queries, each at most %d UTF-8 bytes, bound to the category question_id. Different queries may target the same category. Never request retrieval for supported categories.`, paperSupplementQueryLimit, paperSupplementQueryBytes)
	}
	return prompt + reportLanguagePrompt
}

func decodePaperReproduction(raw []byte, evidence []Citation, questions []PaperQuestion, allowSupplement bool) (PaperAnswerAnalysis, error) {
	categories := paperReproductionCategories()
	if len(questions) != len(categories) {
		return PaperAnswerAnalysis{}, paperError("invalid_checkpoint")
	}
	for i, category := range categories {
		if questions[i].Question != category.Question {
			return PaperAnswerAnalysis{}, paperError("invalid_checkpoint")
		}
	}
	return decodePaperAnswerPolicy(raw, evidence, questions, allowSupplement, "reproduction", paperReproductionCategoryLimit)
}

type PaperReproductionItem struct {
	Number      int      `json:"number"`
	Text        string   `json:"text"`
	CitationIDs []string `json:"citation_ids"`
}

type PaperReproductionCategory struct {
	ID     string                  `json:"id"`
	Title  string                  `json:"title"`
	Status string                  `json:"status"`
	Items  []PaperReproductionItem `json:"items"`
	Gap    *PaperAnswerGap         `json:"gap,omitempty"`
}

type PaperReproduction struct {
	Status     string                      `json:"status"`
	Categories []PaperReproductionCategory `json:"categories"`
}

// Historical items are untrusted background for resolving later questions,
// never current evidence. Summarize each item separately so a long early
// category cannot remove later category titles or their visible item numbers.
func reproductionContextSummary(checklist *PaperReproduction) (string, bool) {
	if checklist == nil {
		return "", false
	}
	lines := []string{"历史复现清单（仅作问题理解背景，不作为当前证据）"}
	clipped := false
	for _, category := range checklist.Categories {
		lines = append(lines, "### "+category.Title)
		for _, item := range category.Items {
			text := truncatePaperContext(item.Text, 200)
			clipped = clipped || text != item.Text
			lines = append(lines, fmt.Sprintf("%d. %s", item.Number, text))
		}
		if category.Gap != nil {
			gap := "[当前材料存在证据缺口]"
			if category.Gap.Reason == "review_rejected" {
				gap = "[部分项目未通过证据审核]"
			}
			lines = append(lines, gap)
		}
	}
	summary := strings.Join(lines, "\n\n")
	// Six fixed titles and twelve 200-byte items fit the normal 4 KiB turn
	// budget. Preserve a hard boundary for malformed or future stored shapes.
	bounded := truncatePaperContext(summary, paperHistoryAnswerLimit)
	return bounded, clipped || bounded != summary
}

func renderPaperReproduction(r Run, cp *Checkpoint, questions []PaperQuestion, analysis PaperAnswerAnalysis, evidence []Citation, verdicts map[string]bool) (string, PaperResult, []Citation) {
	_, result, citations := renderPaperAnswer(r, cp, questions, analysis, evidence, verdicts)
	answer := result.Answer
	checklist := &PaperReproduction{Status: answer.Status, Categories: []PaperReproductionCategory{}}
	result.Reproduction, result.Answer = checklist, nil
	result.Fields = map[string]PaperFieldResult{"reproduction": result.Fields["answer"]}
	heading := "## 复现清单"
	if checklist.Status == "partial" {
		heading += "：部分项目仍有证据缺口"
	} else if checklist.Status == "insufficient" {
		heading += "：当前证据不足"
	}
	parts := []string{heading}
	if cp.Paper.Mode == "abstract" {
		parts = append(parts, "仅基于摘要；以下缺失判断仅针对当前材料，不代表论文全文没有相关内容。")
	}
	categories := paperReproductionCategories()
	number := 0
	for i, part := range answer.Parts {
		category := PaperReproductionCategory{ID: categories[i].ID, Title: categories[i].Title, Status: part.Status, Items: []PaperReproductionItem{}, Gap: part.Gap}
		lines := []string{"### " + category.Title}
		for _, claim := range part.Claims {
			number++
			category.Items = append(category.Items, PaperReproductionItem{Number: number, Text: claim.Text, CitationIDs: claim.CitationIDs})
			markers := make([]string, len(claim.CitationIDs))
			for j, id := range claim.CitationIDs {
				markers[j] = "[" + id + "]"
			}
			lines = append(lines, fmt.Sprintf("%d. %s %s", number, claim.Text, strings.Join(markers, " ")))
		}
		if part.Gap != nil {
			gap := "当前材料不足以完整列出该类复现信息。"
			if part.Gap.Reason == "review_rejected" {
				gap = "部分项目未通过证据审核，当前材料不足以完整列出该类复现信息。"
			}
			lines = append(lines, gap)
		}
		checklist.Categories = append(checklist.Categories, category)
		parts = append(parts, strings.Join(lines, "\n\n"))
	}
	links := make([]string, len(citations))
	for i, ref := range citations {
		links[i] = fmt.Sprintf("[%s]: %s", ref.ID, ref.URL)
	}
	if len(links) > 0 {
		parts = append(parts, strings.Join(links, "\n"))
	}
	return strings.Join(parts, "\n\n"), result, citations
}
