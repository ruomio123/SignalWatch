import {
  Fragment,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import { request, outputFailureMessage } from "../lib/api";
import { useSession } from "../lib/session";
import { useRequestScope } from "../lib/useRequestScope";
import { ErrorNotice, Loading, safeURL } from "./Common";
import type { Configuration, Provider } from "../lib/types";

import { AssistantView, AssistantComposer } from "./AssistantView";
import { PaperReportView, PaperScope, type PaperResult } from "./PaperReport";
import { PaperDocumentStatus } from "./PaperDocumentStatus";
import { ShieldCheck } from "lucide-react";
import { SubscriptionDraftCard, type Draft } from "./SubscriptionDraft";

type Conversation = {
  paper_report_ready?: boolean;
  id: string;
  kind: "paper" | "subscription";
  paper_id?: number;
  title: string;
  active_run_id?: string;
  latest_draft_id?: string;
};
type Citation = {
  id: string;
  quote: string;
  page: number;
  url: string;
  document_id: string;
  content_hash: string;
};
type Message = {
  result?: PaperResult;
  id: number;
  run_id: string;
  role: string;
  content: string;
  citations: Citation[];
  draft_id?: string;
  provider: string;
  model: string;
};
type ReportSnapshot = {
  report: Message | null;
  matches_current_paper: boolean;
};
function mergeMessages(previous: Message[], incoming: Message[]) {
  return [
    ...new Map([...previous, ...incoming].map((m) => [m.id, m])).values(),
  ].sort((a, b) => a.id - b.id);
}
type AgentChatProps = {
  kind: "paper" | "subscription";
  paperID?: number;
  paperTitle?: string;
  onCreated?: () => void;
  onClose?: () => void;
};
type FailedStep = { tool?: string; call_id?: string; failure_code?: string };
type Run = {
  failure_detail?: { code: string; path: string; rule: string };
  failedStep?: FailedStep;
  task?: "paper_report" | "paper_followup";
  effective_context_mode?: "abstract" | "fulltext";
  fallback_reason?: string;
  batch_total?: number;
  batch_completed?: number;
  id: string;
  state: string;
  progress: string;
  failure_code?: string;
  provider: string;
  model: string;
};
const progress: Record<string, string> = {
  queued: "等待处理",
  planning_paper: "正在准备固定分析任务",
  normalizing_question: "正在理解追问",
  analyzing_problem: "正在分析论文问题",
  analyzing_method: "正在分析核心方法",
  analyzing_experiments: "正在分析实验验证",
  analyzing_results: "正在分析主要结果",
  analyzing_limitations: "正在分析局限性",
  analyzing_answer: "正在分析追问",
  validating_paper: "正在校验结论与论文证据",
  retrieving_evidence: "正在检索论文证据",
  previewing_subscription: "正在预览本地匹配论文",
  preparing_draft: "正在整理订阅草案",
  querying_options: "正在查询订阅分类",
  preparing_document: "正在准备论文全文",
  generating: "正在生成回复",
  checking_response: "正在检查回答",
  tool_completed: "已完成工具查询",
  waiting_for_model_slot: "正在等待模型调用空闲",
  completed: "已完成",
  running: "正在处理",
};
const validationRules: Record<string, string> = {
  chinese_text_required: "报告论断需要使用中文叙述，专业名称与公式可保留原文",
  claims_required: "有依据的结论必须包含论断",
  claims_must_be_empty: "缺失或证据不足时不能附带事实论断",
  nonempty_text: "论断文字不能为空",
  duplicate_evidence: "同一论断不能重复引用相同片段",
  evidence_required: "每条论断必须有原文证据",
  required_field: "缺少必填字段",
  unexpected_field: "包含未定义的字段",
  invalid_enum: "字段值不在允许范围内",
  expected_object: "必须是对象",
  expected_array: "必须是数组",
  expected_string: "必须是字符串",
  expected_boolean: "必须是布尔值",
  duplicate_key: "不能重复定义 JSON 字段",
  too_deep: "JSON 嵌套过深",
  invalid_json: "必须是有效 JSON",
};
const failures: Record<string, string> = {
  document_unavailable: "全文不可用。",
  context_too_large: "论文或汇总证据超出本次输入上限，未输出部分报告。",
  invalid_output: "本次输出或证据校验未通过，未发布报告。请手动重试。",
  workflow_changed: "论文助手已升级，旧任务已停止。请重新提问，或手动生成论文报告。",
  result_unknown: "模型调用结果未知，未自动重试。你可以手动重新发送。",
  timeout: "模型响应超时，未自动重试。",
  invalid_citation: "本次回答的证据校验未通过，请手动重试。",
  budget_exhausted:
    "本轮已达到执行上限，未发布部分结果。可以调整资料模式或缩小问题范围后重试。",
  configuration_changed: "模型配置已变化，请重新选择模型。",
  access_or_configuration_changed: "论文访问权限或模型配置已变化。",
  daily_limit: "今日模型调用已达上限。",
  cancelled: "本轮已停止。",
};
export function AgentChat(props: AgentChatProps) {
  const { token } = useSession();
  return (
    <AgentChatView
      key={JSON.stringify([token, props.kind, props.paperID])}
      {...props}
    />
  );
}
function AgentChatView({
  kind,
  paperID,
  paperTitle,
  onCreated,
  onClose,
}: AgentChatProps) {
  const { token } = useSession();
  const location = useLocation();
  const [params, setParams] = useSearchParams();
  const conversationID = params.get("conversation") ?? "";
  const scope = useRequestScope(conversationID);
  const [credentials, setCredentials] = useState<Configuration[]>();
  const [providers, setProviders] = useState<Provider[]>([]);
  const [provider, setProvider] = useState("");
  const [credentialID, setCredentialID] = useState("");
  const [model, setModel] = useState("");
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [nextConversationPage, setNextConversationPage] = useState(0);
  const [loadingConversations, setLoadingConversations] = useState(false);
  const conversationPage = useRef(1);
  const conversationLoading = useRef(false);
  const [messages, setMessages] = useState<Message[]>([]);
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const [current, setCurrent] = useState<Conversation>();
  const [run, setRun] = useState<Run>();
  const [question, setQuestion] = useState("");
  const [mode, setMode] = useState("fulltext");
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const [older, setOlder] = useState(0);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const olderLoading = useRef(false);
  const historyLoaded = useRef(false);
  const [reportSnapshot, setReportSnapshot] = useState<ReportSnapshot>();
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const scrollAnchor = useRef<{ id: string; offset: number } | undefined>(
    undefined,
  );
  const reloadVersion = useRef(0);
  const newConversation = useRef<string | undefined>(undefined);
  const submitting = useRef(false);
  const submission = useRef<{ key: string; body: string } | undefined>(
    undefined,
  );
  const chooseConversation = (id: string) =>
    setParams(
      (p) => {
        const next = new URLSearchParams(p);
        if (id) next.set("conversation", id);
        else next.delete("conversation");
        return next;
      },
      { replace: true },
    );
  const list = useCallback(
    async (signal: AbortSignal, page = 1) => {
      const result = await request<{
        items: Conversation[];
        has_more: boolean;
        next_page: number;
      }>(
        `/agent/conversations?kind=${kind}${paperID ? `&paper_id=${paperID}` : ""}&page=${page}`,
        { token, signal },
      );
      if (signal.aborted) return;
      setConversations((old) => {
        const ordered =
          page === 1 ? [...result.items, ...old] : [...old, ...result.items];
        const values = new Map<string, Conversation>();
        for (const c of ordered) if (!values.has(c.id)) values.set(c.id, c);
        for (const c of result.items) values.set(c.id, c);
        return [...values.values()];
      });
      if (page >= conversationPage.current) {
        conversationPage.current = page;
        setNextConversationPage(result.has_more ? result.next_page : 0);
      }
    },
    [token, kind, paperID],
  );
  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      request<{ items: Configuration[] }>("/ai/credentials", {
        token,
        signal: controller.signal,
      }),
      request<{ items: Provider[] }>("/ai/providers", {
        token,
        signal: controller.signal,
      }),
    ])
      .then(([keys, catalog]) => {
        if (controller.signal.aborted) return;
        setCredentials(keys.items);
        setProviders(catalog.items);
        const selected =
          keys.items.find((c) => c.usable && c.is_default) ??
          keys.items.find((c) => c.usable);
        if (selected) {
          setProvider(selected.provider!);
          setCredentialID(selected.id ?? selected.generation ?? "");
          setModel(selected.model!);
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(e);
      });
    void list(controller.signal).catch((e) => {
      if (!controller.signal.aborted) setError(e);
    });
    return () => controller.abort();
  }, [token, list]);
  const reload = useCallback(
    async (signal: AbortSignal, targetID = conversationID) => {
      if (!targetID) return;
      const revision = ++reloadVersion.current;
      const loadSnapshot = () =>
        Promise.all([
          request<Conversation>(`/agent/conversations/${targetID}`, {
            token,
            signal,
          }),
          request<{ items: Message[]; next_before: number }>(
            `/agent/conversations/${targetID}/messages`,
            { token, signal },
          ),
          kind === "paper"
            ? request<ReportSnapshot>(
                `/agent/conversations/${targetID}/paper-report`,
                { token, signal },
              )
            : Promise.resolve(undefined),
        ]);
      let [c, m, report] = await loadSnapshot();
      if (c.kind !== kind || (kind === "paper" && c.paper_id !== paperID)) {
        throw new Error("该对话不属于当前论文，请在助手设置中选择新对话。");
      }
      const latestRun = c.active_run_id ?? m.items[m.items.length - 1]?.run_id;
      let latest: Run | undefined;
      if (latestRun) {
        const result = await request<{ run: Run; steps?: FailedStep[] }>(
          `/agent/runs/${latestRun}`,
          {
            token,
            signal,
          },
        );
        latest = {
          ...result.run,
          failedStep: result.steps?.filter((step) => step.failure_code).at(-1),
        };
        // Completion may commit between the message read and the run read.
        // Read again after that commit before stopping the poller.
        if (
          latest.state === "completed" &&
          !m.items.some((v) => v.run_id === latestRun && v.role === "assistant")
        ) {
          [c, m, report] = await loadSnapshot();
        }
      }
      const ids = [
        ...new Set(
          m.items.map((v) => v.draft_id).filter((v): v is string => !!v),
        ),
      ];
      const ds = await Promise.all(
        ids.map((id) =>
          request<Draft>(`/agent/subscription-drafts/${id}`, { token, signal }),
        ),
      );
      // Publish a complete snapshot. A terminal run stops polling, so do not
      // publish it before its messages and drafts have finished loading.
      if (signal.aborted || revision !== reloadVersion.current) return;
      setCurrent(c);
      setMessages((old) => mergeMessages(old, m.items));
      if (!historyLoaded.current) setOlder(m.next_before);
      setDrafts((old) => ({
        ...old,
        ...Object.fromEntries(ds.map((d) => [d.id, d])),
      }));
      setReportSnapshot(report);
      setRun(latest);
    },
    [conversationID, token, kind, paperID],
  );
  useLayoutEffect(() => {
    const controller = new AbortController();
    reloadVersion.current++;
    olderLoading.current = false;
    conversationLoading.current = false;
    historyLoaded.current = false;
    scrollAnchor.current = undefined;
    newConversation.current = undefined;
    submitting.current = false;
    submission.current = undefined;
    setOlder(0);
    setLoadingOlder(false);
    setLoadingConversations(false);
    setReportSnapshot(undefined);
    setBusy(false);
    setQuestion("");
    setMessages([]);
    setRun(undefined);
    setCurrent(undefined);
    setError(undefined);
    setDrafts({});
    void reload(controller.signal).catch((e) => {
      if (!controller.signal.aborted) setError(e);
    });
    return () => controller.abort();
  }, [reload]);
  useLayoutEffect(() => {
    const anchor = scrollAnchor.current;
    const root = scrollRef.current;
    if (!anchor || !root) return;
    const element = root.querySelector<HTMLElement>(
      `[data-message-id="${anchor.id}"]`,
    );
    if (element)
      root.scrollTop +=
        element.getBoundingClientRect().top -
        root.getBoundingClientRect().top -
        anchor.offset;
    scrollAnchor.current = undefined;
  }, [messages]);
  const active = !!run && ["pending", "running"].includes(run.state);
  const activeRunID = active ? run.id : undefined;
  useEffect(() => {
    if (!activeRunID) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const result = await request<{ run: Run }>(
          `/agent/runs/${activeRunID}`,
          {
            token,
            signal: controller.signal,
          },
        );
        if (controller.signal.aborted) return;
        if (!["pending", "running"].includes(result.run.state)) {
          await list(controller.signal);
          await reload(controller.signal);
          if (!controller.signal.aborted) setError(undefined);
        } else {
          setRun(result.run);
        }
      } catch (e) {
        if (!controller.signal.aborted) setError(e);
      } finally {
        // Keep polling after a failed read; never resubmit the model call.
        if (!controller.signal.aborted) timer = setTimeout(poll, 2000);
      }
    };
    timer = setTimeout(poll, 2000);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [activeRunID, reload, list, token]);
  async function send(e?: FormEvent, task?: "paper_report" | "paper_followup") {
    e?.preventDefault();
    if (
      (task !== "paper_report" && !question.trim()) ||
      submitting.current ||
      busy ||
      active ||
      !usable.length ||
      !provider ||
      !model ||
      (kind === "paper" && !!conversationID && current?.id !== conversationID)
    )
      return;
    submitting.current = true;
    const signal = scope();
    setBusy(true);
    setError(undefined);
    const text = task === "paper_report" ? "" : question;
    const body = JSON.stringify({
      ...(kind === "paper" ? { task: task ?? "paper_followup" } : {}),
      question: text,
      provider,
      credential_id: credentialID,
      model,
      context_mode: mode,
    });
    if (submission.current?.body !== body)
      submission.current = { key: crypto.randomUUID(), body };
    try {
      let id = conversationID || newConversation.current;
      if (!id) {
        const c = await request<Conversation>("/agent/conversations", {
          token,
          signal,
          method: "POST",
          body: { kind, ...(paperID ? { paper_id: paperID } : {}) },
        });
        id = c.id;
        newConversation.current = id;
      }
      const value = await request<{ run_id: string; run: Run }>(
        `/agent/conversations/${id}/messages`,
        {
          token,
          signal,
          method: "POST",
          body: {
            ...JSON.parse(body),
            idempotency_key: submission.current!.key,
          },
        },
      );
      if (signal.aborted) return;
      submission.current = undefined;
      if (task !== "paper_report") setQuestion("");
      // Only navigate after both writes finish. The new view owns its reads;
      // switching the URL earlier would cancel this legitimate submission.
      if (!conversationID) {
        chooseConversation(id);
        return;
      }
      setRun(value.run);
      await reload(signal, id);
      await list(signal);
    } catch (e) {
      if (!signal.aborted) setError(e);
    } finally {
      if (!signal.aborted) {
        submitting.current = false;
        setBusy(false);
      }
    }
  }
  async function confirm(d: Draft) {
    if (
      busy ||
      active ||
      d.subscription_id ||
      current?.latest_draft_id !== d.id ||
      new Date(d.expires_at).getTime() <= Date.now()
    )
      return;
    setBusy(true);
    setError(undefined);
    const signal = scope();
    try {
      await request(`/agent/subscription-drafts/${d.id}/confirm`, {
        token,
        signal,
        method: "POST",
        body: { version: d.version },
      });
      await reload(signal);
      if (!signal.aborted) onCreated?.();
    } catch (e) {
      if (!signal.aborted) setError(e);
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  }
  function refresh() {
    const signal = scope();
    setError(undefined);
    void reload(signal).catch((error) => {
      if (!signal.aborted) setError(error);
    });
  }
  async function loadOlder() {
    if (olderLoading.current || !older || current?.id !== conversationID)
      return;
    const signal = scope();
    olderLoading.current = true;
    setLoadingOlder(true);
    try {
      const page = await request<{ items: Message[]; next_before: number }>(
        `/agent/conversations/${conversationID}/messages?before=${older}`,
        { token, signal },
      );
      const ids = [
        ...new Set(
          page.items.map((m) => m.draft_id).filter((id): id is string => !!id),
        ),
      ];
      const loadedDrafts = await Promise.all(
        ids.map((id) =>
          request<Draft>(`/agent/subscription-drafts/${id}`, { token, signal }),
        ),
      );
      if (signal.aborted) return;
      const root = scrollRef.current;
      if (root) {
        const bounds = root.getBoundingClientRect();
        const top = bounds.top;
        const firstVisible = [
          ...root.querySelectorAll<HTMLElement>("[data-message-id]"),
        ].find((element) => {
          const rect = element.getBoundingClientRect();
          return rect.bottom > top && rect.top < bounds.bottom;
        });
        if (firstVisible)
          scrollAnchor.current = {
            id: firstVisible.dataset.messageId!,
            offset: firstVisible.getBoundingClientRect().top - top,
          };
      }
      historyLoaded.current = true;
      setMessages((old) => mergeMessages(old, page.items));
      setDrafts((old) => ({
        ...old,
        ...Object.fromEntries(loadedDrafts.map((d) => [d.id, d])),
      }));
      setOlder(page.next_before);
    } catch (error) {
      if (!signal.aborted) setError(error);
    } finally {
      if (!signal.aborted) {
        olderLoading.current = false;
        setLoadingOlder(false);
      }
    }
  }
  async function loadConversations() {
    if (conversationLoading.current || !nextConversationPage) return;
    const signal = scope();
    conversationLoading.current = true;
    setLoadingConversations(true);
    try {
      await list(signal, nextConversationPage);
    } catch (error) {
      if (!signal.aborted) setError(error);
    } finally {
      if (!signal.aborted) {
        conversationLoading.current = false;
        setLoadingConversations(false);
      }
    }
  }
  const usable = credentials?.filter((c) => c.usable) ?? [];
  const returnTo = location.pathname + location.search;
  const errorNotice = (
    <>
      {!!error && <ErrorNotice error={error} retry={refresh} />}
    </>
  );
  const historyControls = (
    <>
      <div className="toolbar">
        <label>
          历史对话
          <select
            value={conversationID}
            onChange={(e) => chooseConversation(e.target.value)}
            disabled={busy}
          >
            <option value="">新对话</option>
            {current && !conversations.some((c) => c.id === current.id) && (
              <option value={current.id}>
                {current.title} · {current.id.slice(0, 6)}
              </option>
            )}
            {conversations.map((c) => (
              <option key={c.id} value={c.id}>
                {c.title} · {c.id.slice(0, 6)}
              </option>
            ))}
          </select>
        </label>
        {nextConversationPage > 0 && (
          <button
            className="button"
            disabled={loadingConversations}
            onClick={() => void loadConversations()}
          >
            {loadingConversations ? "正在加载对话…" : "加载更多对话"}
          </button>
        )}
        {conversationID && (
          <button
            className="button"
            disabled={busy}
            onClick={async () => {
              const signal = scope();
              setBusy(true);
              try {
                await request(`/agent/conversations/${conversationID}`, {
                  token,
                  signal,
                  method: "DELETE",
                });
                if (signal.aborted) return;
                setConversations((old) =>
                  old.filter((c) => c.id !== conversationID),
                );
                await list(signal);
                if (!signal.aborted) chooseConversation("");
              } catch (e) {
                if (!signal.aborted) setError(e);
              } finally {
                if (!signal.aborted) setBusy(false);
              }
            }}
          >
            删除此对话
          </button>
        )}
      </div>
    </>
  );
  const availability = (
    <>
      {!credentials ? (
        <Loading />
      ) : usable.length === 0 ? (
        <div className="content-card">
          <p>请先配置并验证 AI 供应商，再开始对话。</p>
          <Link
            className="button"
            to={`/api-keys?return_to=${encodeURIComponent(returnTo)}`}
          >
            配置 API
          </Link>
        </div>
      ) : null}
    </>
  );
  const modelControls =
    usable.length > 0 ? (
      <>
        <div className="form-grid">
          <label>
            对话 API
            <select
              value={credentialID}
              disabled={busy || active}
              onChange={(e) => {
                const c = usable.find(
                  (v) => (v.id ?? v.generation) === e.target.value,
                );
                setCredentialID(e.target.value);
                setProvider(c?.provider ?? "");
                setModel(c?.model ?? "");
              }}
            >
              {usable.map((c) => (
                <option key={c.id ?? c.generation} value={c.id ?? c.generation}>
                  {c.name ||
                    providers.find((p) => p.id === c.provider)?.name ||
                    c.provider}{" "}
                  · {c.masked_key}
                </option>
              ))}
            </select>
          </label>
          <label>
            对话模型
            <select
              value={model}
              disabled={busy || active}
              onChange={(e) => setModel(e.target.value)}
            >
              {providers
                .find((p) => p.id === provider)
                ?.models.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
            </select>
          </label>
        </div>
        <p className="settings-description">
          仅使用所选供应商的 API
          Key。切换供应商会将后续问题所需的历史与论文片段发送给该供应商；邮件默认模型保持独立。
        </p>
      </>
    ) : null;
  const latestReport = reportSnapshot?.report;
  const reportDisabled =
    busy ||
    active ||
    !usable.length ||
    !provider ||
    !model ||
    (!!conversationID && current?.id !== conversationID);
  const messagesView = (
    <div className="agent-messages" aria-live="polite" aria-label="对话记录">
      {kind === "paper" && latestReport?.result?.report && (
        <section className="paper-report-pinned" aria-label="论文报告">
          <h3>论文报告</h3>
          {!reportSnapshot?.matches_current_paper && (
            <p role="status" className="paper-report-stale">
              这份报告对应旧版论文材料，可按需重新生成。新问题会使用当前材料。
            </p>
          )}
          <PaperReportView
            result={latestReport.result}
            content={latestReport.content}
            citations={latestReport.citations ?? []}
            onRegenerate={() => void send(undefined, "paper_report")}
            disabled={reportDisabled}
          />
          <small>
            {latestReport.provider} / {latestReport.model}
          </small>
        </section>
      )}
      {kind === "paper" && !latestReport && (
        <div className="paper-report-start">
          <p>也可生成论文报告，集中解读问题、方法、实验、结果与局限。</p>
          <button
            className="button"
            disabled={
              busy ||
              active ||
              !usable.length ||
              !provider ||
              !model ||
              (!!conversationID && current?.id !== conversationID)
            }
            onClick={() => void send(undefined, "paper_report")}
          >
            生成论文报告
          </button>
          <small>
            短论文通常调用 6 次；长论文最多 24 次、15
            分钟。全文不可用时自动生成摘要版。
          </small>
        </div>
      )}
      {older > 0 && (
        <button
          className="button"
          disabled={loadingOlder}
          onClick={() => void loadOlder()}
        >
          {loadingOlder ? "正在加载消息…" : "加载更早消息"}
        </button>
      )}
      {!messages.length && !latestReport && (
        <p className="settings-description">
          {kind === "paper"
            ? "直接输入问题即可开始，也可以先生成一份论文报告。"
            : "描述你想关注的研究方向，例如：关注 cs.AI 中视觉语言模型的论文。"}
        </p>
      )}
      {messages.filter((m) => m.id !== latestReport?.id).map((m) => (
        <Fragment key={m.id}>
          <article
            data-message-id={m.id}
            className={`agent-message ${m.role}`}
            aria-label={m.role === "user" ? "你的消息" : "助手回复"}
          >
            <strong>{m.role === "user" ? "你" : "AI 助手"}</strong>
            {m.result?.report ? (
              <PaperReportView
                result={m.result}
                disabled={reportDisabled}
                content={m.content}
                citations={m.citations ?? []}
              />
            ) : (
              <>
                {m.result && <PaperScope result={m.result} />}
                <p className="agent-text">{m.content}</p>
              </>
            )}
            {m.role === "assistant" && (
              <small>
                {m.provider} / {m.model}
              </small>
            )}
            {(!m.result?.report ? (m.citations ?? []) : []).map((ref, i) => (
              <details key={ref.id + i}>
                <summary>
                  证据 {ref.id} · {ref.page ? `第 ${ref.page} 页` : "摘要"}
                </summary>
                <blockquote>{ref.quote}</blockquote>
                <a href={safeURL(ref.url)} target="_blank" rel="noreferrer">
                  查看 arXiv 原文
                </a>
              </details>
            ))}
          </article>
          {m.draft_id && drafts[m.draft_id] && (
            <SubscriptionDraftCard
              draft={drafts[m.draft_id]}
              latest={current?.latest_draft_id === m.draft_id}
              disabled={busy || active}
              onConfirm={confirm}
              onAdjust={() => inputRef.current?.focus()}
              onSaved={refresh}
            />
          )}
        </Fragment>
      ))}
    </div>
  );
  const runStatus = (
    <>
      {run && (
        <div role="status">
          <p>
            {active
              ? (progress[run.progress] ??
                (run.progress?.startsWith("extracting_batch_")
                  ? "正在分批阅读全文"
                  : "正在处理"))
              : run.state === "completed"
                ? "本轮已完成"
                : (outputFailureMessage(run.failure_code ?? "") ??
                  failures[run.failure_code ?? run.state] ??
                  `本轮未完成（${run.failure_code ?? run.state}），请手动重试。`)}
          </p>
          {run.state === "failed" && run.failedStep && (
            <details>
              <summary>失败详情</summary>
              <p>
                失败步骤：
                {run.failedStep.tool?.startsWith("extracting_batch_")
                  ? `第 ${run.failedStep.tool.slice("extracting_batch_".length)} 批全文证据提取`
                  : (progress[run.failedStep.tool ?? ""]?.replace(
                      /^正在/,
                      "",
                    ) ?? run.failedStep.tool)}
              </p>
              <p>原因代码：{run.failedStep.failure_code}</p>
              {run.failure_detail && (
                <>
                  <p>校验位置：{run.failure_detail.path}</p>
                  {run.failure_detail.rule && (
                    <p>
                      校验要求：
                      {validationRules[run.failure_detail.rule] ??
                        run.failure_detail.rule}
                    </p>
                  )}
                </>
              )}
              {run.failedStep.call_id && (
                <p>诊断编号：{run.failedStep.call_id}</p>
              )}
              <p>本轮未发布结果，未自动重复调用模型。</p>
            </details>
          )}
          {!!run.batch_total && active && (
            <p>
              已阅读 {run.batch_completed ?? 0} / {run.batch_total} 批
            </p>
          )}
          {(active || run.state === "failed") && run.effective_context_mode && (
            <PaperScope
              result={{
                context_mode: run.effective_context_mode,
                fallback_reason: run.fallback_reason,
              }}
            />
          )}
          {active && (
            <button
              className="button"
              onClick={async () => {
                const signal = scope();
                try {
                  await request(`/agent/runs/${run.id}/cancel`, {
                    token,
                    signal,
                    method: "POST",
                  });
                  if (!signal.aborted) await reload(signal);
                } catch (e) {
                  if (!signal.aborted) setError(e);
                }
              }}
            >
              停止本轮
            </button>
          )}
        </div>
      )}
      {run &&
        run.task !== "paper_report" &&
        !active &&
        run.state !== "completed" && (
          <button
            className="button"
            onClick={() =>
              setQuestion(
                [...messages].reverse().find((m) => m.role === "user")
                  ?.content ?? "",
              )
            }
          >
            重新编辑上次问题
          </button>
        )}
    </>
  );
  const sendDisabled =
    !usable.length ||
    !provider ||
    !model ||
    !question.trim() ||
    busy ||
    active ||
    (kind === "paper" && !!conversationID && current?.id !== conversationID);
  const selected = usable.find((c) => (c.id ?? c.generation) === credentialID);
  const providerName =
    providers.find((p) => p.id === provider)?.name ?? provider;
  const modelName =
    providers.find((p) => p.id === provider)?.models.find((m) => m.id === model)
      ?.name ?? model;
  return (
    <AssistantView
      scrollRef={scrollRef}
      title={kind === "paper" ? "AI 论文助手" : "订阅助手"}
      subtitle={
        kind === "paper"
          ? (paperTitle ?? "围绕当前论文提问")
          : "订阅配置模式 · 确认后才会创建"
      }
      onClose={onClose}
      settings={
        <>
          {historyControls}
          {modelControls}
          {kind === "paper" && (
            <label>
              解读资料
              <select
                value={mode}
                disabled={active || busy}
                onChange={(e) => setMode(e.target.value)}
              >
                <option value="fulltext">论文文字全文</option>
                <option value="abstract">仅标题和摘要</option>
              </select>
              <small>
                选择全文模式时，直接提问最多等待全文 20
                秒；超时或不可用时使用摘要。全文就绪后，下一次全文模式提问可使用全文；已完成的回答不会自动重答。
              </small>
            </label>
          )}
        </>
      }
      notices={
        <>
          {errorNotice}
          {kind === "paper" && paperID && (
            <PaperDocumentStatus
              paperID={paperID}
              refreshKey={[run?.id, run?.state, run?.progress].join(":")}
            />
          )}
          {availability}
        </>
      }
      messages={messagesView}
      status={runStatus}
      hint={
        kind === "subscription" ? (
          <>
            <ShieldCheck size={16} aria-hidden="true" />
            助手只生成草案，确认后才会创建订阅。
          </>
        ) : (
          `请求资料：${mode === "fulltext" ? "全文优先，最多等待 20 秒后使用摘要" : "仅标题和摘要"}`
        )
      }
      composer={
        <AssistantComposer
          inputRef={inputRef}
          question={question}
          onChange={setQuestion}
          onSend={send}
          disabled={sendDisabled}
          placeholder={
            kind === "paper"
              ? "直接提问，例如：这篇论文解决了什么问题？"
              : "描述研究方向、篇数或邮件偏好……"
          }
          modelLabel={[providerName, modelName].filter(Boolean).join(" · ")}
          configurationStatus={
            !credentials
              ? "配置加载中"
              : selected && provider && model
                ? "配置可用"
                : "未配置可用 API"
          }
        />
      }
    />
  );
}
