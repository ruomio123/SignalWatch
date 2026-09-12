import {
  Fragment,
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import { request } from "../lib/api";
import { useSession } from "../lib/session";
import { useRequestScope } from "../lib/useRequestScope";
import { ErrorNotice, Loading, Modal, safeURL } from "./Common";
import type { Configuration, Provider } from "../lib/types";

import {
  SubscriptionAssistantView,
  SubscriptionComposer,
} from "./SubscriptionAssistant";
import { SubscriptionDraftCard, type Draft } from "./SubscriptionDraft";

type Conversation = {
  id: string;
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
  id: number;
  run_id: string;
  role: string;
  content: string;
  citations: Citation[];
  draft_id?: string;
  provider: string;
  model: string;
};
type Run = {
  id: string;
  state: string;
  progress: string;
  failure_code?: string;
  provider: string;
  model: string;
};
const progress: Record<string, string> = {
  queued: "等待处理",
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
const failures: Record<string, string> = {
  document_unavailable:
    "全文准备失败或超时。可选择仅基于摘要继续，方法及实验细节可能无法回答。",
  result_unknown: "模型调用结果未知，未自动重试。你可以手动重新发送。",
  timeout: "模型响应超时，未自动重试。",
  invalid_citation: "本次回答的证据校验未通过，请手动重试。",
  budget_exhausted: "本轮已达到执行上限，可以缩小问题范围后继续。",
  configuration_changed: "模型配置已变化，请重新选择模型。",
  access_or_configuration_changed: "论文访问权限或模型配置已变化。",
  daily_limit: "今日模型调用已达上限。",
  cancelled: "本轮已停止。",
};
export function AgentDialog({
  kind,
  paperID,
  onClose,
  onCreated,
}: {
  kind: "paper" | "subscription";
  paperID?: number;
  onClose: () => void;
  onCreated?: () => void;
}) {
  return (
    <Modal
      title={kind === "paper" ? "AI 论文助手" : "AI 订阅助手"}
      onClose={onClose}
      wide
    >
      <AgentChat kind={kind} paperID={paperID} onCreated={onCreated} />
    </Modal>
  );
}
export function AgentChat({
  kind,
  paperID,
  onCreated,
  onClose,
}: {
  kind: "paper" | "subscription";
  paperID?: number;
  onCreated?: () => void;
  onClose?: () => void;
}) {
  const { token } = useSession();
  const scope = useRequestScope();
  const location = useLocation();
  const [params, setParams] = useSearchParams();
  const conversationID = params.get("conversation") ?? "";
  const [credentials, setCredentials] = useState<Configuration[]>();
  const [providers, setProviders] = useState<Provider[]>([]);
  const [provider, setProvider] = useState("");
  const [credentialID, setCredentialID] = useState("");
  const [model, setModel] = useState("");
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [messages, setMessages] = useState<Message[]>([]);
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const [current, setCurrent] = useState<Conversation>();
  const [run, setRun] = useState<Run>();
  const [question, setQuestion] = useState("");
  const [mode, setMode] = useState("fulltext");
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const [older, setOlder] = useState(0);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const reloadVersion = useRef(0);
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
    async (signal: AbortSignal) => {
      const result = await request<{ items: Conversation[] }>(
        `/agent/conversations?kind=${kind}${paperID ? `&paper_id=${paperID}` : ""}`,
        { token, signal },
      );
      setConversations(result.items);
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
        ]);
      let [c, m] = await loadSnapshot();
      const latestRun = c.active_run_id ?? m.items[m.items.length - 1]?.run_id;
      let latest: Run | undefined;
      if (latestRun) {
        const result = await request<{ run: Run }>(`/agent/runs/${latestRun}`, {
          token,
          signal,
        });
        latest = result.run;
        // Completion may commit between the message read and the run read.
        // Read again after that commit before stopping the poller.
        if (
          latest.state === "completed" &&
          !m.items.some((v) => v.run_id === latestRun && v.role === "assistant")
        ) {
          [c, m] = await loadSnapshot();
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
      setMessages(m.items);
      setOlder(m.next_before);
      setDrafts(Object.fromEntries(ds.map((d) => [d.id, d])));
      setRun(latest);
    },
    [conversationID, token],
  );
  useEffect(() => {
    const controller = new AbortController();
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
  async function send(e: FormEvent) {
    e.preventDefault();
    if (
      !question.trim() ||
      busy ||
      active ||
      !usable.length ||
      !provider ||
      !model
    )
      return;
    setBusy(true);
    setError(undefined);
    const text = question;
    const body = JSON.stringify({
      question: text,
      provider,
      credential_id: credentialID,
      model,
      context_mode: mode,
    });
    if (submission.current?.body !== body)
      submission.current = { key: crypto.randomUUID(), body };
    try {
      let id = conversationID;
      if (!id) {
        const c = await request<Conversation>("/agent/conversations", {
          token,
          signal: scope(),
          method: "POST",
          body: { kind, ...(paperID ? { paper_id: paperID } : {}) },
        });
        id = c.id;
        chooseConversation(id);
      }
      const value = await request<{ run_id: string; run: Run }>(
        `/agent/conversations/${id}/messages`,
        {
          token,
          signal: scope(),
          method: "POST",
          body: {
            ...JSON.parse(body),
            idempotency_key: submission.current!.key,
          },
        },
      );
      if (scope().aborted) return;
      submission.current = undefined;
      setQuestion("");
      setRun(value.run);
      await reload(scope(), id);
      await list(scope());
    } catch (e) {
      if (!scope().aborted) setError(e);
    } finally {
      if (!scope().aborted) setBusy(false);
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
    try {
      await request(`/agent/subscription-drafts/${d.id}/confirm`, {
        token,
        signal: scope(),
        method: "POST",
        body: { version: d.version },
      });
      await reload(scope());
      onCreated?.();
    } catch (e) {
      if (!scope().aborted) setError(e);
    } finally {
      if (!scope().aborted) setBusy(false);
    }
  }
  const usable = credentials?.filter((c) => c.usable) ?? [];
  const returnTo = location.pathname + location.search;
  const errorNotice = (
    <>
      {!!error && (
        <ErrorNotice
          error={error}
          retry={() => {
            setError(undefined);
            void reload(scope()).catch(setError);
          }}
        />
      )}
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
            {conversations.map((c) => (
              <option key={c.id} value={c.id}>
                {c.title} · {c.id.slice(0, 6)}
              </option>
            ))}
          </select>
        </label>
        {conversationID && (
          <button
            className="button"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                await request(`/agent/conversations/${conversationID}`, {
                  token,
                  signal: scope(),
                  method: "DELETE",
                });
                chooseConversation("");
                await list(scope());
              } catch (e) {
                setError(e);
              } finally {
                setBusy(false);
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
  const messagesView = (
    <div className="agent-messages" aria-live="polite" aria-label="对话记录">
      {older > 0 && (
        <button
          className="button"
          onClick={async () => {
            try {
              const m = await request<{
                items: Message[];
                next_before: number;
              }>(
                `/agent/conversations/${conversationID}/messages?before=${older}`,
                { token, signal: scope() },
              );
              setMessages((old) => [...m.items, ...old]);
              setOlder(m.next_before);
            } catch (e) {
              setError(e);
            }
          }}
        >
          加载更早消息
        </button>
      )}
      {!messages.length && (
        <p className="settings-description">
          {kind === "paper"
            ? "可以问：论文解决了什么问题？实验如何验证方法？有哪些局限？"
            : "描述你想关注的研究方向，例如：关注 cs.AI 中视觉语言模型的论文。"}
        </p>
      )}
      {messages.map((m) => (
        <Fragment key={m.id}>
          <article
            className={`agent-message ${m.role}`}
            aria-label={m.role === "user" ? "你的消息" : "助手回复"}
          >
            <strong>{m.role === "user" ? "你" : "AI 助手"}</strong>
            <p className="agent-text">{m.content}</p>
            {m.role === "assistant" && (
              <small>
                {m.provider} / {m.model}
              </small>
            )}
            {(m.citations ?? []).map((ref, i) => (
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
              onSaved={() => void reload(scope()).catch(setError)}
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
              ? (progress[run.progress] ?? "正在处理")
              : run.state === "completed"
                ? "本轮已完成"
                : (failures[run.failure_code ?? run.state] ??
                  `本轮未完成（${run.failure_code ?? run.state}），请手动重试。`)}
          </p>
          {active && (
            <button
              className="button"
              onClick={async () => {
                try {
                  await request(`/agent/runs/${run.id}/cancel`, {
                    token,
                    signal: scope(),
                    method: "POST",
                  });
                  setRun({ ...run, state: "cancelled" });
                } catch (e) {
                  setError(e);
                }
              }}
            >
              停止本轮
            </button>
          )}
        </div>
      )}
      {run && !active && run.state !== "completed" && (
        <button
          className="button"
          onClick={() =>
            setQuestion(
              [...messages].reverse().find((m) => m.role === "user")?.content ??
                "",
            )
          }
        >
          重新编辑上次问题
        </button>
      )}
    </>
  );
  const sendDisabled =
    !usable.length || !provider || !model || !question.trim() || busy || active;
  if (kind === "subscription") {
    const selected = usable.find(
      (c) => (c.id ?? c.generation) === credentialID,
    );
    const providerName =
      providers.find((p) => p.id === provider)?.name ?? provider;
    const modelName =
      providers
        .find((p) => p.id === provider)
        ?.models.find((m) => m.id === model)?.name ?? model;
    return (
      <SubscriptionAssistantView
        onClose={onClose}
        settings={
          <>
            {historyControls}
            {modelControls}
          </>
        }
        notices={
          <>
            {errorNotice}
            {availability}
          </>
        }
        messages={messagesView}
        status={runStatus}
        composer={
          <SubscriptionComposer
            inputRef={inputRef}
            question={question}
            onChange={setQuestion}
            onSend={send}
            disabled={sendDisabled}
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
  return (
    <section className="agent-chat">
      {errorNotice}
      {historyControls}
      {availability}
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
          <small>全文解读支持文字证据，暂不识别图像、复杂表格和扫描件。</small>
        </label>
      )}

      {messagesView}
      {runStatus}
      <form className="react-form" onSubmit={send}>
        <label>
          你的问题
          <textarea
            maxLength={2000}
            rows={3}
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
            placeholder={
              kind === "paper" ? "这篇论文具体讲了什么？" : "我想关注……"
            }
          />
        </label>
        <button
          className="button button-primary"
          disabled={
            !usable.length ||
            !provider ||
            !model ||
            !question.trim() ||
            busy ||
            active
          }
        >
          发送
        </button>
      </form>
    </section>
  );
}
