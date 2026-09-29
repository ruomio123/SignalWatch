import { useState } from "react";
import {
  CitationDetails,
  CitationText,
  PaperCitations,
  type PaperCitation,
} from "./PaperCitations";
import type { PaperAnswer } from "./PaperAnswer";
import type { PaperReproduction } from "./PaperReproduction";
import { PaperMessageExport } from "./PaperMessageExport";
import { paperIssueMessage, partialPaperMessage, publicPaperIssue, technicalGapMessage, type PaperIssue, type PaperTechnicalGap } from "../lib/paper-status";

export const reportFields = [
  ["problem", "论文问题"],
  ["method", "核心方法"],
  ["experiments", "实验验证"],
  ["results", "主要结果"],
  ["limitations", "局限性"],
] as const;
export type ReportField = (typeof reportFields)[number][0];
export type PaperResult = {
  outcome?: "complete" | "partial";
  issues?: PaperIssue[];
  paper_title?: string;
  original_question?: string;
  structured_gap?: "unavailable" | "incomplete" | "preparation_budget" | "input_budget";
  report?: Record<ReportField, string>;
  answer?: PaperAnswer;
  reproduction?: PaperReproduction;
  fields: Record<string, { status: string; citation_ids: string[]; gap_reason?: PaperTechnicalGap }>;
  context_mode: "abstract" | "fulltext";
  fallback_reason?: string;
  coverage: string;
  workflow_version: string;
  source_version?: string;
};
const fallbackReasons: Record<string, string> = {
  ocr_required: "扫描或图片页面需要 OCR，本期尚未启用",
  incomplete_text: "全文文字提取不完整",
  document_download_failed: "PDF 下载失败",
  document_version_unavailable: "无法确认论文 PDF 版本",
  text_extraction_failed: "PDF 文本解析失败",
  document_extraction_failed: "全文解析失败",
  document_timeout: "全文准备超时",
  document_resource_limit: "PDF 超出解析资源限制",
  invalid_pdf: "下载内容不是有效 PDF",
  ocr_failed: "OCR 未能可靠识别全文",
};
export function structuredGapMessage(gap: PaperResult["structured_gap"]): string {
  if (!gap) return "";
  const reasons = {
    unavailable: "本轮未能取得结构化表格和公式，相关判断可能需要核对原文。",
    incomplete: "本轮仅提取到部分结构化表格和公式，未展示的内容仍需核对原文。",
    preparation_budget: "材料准备时间有限，本轮未完整提取结构化表格和公式。",
    input_budget: "受本轮材料容量限制，部分结构化表格和公式未纳入分析。",
  };
  return reasons[gap] ?? "本轮结构化材料不完整，相关判断可能需要核对原文。";
}
export function PaperScope({
  result,
}: {
  result: Pick<PaperResult, "context_mode" | "fallback_reason" | "structured_gap" | "outcome" | "issues">;
}) {
  return (
    <>
      <p className="paper-report-scope">
        {result.context_mode === "abstract"
          ? "仅基于摘要；缺失判断不代表论文全文没有相关内容。"
          : "基于论文提取文字；图片、公式及复杂表格可能无法可靠解析。"}
        {result.fallback_reason && (
          <span>
            {" "}
            自动降级原因：
            {fallbackReasons[result.fallback_reason] ?? "全文不可用"}。
          </span>
        )}
        {result.structured_gap && <span> {structuredGapMessage(result.structured_gap)}</span>}
      </p>
      {result.outcome === "partial" && <div className="paper-answer-gap">
        <p>{partialPaperMessage}</p>
        {!!result.issues?.length && <ul>{result.issues.map((issue, index) => <li key={index}>{paperIssueMessage(issue)}</li>)}</ul>}
      </div>}
    </>
  );
}
export function PaperReportView({
  messageID,
  result,
  content,
  citations,
  onRegenerate,
  disabled,
}: {
  messageID: string;
  result: PaperResult;
  content: string;
  citations: PaperCitation[];
  onRegenerate?: () => void;
  disabled?: boolean;
}) {
  const [copied, setCopied] = useState("");
  const [error, setError] = useState("");
  const assigned = new Set<string>();
  const fieldCitations = Object.fromEntries(
    reportFields.map(([field]) => {
      const ids = (result.fields[field]?.citation_ids ?? []).filter(
        (id) => !assigned.has(id),
      );
      ids.forEach((id) => assigned.add(id));
      return [field, ids];
    }),
  );
  async function copy() {
    try {
      await navigator.clipboard.writeText(
        JSON.stringify(result.outcome === "partial" ? {
          ...result.report, outcome: result.outcome,
          issues: (result.issues ?? []).map(publicPaperIssue),
        } : result.report, null, 2),
      );
      setCopied("JSON");
      setError("");
    } catch {
      setError("复制失败，请允许浏览器访问剪贴板后重试。");
    }
  }
  return (
    <PaperCitations messageID={messageID} citations={citations}>
      <div className="paper-report">
        <PaperScope result={result} />
        <div className="react-actions">
          {onRegenerate && (
            <button className="button" disabled={disabled} onClick={onRegenerate}>
              重新生成论文报告
            </button>
          )}
          <button className="button" onClick={() => void copy()}>
            复制 JSON
          </button>
          {copied && <span role="status">已复制 {copied}</span>}
        </div>
        {error && <p role="alert">{error}</p>}
        <PaperMessageExport messageID={messageID} result={result} content={content} citations={citations} />
        {reportFields.map(([field, label]) => (
          <section className="paper-report-field" key={field} aria-label={label}>
            <h3>{label}</h3>
            <p className="agent-text">
              <CitationText text={result.report?.[field] ?? ""} />
            </p>
            {technicalGapMessage(result.fields[field]?.gap_reason) ? (
              <small>{technicalGapMessage(result.fields[field]?.gap_reason)}</small>
            ) : result.fields[field]?.status === "insufficient_evidence" && (
              <small>证据不足，未保留未经支持的结论。</small>
            )}
            <CitationDetails ids={fieldCitations[field]} />
          </section>
        ))}
        <CitationDetails
          ids={citations.filter((ref) => !assigned.has(ref.id)).map((ref) => ref.id)}
        />
      </div>
    </PaperCitations>
  );
}
