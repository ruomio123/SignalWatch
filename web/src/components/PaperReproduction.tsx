import {
  CitationDetails,
  CitationMarker,
  PaperCitations,
  type PaperCitation,
} from "./PaperCitations";

export type PaperReproductionItem = {
  number: number;
  text: string;
  citation_ids: string[];
};
export type PaperReproduction = {
  status: "complete" | "partial" | "insufficient";
  categories: {
    id: "data" | "model" | "training" | "evaluation" | "compute" | "resources";
    title: string;
    status: "supported" | "partial" | "insufficient_evidence";
    items: PaperReproductionItem[];
    gap?: { reason: "insufficient_evidence" | "review_rejected" };
  }[];
};

export function reproductionGapMessage(reason?: string): string {
  return reason === "review_rejected"
    ? "部分项目未通过证据审核，当前材料不足以完整列出该类复现信息。"
    : "当前材料不足以完整列出该类复现信息。";
}

export function PaperReproductionView({
  reproduction,
  messageID,
  citations,
  onFollowUp,
  disabled,
}: {
  reproduction: PaperReproduction;
  messageID: string;
  citations: PaperCitation[];
  onFollowUp: (item: PaperReproductionItem) => void;
  disabled?: boolean;
}) {
  const visibleIDs = new Set(reproduction.categories.flatMap((category) =>
    category.items.flatMap((item) => item.citation_ids),
  ));
  return <PaperCitations messageID={messageID} citations={citations.filter((ref) => visibleIDs.has(ref.id))}>
    <div className="paper-reproduction">
      <h3>复现清单</h3>
      {reproduction.status !== "complete" && <p className="paper-answer-status">
        {reproduction.status === "partial" ? "部分项目仍有证据缺口" : "当前证据不足"}
      </p>}
      <p className="paper-reproduction-hint">可按条目编号继续追问；清单仅包含当前材料支持的信息。</p>
      {reproduction.categories.map((category) => <section className="paper-reproduction-category" key={category.id} aria-label={category.title}>
        <h4>{category.title}</h4>
        {category.items.length > 0 && <ol start={category.items[0].number}>
          {category.items.map((item) => <li value={item.number} key={item.number}>
            <p className="agent-text">{item.text}{" "}{[...new Set(item.citation_ids)].map((id) => <CitationMarker key={id} id={id} />)}</p>
            <button type="button" className="button paper-reproduction-followup" disabled={disabled} onClick={() => onFollowUp(item)}>追问第 {item.number} 项</button>
          </li>)}
        </ol>}
        {(category.gap || category.status !== "supported") && <p className="paper-answer-gap">{reproductionGapMessage(category.gap?.reason)}</p>}
      </section>)}
    </div>
    <CitationDetails />
  </PaperCitations>;
}
