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

func paperCallLimit(task string) int {
	if task == TaskPaperReport {
		return 30
	}
	return 5
}

func paperFailureDetail(err error) *PaperFailure {
	var failure *generation.OutputError
	if !errors.As(err, &failure) {
		return nil
	}
	return &PaperFailure{Code: failure.Code, Path: failure.Path, Rule: failure.Rule, Count: failure.Count, Limit: failure.Limit, Unit: failure.Unit}
}

// A durable terminal failure prevents a crash between recording a known result
// and finishing the run from replaying the paid call.
func (s *Service) failPaperStage(ctx context.Context, r Run, cp *Checkpoint, stage string, err error, step *Step) error {
	code := paperFailureCode(err)
	cp.Paper.CurrentStage = stage
	cp.Paper.TerminalFailure = code
	cp.Phase = "failed"
	if cp.Paper.Failure == nil || cp.Paper.Failure.Code != code {
		cp.Paper.Failure = paperFailureDetail(err)
	}
	if repair := cp.Paper.Repair; repair != nil && (repair.State == "calling" || repair.State == "pending") {
		repair.State = "failed"
	}
	if step == nil {
		cp.Sequence++
		step = &Step{RunID: r.ID, Sequence: cp.Sequence, Kind: "workflow", Tool: stage, FailureCode: code, CreatedAt: time.Now().UTC()}
	}
	if saveErr := s.Store.Save(ctx, r, *cp, "failed", step); saveErr != nil {
		return saveErr
	}
	return err
}

// paperCall is the sole model-call boundary for both fixed paper workflows.
// Saved outputs are replayed locally; an uncertain remote call is never replayed.
func (s *Service) paperCall(ctx context.Context, r Run, cp *Checkpoint, check func(context.Context) error, stage, prompt string, input any, validate func([]byte) error) ([]byte, error) {
	if raw, ok := cp.Paper.Outputs[stage]; ok {
		return raw, validate(raw)
	}
	if repair := cp.Paper.Repair; repair != nil && repair.Stage == stage {
		return s.runPaperRepair(ctx, r, cp, check)
	}
	var raw []byte
	var err error
	if saved, ok := input.(paperSerializedInput); ok {
		raw = []byte(saved)
		if len(raw) > paperInputLimit {
			err = paperError("context_too_large")
		}
	} else {
		raw, err = boundedPaperInput(input)
	}
	if err != nil {
		return nil, s.failPaperStage(ctx, r, cp, stage, err, nil)
	}
	for {
		if cp.Calls >= paperCallLimit(r.Task) {
			return nil, s.failPaperStage(ctx, r, cp, stage, ErrBudget, nil)
		}
		if err := ctx.Err(); err != nil {
			return nil, s.failPaperStage(ctx, r, cp, stage, err, nil)
		}
		if err := check(ctx); err != nil {
			return nil, s.failPaperStage(ctx, r, cp, stage, err, nil)
		}
		started := time.Now()
		progress := stage
		if strings.HasPrefix(stage, "validating_paper") {
			progress = "validating_paper"
		}
		var validationErr error
		reserved := false
		result, callErr := s.Gateway.Generate(ctx, ModelRequest{Run: r, Feature: "paper_qa", System: paperPolicy + "\n" + prompt, Input: raw, Schema: paperStageSchema(stage), MaxTokens: paperStageTokens(stage),
			Before: func(call context.Context) error {
				if err := check(call); err != nil {
					return err
				}
				if err := call.Err(); err != nil {
					return err
				}
				if cp.Calls >= paperCallLimit(r.Task) {
					return ErrBudget
				}
				previousCalls, previousPhase := cp.Calls, cp.Phase
				previousFailure, previousStage := cp.Paper.Failure, cp.Paper.CurrentStage
				previousRepairState, previousAttempted := "", false
				if cp.Paper.Repair != nil {
					previousRepairState, previousAttempted = cp.Paper.Repair.State, cp.Paper.Repair.Attempted
				}
				if paperContract(stage).OriginalStage != "" {
					repair := cp.Paper.Repair
					if repair == nil || repair.Attempted || repair.State != "pending" {
						return paperError("invalid_checkpoint")
					}
					repair.Attempted, repair.State = true, "calling"
				}
				cp.Paper.Failure = nil
				cp.Paper.CurrentStage = stage
				cp.Calls++
				cp.Phase = "calling"
				if err := s.Store.Save(call, r, *cp, progress, nil); err != nil {
					// Returning from Before prevents the external call. Restore the
					// in-memory reservation before recording that definite failure.
					// A process death after Save still leaves calling on disk.
					cp.Calls, cp.Phase = previousCalls, previousPhase
					cp.Paper.Failure, cp.Paper.CurrentStage = previousFailure, previousStage
					if cp.Paper.Repair != nil {
						cp.Paper.Repair.State, cp.Paper.Repair.Attempted = previousRepairState, previousAttempted
					}
					return err
				}
				reserved = true
				return nil
			}, Validate: func(res generation.Result) error {
				validationErr = validate(res.Content)
				return validationErr
			}})
		if callErr != nil {
			var failure *ModelError
			if errors.As(callErr, &failure) && failure.Admission && !reserved {
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
		} else {
			// Enforce the contract even for a gateway that omits its validator.
			validationErr = validate(result.Content)
			callErr = validationErr
		}
		cp.Sequence++
		step := Step{RunID: r.ID, Sequence: cp.Sequence, Kind: "model", Tool: stage, CallID: result.CallID, DurationMS: time.Since(started).Milliseconds(), InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, CreatedAt: time.Now().UTC()}
		if !reserved {
			step.Kind = "workflow"
		}
		if callErr != nil {
			step.FailureCode = paperFailureCode(callErr)
			cp.Paper.Failure = paperFailureDetail(callErr)
			if detail := paperFailureDetail(validationErr); detail != nil && detail.Code == step.FailureCode {
				cp.Paper.Failure = detail
			}
			// Only a fully received, settled count/text limit failure is eligible.
			// Revalidate the returned candidate: some gateways return content on
			// transport or settlement errors as well as on validation failure.
			if reserved && paperContract(stage).RepairStage != "" && cp.Paper.Repair == nil && step.FailureCode == "output_limit_exceeded" && repairablePaperLimit(validationErr) && repairablePaperLimit(validate(result.Content)) {
				permissionErr := ctx.Err()
				if permissionErr == nil {
					permissionErr = check(ctx)
				}
				if permissionErr != nil {
					// Save the paid call's original failure and the actual terminal
					// error atomically; there must be no replayable ready gap.
					return nil, s.failPaperStage(ctx, r, cp, stage, permissionErr, &step)
				}
				request, budgetErr := buildPaperRepairRequest(r, cp.Paper, stage, result.Content, raw)
				if budgetErr != nil && paperFailureCode(budgetErr) != "context_too_large" {
					return nil, s.failPaperStage(ctx, r, cp, stage, budgetErr, &step)
				}
				cp.Paper.Repair = &PaperRepair{Stage: stage, Field: paperContract(stage).Field, Candidate: append(json.RawMessage(nil), result.Content...), Request: request, Failure: cp.Paper.Failure, State: "pending"}
				if budgetErr != nil {
					cp.Paper.Repair.State = "budget_exceeded"
					return nil, s.failPaperStage(ctx, r, cp, stage, callErr, &step)
				}
				cp.Phase = "ready"
				cp.Paper.CurrentStage = paperContract(stage).RepairStage
				if err := s.Store.Save(ctx, r, *cp, paperContract(stage).RepairStage, &step); err != nil {
					return nil, err
				}
				return s.runPaperRepair(ctx, r, cp, check)
			}
			return nil, s.failPaperStage(ctx, r, cp, stage, callErr, &step)
		}
		cp.Paper.Outputs[stage] = append(json.RawMessage(nil), result.Content...)
		if paperContract(stage).OriginalStage != "" {
			repair := cp.Paper.Repair
			cp.Paper.Outputs[repair.Stage] = append(json.RawMessage(nil), result.Content...)
			repair.State = "completed"
		}
		cp.Paper.Failure = nil
		cp.Phase = "ready"
		if err := s.Store.Save(ctx, r, *cp, progress, &step); err != nil {
			return nil, err
		}
		return result.Content, nil
	}
}

func (s *Service) preparePaper(ctx context.Context, r Run, c Conversation, cp *Checkpoint, check func(context.Context) error) (document.Document, []Citation, error) {
	// A preparation deadline can cause a fallback; loss of the parent run's
	// budget, ownership, access, or configuration must stop execution instead.
	checkRun := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.Deadline != nil && !r.Deadline.After(time.Now()) {
			return ErrBudget
		}
		return check(ctx)
	}
	if err := checkRun(); err != nil {
		return document.Document{}, nil, err
	}
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
	if pc.Outputs == nil || (pc.Mode != "" && pc.Mode != "abstract" && pc.Mode != "fulltext") {
		return document.Document{}, nil, paperError("invalid_checkpoint")
	}
	if pc.PaperHash != paperSnapshotHash(p) {
		return document.Document{}, nil, ErrConflict
	}
	if pc.Mode == "" && r.ContextMode == "abstract" {
		freezePaperAbstract(cp, "")
	}
	var doc document.Document
	if pc.Mode == "" {
		src := document.Source{PaperID: p.ID, ArXivID: p.ArXivID, PDFURL: p.PDFURL, UpdatedAt: p.ArXivUpdatedAt}
		id := document.Identity(src)
		if cp.DocumentID != "" && cp.DocumentID != id {
			return doc, nil, ErrConflict
		}
		cp.DocumentID = id
		deadline := time.Now().Add(105 * time.Second)
		if r.Task == TaskPaperFollowup {
			if pc.PreparationDeadline == nil {
				timeout := s.paperPreparationTimeout
				if timeout <= 0 {
					timeout = 20 * time.Second
				}
				deadline = time.Now().Add(timeout)
				pc.PreparationDeadline = &deadline
			}
			deadline = *pc.PreparationDeadline
		}
		if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
			deadline = parent
		}
		if r.Deadline != nil && r.Deadline.Before(deadline) {
			deadline = *r.Deadline
		}
		// Persist the absolute budget before any potentially blocking document
		// I/O. A recovery reuses it, even if the document has since become ready.
		if err = s.Store.Save(ctx, r, *cp, "preparing_document", nil); err != nil {
			return doc, nil, err
		}
		timedOut := !deadline.After(time.Now())
		if !timedOut {
			preparation, stop := context.WithDeadline(ctx, deadline)
			doc, timedOut, err = s.awaitPaperDocument(ctx, preparation, r.Task, src, check)
			stop()
			if err != nil {
				return doc, nil, err
			}
		}
		if err = checkRun(); err != nil {
			return doc, nil, err
		}
		if timedOut {
			freezePaperAbstract(cp, "document_timeout")
		} else if doc.ID != id {
			return doc, nil, ErrConflict
		} else if doc.State == "ready" && doc.TextComplete {
			pc.Mode = "fulltext"
			pc.SourceVersion = doc.SourceVersion
			pc.ContentHash = doc.ContentHash
			pc.Sections = doc.Sections
		} else {
			reason := doc.FailureCode
			if reason == "" {
				reason = "incomplete_text"
			}
			freezePaperAbstract(cp, reason)
		}
	}
	if err = checkRun(); err != nil {
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

func freezePaperAbstract(cp *Checkpoint, reason string) {
	cp.Paper.Mode = "abstract"
	cp.Paper.FallbackReason = reason
	cp.Paper.SourceVersion = ""
	cp.Paper.ContentHash = ""
	cp.Paper.Sections = nil
	cp.DocumentID = ""
}

// Document reads and polling share the preparation budget. Only that child
// deadline may produce timedOut; storage and authorization errors propagate.
func (s *Service) awaitPaperDocument(ctx, preparation context.Context, task string, src document.Source, check func(context.Context) error) (document.Document, bool, error) {
	var doc document.Document
	var err error
	if task == TaskPaperReport {
		doc, err = s.Documents.Prepare(preparation, src)
	} else {
		doc, err = s.Documents.Ensure(preparation, src)
	}
	for {
		if parentErr := ctx.Err(); parentErr != nil {
			return doc, false, parentErr
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && errors.Is(preparation.Err(), context.DeadlineExceeded) {
				return doc, true, nil
			}
			return doc, false, err
		}
		if preparation.Err() != nil {
			return doc, true, nil
		}
		if doc.State == "ready" || doc.State == "failed" {
			return doc, false, nil
		}
		if err = check(preparation); err != nil {
			// Re-enter the common classification before deciding on a fallback.
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-preparation.Done():
			timer.Stop()
			return doc, true, nil
		case <-timer.C:
		}
		doc, err = s.Documents.Get(preparation, document.Identity(src))
	}
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
	_, evidence, err := s.preparePaper(ctx, r, c, cp, check)
	if err != nil {
		return err
	}
	if r.Task == TaskPaperFollowup {
		return s.processPaperQuestion(ctx, r, c, cp, check, evidence)
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
				return s.failPaperStage(ctx, r, cp, "analyzing_"+field, err, nil)
			}
		}
	}
	if r.Task == TaskPaperReport {
		for _, field := range fields {
			raw, err := s.paperCall(ctx, r, cp, check, "analyzing_"+field, fieldPromptFor(field, true), paperInput(pc, field, available[field]), func(raw []byte) error { _, err := decodeFieldFor(raw, available[field], field, true); return err })
			if err != nil {
				return err
			}
			analyses[field], _ = decodeFieldFor(raw, available[field], field, true)
		}
	}
	claims := reviewClaims(fields, analyses)
	sources := map[string]Citation{}
	for _, field := range fields {
		for _, source := range available[field] {
			sources[source.ID] = source
		}
	}
	verdicts, err := s.reviewPaper(ctx, r, cp, check, claims, sources)
	if err != nil {
		return err
	}
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
