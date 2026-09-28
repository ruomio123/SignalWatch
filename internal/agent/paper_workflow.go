package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"signalwatch/internal/document"
	"signalwatch/internal/generation"
	"strings"
	"time"
)

func paperError(code string) error { return &ModelError{Code: code} }
func paperFailureCode(err error) string {
	var output *generation.OutputError
	if errors.As(err, &output) {
		return output.Code
	}
	var model *ModelError
	if errors.As(err, &model) {
		return model.Code
	}
	switch {
	case errors.Is(err, ErrReportRequired):
		return "PAPER_REPORT_REQUIRED"
	case errors.Is(err, ErrOutput):
		return "invalid_output"
	case errors.Is(err, ErrBudget), errors.Is(err, context.DeadlineExceeded):
		return "budget_exhausted"
	case errors.Is(err, ErrConflict), errors.Is(err, ErrLease), errors.Is(err, ErrNotFound), errors.Is(err, context.Canceled):
		return "access_or_configuration_changed"
	default:
		return "storage_failed"
	}
}
func boundedPaperInput(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > paperInputLimit {
		return nil, paperError("context_too_large")
	}
	return raw, nil
}

// paperCall is the sole model-call boundary for both fixed paper workflows.
// Saved outputs are replayed locally; an uncertain remote call is never replayed.
func (s *Service) paperCall(ctx context.Context, r Run, cp *Checkpoint, check func(context.Context) error, stage, prompt string, input any, validate func([]byte) error) ([]byte, error) {
	if raw, ok := cp.Paper.Outputs[stage]; ok {
		return raw, validate(raw)
	}
	raw, err := boundedPaperInput(input)
	if err != nil {
		return nil, err
	}
	limit := 3
	if r.Task == TaskPaperReport {
		limit = 24
	}
	for {
		if cp.Calls >= limit {
			return nil, ErrBudget
		}
		if err := check(ctx); err != nil {
			return nil, err
		}
		started := time.Now()
		result, err := s.Gateway.Generate(ctx, ModelRequest{Run: r, Feature: "paper_qa", System: paperPolicy + "\n" + prompt, Input: raw, Schema: paperStageSchema(stage),
			Before: func(call context.Context) error {
				if err := check(call); err != nil {
					return err
				}
				cp.Phase = "calling"
				return s.Store.Save(call, r, *cp, stage, nil)
			}, Validate: func(res generation.Result) error {
				err := validate(res.Content)
				var failure *generation.OutputError
				if errors.As(err, &failure) {
					cp.Paper.Failure = &PaperFailure{Code: failure.Code, Path: failure.Path, Rule: failure.Rule}
				}
				return err
			}})
		if err != nil {
			var failure *ModelError
			if errors.As(err, &failure) && failure.Admission {
				cp.Phase = "ready"
				if e := s.Store.Save(ctx, r, *cp, "waiting_for_model_slot", nil); e != nil {
					return nil, e
				}
				timer := time.NewTimer(min(max(failure.RetryAfter, time.Second), 2*time.Second))
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
				continue
			}
			cp.Sequence++
			step := Step{RunID: r.ID, Sequence: cp.Sequence, Kind: "model", Tool: stage, CallID: result.CallID, DurationMS: time.Since(started).Milliseconds(), InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, FailureCode: paperFailureCode(err), CreatedAt: time.Now().UTC()}
			_ = s.Store.Save(ctx, r, *cp, "failed", &step)
			return nil, err
		}
		// Validate again for gateways that do not enforce the callback contract.
		if err := validate(result.Content); err != nil {
			return nil, err
		}
		cp.Calls++
		cp.Paper.Outputs[stage] = append(json.RawMessage(nil), result.Content...)
		cp.Phase = "ready"
		cp.Sequence++
		step := Step{RunID: r.ID, Sequence: cp.Sequence, Kind: "model", Tool: stage, CallID: result.CallID, DurationMS: time.Since(started).Milliseconds(), InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, CreatedAt: time.Now().UTC()}
		if err := s.Store.Save(ctx, r, *cp, stage, &step); err != nil {
			return nil, err
		}
		return result.Content, nil
	}
}

func (s *Service) preparePaper(ctx context.Context, r Run, c Conversation, cp *Checkpoint, check func(context.Context) error) (document.Document, []Citation, error) {
	p, err := s.Papers.Get(ctx, r.UserID, *c.PaperID)
	if err != nil {
		return document.Document{}, nil, err
	}
	if cp.Paper == nil {
		cp.Paper = &PaperCheckpoint{Context: contextOf(p), PaperHash: paperSnapshotHash(p), Outputs: map[string]json.RawMessage{}}
		h := sha256.Sum256([]byte(p.Title + "\n" + p.Abstract))
		hash := hex.EncodeToString(h[:])
		cp.Evidence = []Citation{{ID: "abstract", DocumentID: "abstract:" + hash, ContentHash: hash, Quote: p.Title + "\n" + p.Abstract, URL: p.ArXivURL}}
	}
	pc := cp.Paper
	if pc.Outputs == nil {
		return document.Document{}, nil, paperError("invalid_checkpoint")
	}
	if pc.PaperHash != paperSnapshotHash(p) {
		return document.Document{}, nil, ErrConflict
	}
	if r.Task == TaskPaperFollowup {
		message, err := s.Store.LatestPaperReport(ctx, c.ID)
		if errors.Is(err, ErrNotFound) {
			return document.Document{}, nil, ErrReportRequired
		}
		if err != nil {
			return document.Document{}, nil, err
		}
		report, ok := validPaperReport(message, pc.PaperHash)
		if !ok {
			return document.Document{}, nil, ErrReportRequired
		}
		if pc.Mode == "" && report.ContextMode == "abstract" {
			pc.Mode = "abstract"
			pc.FallbackReason = report.FallbackReason
		}
	}
	if pc.Mode == "" && r.ContextMode == "abstract" {
		pc.Mode = "abstract"
	}
	var doc document.Document
	if pc.Mode == "" {
		src := document.Source{PaperID: p.ID, ArXivID: p.ArXivID, PDFURL: p.PDFURL, UpdatedAt: p.ArXivUpdatedAt}
		doc, err = s.Documents.Prepare(ctx, src)
		if err != nil {
			return doc, nil, err
		}
		if cp.DocumentID != "" && cp.DocumentID != doc.ID {
			return doc, nil, ErrConflict
		}
		cp.DocumentID = doc.ID
		if err = s.Store.Save(ctx, r, *cp, "preparing_document", nil); err != nil {
			return doc, nil, err
		}
		preparation, stop := context.WithTimeout(ctx, 105*time.Second)
		defer stop()
		for doc.State != "ready" && doc.State != "failed" {
			if err = check(ctx); err != nil {
				return doc, nil, err
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-preparation.Done():
				timer.Stop()
				if ctx.Err() != nil {
					return doc, nil, ctx.Err()
				}
				pc.Mode = "abstract"
				pc.FallbackReason = "document_timeout"
			case <-timer.C:
			}
			if pc.Mode == "abstract" {
				break
			}
			doc, err = s.Documents.Get(ctx, doc.ID)
			if err != nil {
				return doc, nil, err
			}
		}
		if pc.Mode == "" {
			if doc.State == "ready" && doc.TextComplete {
				pc.Mode = "fulltext"
				pc.SourceVersion = doc.SourceVersion
				pc.ContentHash = doc.ContentHash
				pc.Sections = doc.Sections
			} else {
				pc.Mode = "abstract"
				pc.FallbackReason = doc.FailureCode
				if pc.FallbackReason == "" {
					pc.FallbackReason = "incomplete_text"
				}
			}
		}
		if pc.Mode == "abstract" {
			cp.DocumentID = ""
		}
	}
	if err = check(ctx); err != nil {
		return doc, nil, err
	}
	if err = s.Store.Save(ctx, r, *cp, "planning_paper", nil); err != nil {
		return doc, nil, err
	}
	if pc.Mode == "abstract" {
		return document.Document{}, paperEvidence(cp.Evidence), nil
	}
	doc, err = s.Documents.Get(ctx, cp.DocumentID)
	if err != nil {
		return doc, nil, err
	}
	if doc.ParserVersion != document.ParserVersion {
		return doc, nil, paperError("workflow_changed")
	}
	if doc.State != "ready" || !doc.TextComplete || doc.ContentHash != pc.ContentHash || doc.SourceVersion != pc.SourceVersion {
		return doc, nil, ErrConflict
	}
	chunks, err := s.Documents.Chunks(ctx, doc.ID)
	if err != nil {
		return doc, nil, err
	}
	if len(chunks) == 0 {
		return doc, nil, paperError("incomplete_text")
	}
	evidence := make([]Citation, 0, len(chunks))
	for _, chunk := range chunks {
		evidence = append(evidence, Citation{ID: fmt.Sprintf("p%d-c%d", chunk.Page, chunk.Number), DocumentID: doc.ID, ContentHash: doc.ContentHash, Page: chunk.Page, Quote: chunk.Text, URL: fmt.Sprintf("https://arxiv.org/pdf/%s#page=%d", doc.SourceVersion, chunk.Page)})
	}
	return doc, paperEvidence(evidence), nil
}

func paperInput(pc *PaperCheckpoint, field string, evidence []Citation) map[string]any {
	input := map[string]any{"goal": PaperGoal, "paper": pc.Context, "context_mode": pc.Mode, "field": field, "sections": pc.Sections, "evidence": evidencePassages(evidence)}
	if field != "answer" {
		input["output_language"] = "zh-CN"
	}
	return input
}

// partitionPaper fits whole source chunks into bounded requests. No passage is
// dropped, including unrecognized sections and appendices.
func partitionPaper(pc *PaperCheckpoint, evidence []Citation) ([][]Citation, error) {
	batches := [][]Citation{}
	current := []Citation{}
	for _, ref := range evidence {
		candidate := append(append([]Citation{}, current...), ref)
		if _, err := boundedPaperInput(paperInput(pc, "all_fields", candidate)); err != nil {
			if len(current) == 0 {
				return nil, err
			}
			batches = append(batches, current)
			current = []Citation{ref}
			if _, err := boundedPaperInput(paperInput(pc, "all_fields", current)); err != nil {
				return nil, err
			}
		} else {
			current = candidate
		}
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	if len(batches) > 18 {
		return nil, ErrBudget
	}
	return batches, nil
}

func (s *Service) processPaper(ctx context.Context, r Run, c Conversation, cp *Checkpoint, check func(context.Context) error) error {
	doc, evidence, err := s.preparePaper(ctx, r, c, cp, check)
	if err != nil {
		return err
	}
	fields := paperFields
	analyses := map[string]FieldAnalysis{}
	available := map[string][]Citation{}
	pc := cp.Paper
	if r.Task == TaskPaperReport {
		batches, err := partitionPaper(pc, evidence)
		if err != nil {
			return err
		}
		if len(batches) > 1 {
			pc.BatchTotal = len(batches)
		}
		if err = s.Store.Save(ctx, r, *cp, "planning_paper", nil); err != nil {
			return err
		}
		if len(batches) == 1 {
			for _, field := range fields {
				available[field] = evidence
			}
		} else {
			index := evidenceIndex(evidence)
			for i, batch := range batches {
				stage := fmt.Sprintf("extracting_batch_%d", i+1)
				raw, err := s.paperCall(ctx, r, cp, check, stage, batchPrompt, paperInput(pc, "all_fields", batch), func(raw []byte) error { _, e := decodeBatch(raw, batch); return e })
				if err != nil {
					return err
				}
				candidates, _ := decodeBatch(raw, batch)
				for _, field := range fields {
					for _, ref := range candidates[field] {
						available[field] = append(available[field], index[ref.ID])
					}
				}
				pc.BatchCompleted = i + 1
				if err = s.Store.Save(ctx, r, *cp, stage, nil); err != nil {
					return err
				}
			}
		}
		// Reject oversized aggregation before paying for any final field analysis.
		for _, field := range fields {
			if _, err := boundedPaperInput(paperInput(pc, field, available[field])); err != nil {
				return err
			}
		}
	} else {
		fields = []string{"answer"}
		history, err := s.Store.History(ctx, c.ID)
		if err != nil {
			return err
		}
		past := []map[string]string{}
		budget := 24000
		for i := len(history) - 1; i >= 0; i-- {
			if len(history[i].Content) > budget {
				break
			}
			budget -= len(history[i].Content)
			past = append([]map[string]string{{"role": history[i].Role, "content": history[i].Content}}, past...)
		}
		input := map[string]any{"paper": pc.Context, "question": r.Question, "history": past}
		decode := func(raw []byte) (string, string, error) {
			var value questionOutput
			if err := paperSchemaJSON(raw, &value, questionSchema); err != nil {
				return "", "", err
			}
			if strings.TrimSpace(value.Question) == "" || len(value.Question) > 8000 || strings.TrimSpace(value.Query) == "" || len(value.Query) > 1000 {
				return "", "", ErrOutput
			}
			return value.Question, value.Query, nil
		}
		raw, err := s.paperCall(ctx, r, cp, check, "normalizing_question", `Resolve pronouns using history, preserve the user's question and language, and provide English search terms. Return question (standalone user question) and query (English keywords) according to the output JSON Schema. Do not answer or choose tools.`, input, func(raw []byte) error { _, _, err := decode(raw); return err })
		if err != nil {
			return err
		}
		question, query, _ := decode(raw)
		if pc.Mode == "fulltext" {
			chunks, err := s.Documents.Chunks(ctx, doc.ID)
			if err != nil {
				return err
			}
			selected := document.Search(chunks, query, 6)
			for _, chunk := range selected {
				prefix := fmt.Sprintf("p%d-c%d-s", chunk.Page, chunk.Number)
				for _, source := range evidence {
					if strings.HasPrefix(source.ID, prefix) {
						available["answer"] = append(available["answer"], source)
					}
				}
			}
		} else {
			available["answer"] = evidence
		}
		request := paperInput(pc, "answer", available["answer"])
		request["question"] = question
		request["original_question"] = r.Question
		request["coverage"] = "retrieved_passages"
		raw, err = s.paperCall(ctx, r, cp, check, "analyzing_answer", followupFieldPrompt, request, func(raw []byte) error { _, err := decodeField(raw, available["answer"]); return err })
		if err != nil {
			return err
		}
		analyses["answer"], _ = decodeField(raw, available["answer"])
	}
	if r.Task == TaskPaperReport {
		for _, field := range fields {
			raw, err := s.paperCall(ctx, r, cp, check, "analyzing_"+field, reportFieldPrompt, paperInput(pc, field, available[field]), func(raw []byte) error { _, err := decodeReportField(raw, available[field]); return err })
			if err != nil {
				return err
			}
			analyses[field], _ = decodeReportField(raw, available[field])
		}
	}
	claims := reviewClaims(fields, analyses)
	// Review the actual server-owned passages, once each. Claims contain only
	// references, avoiding duplicate source text in the bounded reviewer input.
	supporting := []Citation{}
	seen := map[string]bool{}
	for _, field := range fields {
		index := evidenceIndex(available[field])
		for _, claim := range analyses[field].Claims {
			for _, ref := range claim.Evidence {
				if !seen[ref.ID] {
					supporting = append(supporting, index[ref.ID])
					seen[ref.ID] = true
				}
			}
		}
	}
	reviewInput := map[string]any{"paper": pc.Context, "context_mode": pc.Mode, "claims": claims, "source_passages": evidencePassages(supporting)}
	raw, err := s.paperCall(ctx, r, cp, check, "validating_paper", verdictPrompt, reviewInput, func(raw []byte) error { _, err := decodeVerdicts(raw, claims); return err })
	if err != nil {
		return err
	}
	verdicts, _ := decodeVerdicts(raw, claims)
	content, result, citations := renderPaperResult(r, cp, fields, analyses, available, verdicts)
	if err = check(ctx); err != nil {
		return err
	}
	encoded, _ := json.Marshal(result)
	refs, _ := json.Marshal(citations)
	message := Message{ConversationID: c.ID, RunID: r.ID, Role: "assistant", Content: content, Result: encoded, Citations: refs, Provider: r.Provider, Model: r.Model, CreatedAt: time.Now().UTC()}
	return s.Store.Finish(ctx, r, "completed", "", &message)
}

func renderPaperResult(r Run, cp *Checkpoint, fields []string, analyses map[string]FieldAnalysis, available map[string][]Citation, verdicts map[string]bool) (string, PaperResult, []Citation) {
	pc := cp.Paper
	coverage := "all_extracted_text"
	if r.Task == TaskPaperFollowup {
		coverage = "retrieved_passages"
	}
	if pc.Mode == "abstract" {
		coverage = "abstract_only"
	}
	result := PaperResult{Fields: map[string]PaperFieldResult{}, ContextMode: pc.Mode, FallbackReason: pc.FallbackReason, DocumentID: cp.DocumentID, SourceVersion: pc.SourceVersion, ContentHash: pc.ContentHash, PaperHash: pc.PaperHash, WorkflowVersion: PaperWorkflowVersion, Coverage: coverage}
	values := map[string]string{}
	citations := []Citation{}
	parts := []string{}
	if pc.Mode == "abstract" {
		parts = append(parts, "仅基于摘要；以下缺失判断仅针对当前材料，不代表论文全文没有相关内容。")
	}
	for _, field := range fields {
		analysis := analyses[field]
		status := analysis.Status
		texts := []string{}
		ids := []string{}
		index := evidenceIndex(available[field])
		for i, claim := range analysis.Claims {
			if !verdicts[fmt.Sprintf("%s-%d", field, i+1)] {
				continue
			}
			markers := []string{}
			for j, ref := range claim.Evidence {
				cite := index[ref.ID]
				cite.Quote = ref.Quote
				cite.ID = fmt.Sprintf("%s-%d-%d", field, i+1, j+1)
				citations = append(citations, cite)
				ids = append(ids, cite.ID)
				markers = append(markers, "["+cite.ID+"]")
			}
			texts = append(texts, claim.Text+" "+strings.Join(markers, " "))
		}
		if len(texts) == 0 {
			if status == "not_stated" {
				texts = []string{"论文未明确说明"}
			} else {
				status = "insufficient_evidence"
				texts = []string{"当前材料不足以可靠回答"}
			}
		}
		values[field] = strings.Join(texts, "\n\n")
		result.Fields[field] = PaperFieldResult{Status: status, CitationIDs: ids}
		parts = append(parts, "## "+paperLabels[field]+"\n\n"+values[field])
	}
	if r.Task == TaskPaperReport {
		result.Report = &PaperReport{values["problem"], values["method"], values["experiments"], values["results"], values["limitations"]}
	}
	// Link definitions keep copied Markdown useful outside the application.
	links := []string{}
	for _, ref := range citations {
		links = append(links, fmt.Sprintf("[%s]: %s", ref.ID, ref.URL))
	}
	if len(links) > 0 {
		parts = append(parts, strings.Join(links, "\n"))
	}
	return strings.Join(parts, "\n\n"), result, citations
}
