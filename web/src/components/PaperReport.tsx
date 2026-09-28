import { useState } from "react";
import {
  CitationDetails,
  CitationText,
  PaperCitations,
  type PaperCitation,
} from "./PaperCitations";
import type { PaperAnswer } from "./PaperAnswer";

export const reportFields = [
  ["problem", "论文问题"],
  ["method", "核心方法"],
  ["experiments", "实验验证"],
  ["results", "主要结果"],
  ["limitations", "局限性"],
] as const;
export type ReportField = (typeof reportFields)[number][0];
export type PaperResult = {
  report?: Record<ReportField, string>;
  answer?: PaperAnswer;
  fields: Record<string, { status: string; citation_ids: string[] }>;
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
export function PaperScope({
  result,
}: {
  result: Pick<PaperResult, "context_mode" | "fallback_reason">;
}) {
  return (
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
    </p>
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
  async function copy(kind: "JSON" | "Markdown") {
    try {
      await navigator.clipboard.writeText(
        kind === "JSON" ? JSON.stringify(result.report, null, 2) : content,
      );
      setCopied(kind);
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
          <button className="button" onClick={() => void copy("JSON")}>
            复制 JSON
          </button>
          <button className="button" onClick={() => void copy("Markdown")}>
            复制 Markdown
          </button>
          {copied && <span role="status">已复制 {copied}</span>}
        </div>
        {error && <p role="alert">{error}</p>}
        {reportFields.map(([field, label]) => (
          <section className="paper-report-field" key={field} aria-label={label}>
            <h3>{label}</h3>
            <p className="agent-text">
              <CitationText text={result.report?.[field] ?? ""} />
            </p>
            {result.fields[field]?.status === "insufficient_evidence" && (
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
