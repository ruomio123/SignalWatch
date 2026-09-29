import { useCallback, useEffect, useRef, useState } from "react";
import { request } from "../lib/api";
import { useSession } from "../lib/session";
import { useRequestScope } from "../lib/useRequestScope";
import { ErrorNotice } from "./Common";

type DocumentStatus = {
  state: "not_prepared" | "pending" | "processing" | "ready" | "failed";
  usable: boolean;
  document_id: string;
  source_version: string;
  parser_version: string;
  page_count: number;
  updated_at?: string;
  failure_code?: string;
};

const failureMessages: Record<string, string> = {
  document_version_unavailable: "论文版本暂时无法获取，请稍后重试。",
  document_download_failed: "PDF 下载失败，请稍后重试。",
  invalid_pdf: "下载的文件不是有效 PDF，暂时无法解析。",
  document_extraction_failed: "全文解析失败，可以重试解析。",
  text_extraction_failed: "论文文字提取失败，可以重试解析。",
  document_resource_limit: "文档超出处理限制，暂时使用摘要。",
  ocr_required: "这份论文需要 OCR 才能读取文字，目前暂时使用摘要。",
  ocr_failed: "OCR 未能可靠识别文字，暂时使用摘要。",
  document_timeout: "全文准备超时，可以重试解析。",
};

type DocumentStatusProps = { paperID: number; refreshKey?: string };

export function PaperDocumentStatus({ paperID, refreshKey }: DocumentStatusProps) {
  const { token } = useSession();
  // Keep this component safe when used outside the keyed AgentChat as well.
  return (
    <DocumentStatusCard
      key={`${token}:${paperID}`}
      paperID={paperID}
      refreshKey={refreshKey}
    />
  );
}

function DocumentStatusCard({ paperID, refreshKey }: DocumentStatusProps) {
  const { token } = useSession();
  const scope = useRequestScope();
  const root = useRef<HTMLElement>(null);
  const revision = useRef(0);
  const reading = useRef<AbortController | undefined>(undefined);
  const preparing = useRef(false);
  const [status, setStatus] = useState<DocumentStatus>();
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const waiting = status?.state === "pending" || status?.state === "processing";
  const path = `/agent/papers/${paperID}/document`;

  const load = useCallback(async () => {
    if (preparing.current) return;
    const signal = scope();
    const controller = new AbortController();
    reading.current?.abort();
    reading.current = controller;
    const version = ++revision.current;
    const current = () =>
      !signal.aborted &&
      !controller.signal.aborted &&
      version === revision.current;
    try {
      const value = await request<DocumentStatus>(path, {
        token,
        signal: AbortSignal.any([signal, controller.signal]),
      });
      if (current()) {
        setStatus(value);
        setError(undefined);
      }
    } catch (error) {
      if (current()) setError(error);
    } finally {
      if (reading.current === controller) reading.current = undefined;
    }
  }, [path, token, scope]);

  useEffect(() => {
    void load();
    return () => reading.current?.abort();
  }, [load, refreshKey]);

  useEffect(() => {
    if (!waiting || busy) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const visible = () =>
      !document.hidden && !!root.current &&
      root.current.getClientRects().length > 0 && !root.current.closest("[hidden]");
    const tick = async () => {
      if (stopped) return;
      if (visible()) await load();
      if (!stopped) timer = setTimeout(tick, 2000);
    };
    const visibilityChanged = () => {
      if (document.hidden) reading.current?.abort();
    };
    timer = setTimeout(tick, 2000);
    document.addEventListener("visibilitychange", visibilityChanged);
    return () => {
      stopped = true;
      clearTimeout(timer);
      reading.current?.abort();
      document.removeEventListener("visibilitychange", visibilityChanged);
    };
  }, [waiting, busy, load]);

  async function prepare() {
    if (preparing.current || waiting || status?.state === "ready") return;
    const signal = scope();
    const version = ++revision.current;
    preparing.current = true;
    reading.current?.abort();
    setBusy(true);
    setError(undefined);
    try {
      const value = await request<DocumentStatus>(`${path}/prepare`, {
        token,
        signal,
        method: "POST",
      });
      if (!signal.aborted && version === revision.current) setStatus(value);
    } catch (error) {
      if (!signal.aborted && version === revision.current) setError(error);
    } finally {
      if (!signal.aborted) {
        preparing.current = false;
        setBusy(false);
      }
    }
  }

  const label = !status
    ? error ? "全文状态暂时不可用" : "正在读取全文状态…"
    : status.state === "ready"
      ? status.usable
        ? "全文已就绪"
        : "全文文字不完整"
      : {
          not_prepared: "全文尚未准备",
          pending: "全文等待解析",
          processing: "正在解析全文",
          failed: "全文准备失败",
        }[status.state];

  return (
    <section
      className="paper-document-status"
      aria-label="论文全文材料"
      ref={root}
    >
      <div className="paper-document-heading">
        <div>
          <h3>论文全文</h3>
          <p role="status" aria-live="polite">
            {label}
          </p>
        </div>
        {status?.state !== "ready" && (
          <button
            className="button"
            disabled={busy || waiting}
            onClick={() => void prepare()}
          >
            {busy || waiting
              ? "正在准备全文…"
              : status?.state === "failed"
                ? "重试解析"
                : "准备全文"}
          </button>
        )}
      </div>
      {status?.state === "ready" && (
        <p>
          {status.usable
            ? "下次提问可使用全文。"
            : "当前解析结果暂不可用于全文问答，将继续使用摘要。"}
        </p>
      )}
      {status?.state === "failed" && (
        <p>
          {failureMessages[status.failure_code ?? ""] ??
            failureMessages.document_extraction_failed}
        </p>
      )}
      {!!status?.page_count && <small>PDF {status.page_count} 页</small>}
      <small>仅准备论文材料，不调用 AI 模型。</small>
      {!!error && <ErrorNotice error={error} retry={() => void load()} />}
    </section>
  );
}
