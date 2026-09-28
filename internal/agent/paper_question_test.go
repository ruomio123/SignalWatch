package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"signalwatch/internal/document"
)

func TestPaperQuestionsPublishReviewedCoveragePerAspect(t *testing.T) {
	for _, scenario := range []string{"complete", "partial", "partial-aspect", "insufficient", "review-rejected"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			c, doc := workflowPaper(t, f, []string{"The retrieval method selects relevant text.", "Accuracy evaluation uses held out data."})
			answer := paperAnswerOutput{Answers: []paperAnswerPartOutput{
				{QuestionID: "q1", Status: "supported", Claims: []claimOutput{{Text: "论文的方法检索相关文本。", Evidence: []evidenceOutput{{ID: "p1-c1-s0"}}}}},
				{QuestionID: "q2", Status: "supported", Claims: []claimOutput{{Text: "论文在留出数据上评估准确率。", Evidence: []evidenceOutput{{ID: "p2-c2-s0"}}}}},
			}, SupplementalQueries: []PaperSupplementQuery{}}
			wantStatus, wantClaims := "complete", 2
			switch scenario {
			case "partial":
				answer.Answers[1].Status, answer.Answers[1].Claims = "insufficient_evidence", []claimOutput{}
				wantStatus, wantClaims = "partial", 1
			case "partial-aspect":
				answer.Answers[0].Status = "partial"
				wantStatus = "partial"
			case "insufficient":
				for i := range answer.Answers {
					answer.Answers[i].Status, answer.Answers[i].Claims = "insufficient_evidence", []claimOutput{}
				}
				wantStatus, wantClaims = "insufficient", 0
			case "review-rejected":
				wantStatus, wantClaims = "partial", 1
			}
			verdicts := []map[string]any{}
			for _, part := range answer.Answers {
				for i := range part.Claims {
					verdicts = append(verdicts, map[string]any{"id": fmt.Sprintf("%s-%d", part.QuestionID, i+1), "supported": scenario != "review-rejected" || part.QuestionID != "q2"})
				}
			}
			f.gateway.actions = []string{
				string(budgetJSON(t, paperQuestionsOutput{Questions: []paperQuestionItemOutput{{Question: "论文使用什么方法？", Query: "retrieval method"}, {Question: "怎样评估准确率？", Query: "accuracy evaluation"}}})),
				string(budgetJSON(t, answer)),
				string(budgetJSON(t, map[string]any{"verdicts": verdicts})),
			}
			requests := []ModelRequest{}
			f.gateway.hook = func(req ModelRequest) { requests = append(requests, req) }
			r := directSubmit(t, f, c, TaskPaperFollowup, "fulltext")
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, messages := paperOutcome(t, f, c, r)
			if end.State != "completed" || len(messages) != 2 || len(requests) != 3 {
				t.Fatalf("question failed: %+v messages=%d calls=%d", end, len(messages), len(requests))
			}
			result := directResult(t, f, r)
			if result.Report != nil || result.Answer == nil || result.Answer.Status != wantStatus || len(result.Answer.Parts) != 2 {
				t.Fatalf("wrong structured answer: %+v", result)
			}
			var citations []Citation
			must(t, json.Unmarshal(messages[1].Citations, &citations))
			if len(citations) != wantClaims {
				t.Fatalf("unreviewed or missing claims published: %+v", citations)
			}
			for _, citation := range citations {
				if citation.DocumentID != doc.ID || citation.ContentHash != "fixture-content-hash" || !strings.HasPrefix(citation.ID, "answer-q") || !strings.Contains(citation.URL, "1706.03762v1#page=") || citation.Quote == "" {
					t.Fatalf("citation lost its source version or passage: %+v", citation)
				}
			}
			if scenario == "review-rejected" && (result.Answer.Parts[1].Gap == nil || result.Answer.Parts[1].Gap.Reason != "review_rejected" || strings.Contains(messages[1].Content, answer.Answers[1].Claims[0].Text)) {
				t.Fatal("rejected fact was published or its gap was hidden")
			}
			for i, tokens := range []int{4096, 8192, 4096} {
				if requests[i].MaxTokens != tokens || len(requests[i].Input) > paperInputLimit {
					t.Fatalf("stage %d budget violated: %+v", i, requests[i])
				}
			}
			var input struct {
				Questions []PaperQuestion `json:"questions"`
				Evidence  []Citation      `json:"evidence"`
			}
			must(t, json.Unmarshal(requests[1].Input, &input))
			if len(input.Questions) != 2 || input.Questions[0].ID != "q1" || input.Questions[1].ID != "q2" || len(input.Evidence) != 2 {
				t.Fatalf("normalization or query coverage lost: %+v", input)
			}
			if public := string(budgetJSON(t, end)); strings.Contains(public, "normalization_request") || strings.Contains(public, "retriever_version") || strings.Contains(public, "retrieval method") {
				t.Fatalf("private retrieval data leaked: %s", public)
			}
		})
	}
}

type questionCheckpointStore struct {
	Store
	pause  func(Checkpoint, string) bool
	reads  int
	paused bool
}

func (s *questionCheckpointStore) PaperHistory(ctx context.Context, conversation, hash, exclude string) (PaperConversationContext, error) {
	s.reads++
	return s.Store.PaperHistory(ctx, conversation, hash, exclude)
}

func (s *questionCheckpointStore) Save(ctx context.Context, r Run, cp Checkpoint, progress string, step *Step) error {
	if err := s.Store.Save(ctx, r, cp, progress, step); err != nil {
		return err
	}
	if !s.paused && s.pause(cp, progress) {
		s.paused = true
		return errContextCheckpointPause
	}
	return nil
}

func TestPaperQuestionRecoveryReusesFrozenRequestsAndEvidence(t *testing.T) {
	for _, barrier := range []string{"normalization-request", "normalized", "retrieved", "analyzed", "review-plan"} {
		t.Run(barrier, func(t *testing.T) {
			f := newFixture(t)
			c, doc := workflowPaper(t, f, []string{"The retrieval method evaluates held out data."})
			hash := contextPaperHash(t, f, c)
			seedPaperContextRun(t, f, c, contextSeed{hash: hash, question: "原始历史问题", answer: "原始历史回答"})
			watch := &questionCheckpointStore{Store: f.store}
			watch.pause = func(cp Checkpoint, progress string) bool {
				if cp.Paper == nil || cp.Paper.QA == nil || cp.Phase != "ready" {
					return false
				}
				switch barrier {
				case "normalization-request":
					return cp.Calls == 0 && cp.Paper.QA.NormalizationRequest != ""
				case "normalized":
					return cp.Calls == 1 && cp.Paper.Outputs["normalizing_question"] != nil
				case "retrieved":
					return progress == "retrieving_evidence" && cp.Paper.QA.Initial != nil
				case "analyzed":
					return cp.Paper.Outputs["analyzing_answer"] != nil
				default:
					return cp.Paper.ReviewPlan != nil
				}
			}
			f.s.Store = watch
			g := &workflowGateway{}
			f.s.Gateway = g
			r := f.claim(t, directSubmit(t, f, c, TaskPaperFollowup, "fulltext").ID)
			g.hook = func(req ModelRequest) error {
				stored := directCheckpoint(t, f, r)
				if req.Schema == paperQuestionSchema && string(req.Input) != stored.Paper.QA.NormalizationRequest {
					t.Fatal("normalization request was not frozen before call")
				}
				if req.Schema == paperAnswerSchemas.full && (stored.Paper.QA.Initial == nil || string(req.Input) != stored.Paper.QA.Initial.Request) {
					t.Fatal("analysis request was not frozen before call")
				}
				return nil
			}
			cp := directCheckpoint(t, f, r)
			err := f.s.processPaper(t.Context(), r, c, &cp, func(ctx context.Context) error { return f.store.Check(ctx, r) })
			if !errors.Is(err, errContextCheckpointPause) || !watch.paused {
				t.Fatalf("did not stop at %s: %v", barrier, err)
			}
			frozen := directCheckpoint(t, f, r)
			seedPaperContextRun(t, f, c, contextSeed{hash: hash, question: "恢复后新增的问题", answer: "不应进入快照的回答"})
			if frozen.Paper.QA.Initial != nil {
				// Simulate changed reader output under the same pinned identity: the
				// persisted request and source text remain authoritative on resume.
				must(t, f.db.Model(&document.Chunk{}).Where("document_id=?", doc.ID).Update("text", "Changed reader output contains no original evidence.").Error)
			}
			must(t, f.db.Model(&Run{}).Where("id=?", r.ID).Update("lease_until", time.Now().Add(-time.Minute)).Error)
			f.s.process(t.Context(), f.claim(t, r.ID))
			end, _ := paperOutcome(t, f, c, r)
			if end.State != "completed" || len(g.calls) != 3 || watch.reads != 1 {
				t.Fatalf("recovery replayed calls/read history: state=%s failure=%s calls=%d history=%d", end.State, end.FailureCode, len(g.calls), watch.reads)
			}
			completed := directCheckpoint(t, f, r)
			if completed.Paper.QA.NormalizationRequest != frozen.Paper.QA.NormalizationRequest || !reflect.DeepEqual(completed.Paper.ConversationContext, frozen.Paper.ConversationContext) {
				t.Fatal("recovery changed normalization or conversation snapshot")
			}
			if frozen.Paper.QA.Initial != nil && !reflect.DeepEqual(completed.Paper.QA.Initial, frozen.Paper.QA.Initial) {
				t.Fatal("recovery reselected or changed frozen evidence")
			}
			if completed.Calls != 3 || paperCallLimit(TaskPaperFollowup) != 6 {
				t.Fatal("normal call counting or v11 hard limit changed")
			}
		})
	}
}

func TestPaperAnswerPackingTrimsHistoryBeforeWholeEvidence(t *testing.T) {
	questions := []PaperQuestion{{ID: "q1", Question: "论文使用什么方法？", Query: "method"}}
	for _, count := range []int{8, 24} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			pc := &PaperCheckpoint{Mode: "fulltext", Context: PaperContext{Title: "paper"}, ConversationContext: PaperConversationContext{Turns: []PaperConversationTurn{
				{Question: strings.Repeat("Q", 2000), Answer: strings.Repeat("A", 4000)},
				{Question: strings.Repeat("R", 2000), Answer: strings.Repeat("B", 4000)},
				{Question: strings.Repeat("S", 2000), Answer: strings.Repeat("C", 4000)},
			}}}
			snapshot := string(budgetJSON(t, pc.ConversationContext))
			evidence := make([]Citation, count)
			for i := range evidence {
				evidence[i] = Citation{ID: fmt.Sprintf("p1-c1-s%d", i*1000), Quote: strings.Repeat("<", 990) + "中文🙂", DocumentID: "frozen", ContentHash: "hash", Page: 1}
			}
			r := Run{Question: "原始问题保持完整🙂"}
			packed, err := packPaperAnswerInput(r, pc, questions, evidence)
			must(t, err)
			if len(packed.Request) > paperInputLimit || len(packed.Evidence) == 0 || !strings.Contains(packed.Request, r.Question) {
				t.Fatalf("wrong packed budget: bytes=%d sources=%d", len(packed.Request), len(packed.Evidence))
			}
			var decoded struct {
				Evidence []evidencePassage        `json:"evidence"`
				Context  PaperConversationContext `json:"conversation_context"`
			}
			must(t, json.Unmarshal([]byte(packed.Request), &decoded))
			if len(decoded.Context.Turns) >= 3 || len(decoded.Evidence) != len(packed.Evidence) {
				t.Fatal("history was not reduced before evidence")
			}
			if count == 8 && len(packed.Evidence) != count {
				t.Fatal("discarded evidence despite removable history")
			}
			if count == 24 && len(packed.Evidence) >= count {
				t.Fatal("did not account for JSON-escaped byte size")
			}
			for i, source := range packed.Evidence {
				if source != evidence[i] || decoded.Evidence[i].Quote != source.Quote || decoded.Evidence[i].ID != source.ID {
					t.Fatal("selected evidence was clipped, reordered or changed")
				}
			}
			if string(budgetJSON(t, pc.ConversationContext)) != snapshot {
				t.Fatal("packing mutated the captured history")
			}
		})
	}
	if _, err := packPaperAnswerInput(Run{Question: strings.Repeat("<", paperInputLimit)}, &PaperCheckpoint{}, questions, nil); paperFailureCode(err) != "context_too_large" {
		t.Fatalf("oversized mandatory input was silently clipped: %v", err)
	}
	short := make([]Citation, 33)
	for i := range short {
		short[i] = Citation{ID: fmt.Sprintf("abstract-s%d", i*1000), Quote: "完整摘要证据🙂"}
	}
	packed, err := packPaperAnswerInput(Run{Question: "问题"}, &PaperCheckpoint{Mode: "abstract"}, questions, short)
	must(t, err)
	if !reflect.DeepEqual(packed.Evidence, short[:24]) {
		t.Fatal("abstract initial retrieval did not retain at most 24 whole passages")
	}
}
