import {
  CitationDetails,
  CitationMarker,
  PaperCitations,
  type PaperCitation,
} from "./PaperCitations";
import { technicalGapMessage, type PaperTechnicalGap } from "../lib/paper-status";

export type PaperAnswer = {
  status: "complete" | "partial" | "insufficient";
  parts: {
    question_id: string;
    question: string;
    status: "supported" | "partial" | "insufficient_evidence" | "processing_failed";
    claims: { text: string; citation_ids: string[] }[];
    gap?: { reason: "insufficient_evidence" | "review_rejected" | PaperTechnicalGap };
  }[];
};

const gapMessages = {
  insufficient_evidence: "当前材料不足以可靠回答这部分问题。",
  review_rejected: "这部分结论未通过证据审核，未予展示。",
};

export function answerGapMessage(reason?: string): string {
  return technicalGapMessage(reason) ?? (reason === "review_rejected" ? gapMessages.review_rejected : gapMessages.insufficient_evidence);
}

export function PaperAnswerView({
  answer,
  messageID,
  citations,
}: {
  answer: PaperAnswer;
  messageID: string;
  citations: PaperCitation[];
}) {
  const visibleIDs = new Set(
    answer.parts.flatMap((part) =>
      part.claims.flatMap((claim) => claim.citation_ids),
    ),
  );
  return (
    <PaperCitations
      messageID={messageID}
      citations={citations.filter((ref) => visibleIDs.has(ref.id))}
    >
      <div className="paper-answer">
        {answer.status !== "complete" && (
          <p className="paper-answer-status">
            {answer.parts.some((part) => technicalGapMessage(part.gap?.reason)) ? "部分回答，部分处理未完成" : answer.status === "partial" ? "部分回答" : "证据不足"}
          </p>
        )}
        {answer.parts.map((part) => (
          <section className="paper-answer-part" key={part.question_id}>
            {answer.parts.length > 1 && <h4>{part.question}</h4>}
            {part.claims.map((claim, index) => (
              <p className="agent-text" key={index}>
                {claim.text}{" "}
                {[...new Set(claim.citation_ids)].map((id) => (
                  <CitationMarker key={id} id={id} />
                ))}
              </p>
            ))}
            {(part.gap || part.status !== "supported") && (
              <p className="paper-answer-gap">
                {answerGapMessage(part.gap?.reason)}
              </p>
            )}
          </section>
        ))}
      </div>
      <CitationDetails />
    </PaperCitations>
  );
}
