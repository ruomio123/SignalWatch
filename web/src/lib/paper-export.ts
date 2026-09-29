import { reportFields, structuredGapMessage, type PaperResult } from "../components/PaperReport";
import type { PaperCitation } from "../components/PaperCitations";
import { safeURL } from "../components/Common";
import { reproductionGapMessage } from "../components/PaperReproduction";
import { answerGapMessage } from "../components/PaperAnswer";
import { paperIssueMessage, partialPaperMessage, technicalGapMessage } from "./paper-status";

export type PaperExportContent = {
  result?: PaperResult;
  content: string;
  citations: PaperCitation[];
};

// All source text stays literal Markdown; only our known citation URLs become links.
function literal(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/([\\`*_{}\[\]()#+.!|~>-])/g, "\\$1");
}
function sourceLink(url: string, label: string): string {
  const safe = safeURL(url);
  if (!safe) return literal(label);
  const target = safe.replace(/[<>\s\\]/g, (character) => encodeURIComponent(character));
  return `[${literal(label)}](<${target}>)`;
}
function texBlock(tex: string): string {
  const longest = Math.max(2, ...(tex.match(/`+/g) ?? []).map((run) => run.length));
  const fence = "`".repeat(longest + 1);
  return `${fence}tex\n${tex}\n${fence}`;
}
function cell(value: string): string {
  return literal(value).replace(/\r?\n/g, "<br>");
}

export function paperMarkdown({ result, content, citations }: PaperExportContent): string {
  const references = new Map(citations.map((ref) => [ref.id, ref]));
  const used = new Set<string>();
  function marker(id: string): string {
    const ref = references.get(id);
    if (!ref) return literal(`[${id}]`);
    used.add(id);
    return sourceLink(ref.url, `[${id}]`);
  }
  function citedText(text: string): string {
    return text.split(/(\[[^\]\r\n]+\])/g).map((part) =>
      part.startsWith("[") && part.endsWith("]") && references.has(part.slice(1, -1))
        ? marker(part.slice(1, -1)) : literal(part),
    ).join("");
  }
  const kind = result?.report ? "论文报告" : result?.reproduction ? "复现清单" : "论文问答";
  const lines = [`# ${literal(result?.paper_title || "论文解读")} — ${kind}`];
  if (result) {
    if (result.outcome === "partial") {
      lines.push(partialPaperMessage);
      for (const issue of result.issues ?? []) lines.push(literal(paperIssueMessage(issue)));
    }
    lines.push(result.context_mode === "abstract"
      ? "仅基于摘要；缺失判断不代表论文全文没有相关内容。"
      : "基于论文提取材料；请结合下方原文证据核对结论。");
    if (result.source_version) lines.push(`论文版本：${literal(result.source_version)}`);
    if (result.structured_gap) lines.push(structuredGapMessage(result.structured_gap));
    if (result.original_question && !result.report) lines.push(`原问题：${literal(result.original_question)}`);
  }
  if (result?.report) {
    for (const [field, label] of reportFields) {
      lines.push(`## ${label}`, citedText(result.report[field]));
      const gap = technicalGapMessage(result.fields[field]?.gap_reason);
      if (gap) lines.push(gap);
    }
  } else if (result?.reproduction) {
    const reproduction = result.reproduction;
    if (reproduction.status !== "complete") lines.push(reproduction.categories.some((category) => technicalGapMessage(category.gap?.reason)) ? "部分清单已生成，部分处理未完成" : reproduction.status === "partial" ? "部分项目仍有证据缺口" : "当前证据不足");
    for (const category of reproduction.categories) {
      lines.push(`## ${literal(category.title)}`);
      for (const item of category.items) lines.push(`${item.number}. ${literal(item.text)} ${[...new Set(item.citation_ids)].map(marker).join(" ")}`.trim());
      if (category.gap || category.status !== "supported") lines.push(reproductionGapMessage(category.gap?.reason));
    }
  } else if (result?.answer) {
    const answer = result.answer;
    if (answer.status !== "complete") lines.push(answer.parts.some((part) => technicalGapMessage(part.gap?.reason)) ? "部分回答，部分处理未完成" : answer.status === "partial" ? "部分回答" : "证据不足");
    for (const part of answer.parts) {
      lines.push(`## ${literal(part.question)}`);
      for (const claim of part.claims) {
        lines.push(`${literal(claim.text)} ${[...new Set(claim.citation_ids)].map(marker).join(" ")}`.trim());
      }
      if (part.gap || part.status !== "supported") lines.push(answerGapMessage(part.gap?.reason));
    }
  } else {
    lines.push(citedText(content));
  }
  // The same immutable source can have distinct public IDs for separate parts.
  // Keep those aliases readable while emitting its full body only once.
  const sources = new Map<string, { ref: PaperCitation; ids: string[] }>();
  for (const id of used) {
    const ref = references.get(id)!;
    const identity = JSON.stringify([
      ref.source_type, ref.source_version, ref.source_hash, ref.content_hash,
      ref.url, ref.anchor, ref.quote, ref.kind,
      ref.table && [ref.table.headers, ref.table.rows, ref.table.caption, ref.table.notes],
      ref.formula && [ref.formula.tex, ref.formula.context],
    ]);
    const existing = sources.get(identity);
    if (existing) existing.ids.push(id);
    else sources.set(identity, { ref, ids: [id] });
  }
  if (sources.size) lines.push("## 原文证据");
  for (const { ref, ids } of sources.values()) {
    lines.push(`### 证据 ${ids.map(literal).join("、")}`);
    lines.push(`${ref.source_type === "html" ? "HTML 原文" : ref.page ? `第 ${ref.page} 页` : "摘要"}${ref.label ? ` · ${literal(ref.label)}` : ""}`);
    if (ref.source_version) lines.push(`来源版本：${literal(ref.source_version)}`);
    if (ref.anchor) lines.push(`原文位置：${literal(ref.anchor)}`);
    lines.push(sourceLink(ref.url, "查看 arXiv 原文"));
    if (ref.kind === "table" && ref.table) {
      const table = ref.table;
      if (table.caption) lines.push(literal(table.caption));
      lines.push([
        `| ${table.headers.map(cell).join(" | ")} |`,
        `| ${table.headers.map(() => "---").join(" | ")} |`,
        ...table.rows.map((row) => `| ${row.map(cell).join(" | ")} |`),
      ].join("\n"));
      if (table.notes.length) lines.push(table.notes.map((note) => `- ${literal(note)}`).join("\n"));
    }
    if (ref.kind === "formula" && ref.formula) {
      lines.push(texBlock(ref.formula.tex));
      if (ref.formula.context) lines.push(literal(ref.formula.context));
    }
    lines.push(ref.quote.split(/\r?\n/).map((line) => `> ${literal(line)}`).join("\n"));
  }
  return `${lines.filter(Boolean).join("\n\n")}\n`;
}
