import { aiFailureMessage } from "./api";

export type PaperIssue = {
  stage: string;
  code: string;
  field?: string;
  question_ids?: string[];
};

export type PaperTechnicalGap = "processing_failed" | "review_incomplete";

export const partialPaperMessage = "本轮部分完成，仅保留已通过证据审核的结论。";

const fields: Record<string, string> = {
  problem: "论文问题", method: "核心方法", experiments: "实验验证",
  results: "主要结果", limitations: "局限性", answer: "追问回答",
  answer_supplement: "补充回答", reproduction: "复现清单",
  reproduction_supplement: "补充复现清单",
};

export function paperStageLabel(stage: string): string {
  if (stage.startsWith("repairing_")) return `恢复${paperStageLabel(stage.slice(10))}`;
  if (stage.startsWith("analyzing_")) return fields[stage.slice(10)] ?? "论文分析";
  if (fields[stage]) return fields[stage];
  if (/^validating_paper(?:_\d+)?$/.test(stage)) return "证据审核";
  if (/^extracting_batch_\d+$/.test(stage)) return "分批证据提取";
  if (stage === "normalizing_question") return "问题理解";
  if (stage === "planning_reproduction") return "复现清单规划";
  return "论文处理";
}

export function paperIssueMessage(issue: PaperIssue): string {
  const label = (issue.field && fields[issue.field]) || paperStageLabel(issue.stage);
  const reason = issue.code === "output_language_mismatch" ? "这部分输出未按要求使用中文。"
    : issue.code === "context_too_large" ? "当前材料超出模型可用上下文或本轮输入上限。"
    : aiFailureMessage(issue.code);
  return `${label}：${reason}`;
}

export function technicalGapMessage(reason?: string): string | undefined {
  if (reason === "processing_failed") return "这部分处理未完成，仅展示已通过审核的内容。";
  if (reason === "review_incomplete") return "这部分证据审核未完成，尚未审核的结论未予展示。";
  return undefined;
}

// Keep extra diagnostic payloads out of user exports.
export function publicPaperIssue(issue: PaperIssue): PaperIssue {
  return {
    stage: issue.stage, code: issue.code,
    ...(issue.field ? { field: issue.field } : {}),
    ...(issue.question_ids ? { question_ids: issue.question_ids } : {}),
  };
}
