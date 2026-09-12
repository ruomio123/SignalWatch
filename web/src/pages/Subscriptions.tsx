import { useSearchParams } from "react-router-dom";
import { AgentChat } from "../components/AgentChat";
import {
  ResizableWorkspace,
  type WorkspaceConfig,
  type WorkspaceView,
} from "../components/ResizableWorkspace";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { Plus, Pencil, Pause, Play, Trash2 } from "lucide-react";
import { useRequestScope } from "../lib/useRequestScope";
import { useAccount } from "../lib/account";
import { request } from "../lib/api";
import { useSession } from "../lib/session";
import { useResource } from "../lib/useResource";
import type {
  Page,
  Subscription,
  SubscriptionInput,
  Source,
  Profile,
} from "../lib/types";
import {
  ErrorNotice,
  Loading,
  Modal,
  PageTitle,
  Pagination,
  Button,
  Badge,
  Card,
  EmptyState,
  SuccessNotice,
} from "../components/Common";
function deleteSubscription(
  subscription: Subscription,
  token: string | undefined,
  signal: AbortSignal,
) {
  return request<void>(`/subscriptions/${subscription.id}`, {
    token,
    signal,
    method: "DELETE",
    etag: `"${subscription.version}"`,
  });
}

const subscriptionLayout: WorkspaceConfig = {
  storageKey: "signalwatch.subscription-layout.v1",
  orderKey: "primaryFirst",
  primaryLabel: "订阅列表",
  primaryShortLabel: "订阅",
  assistantLabel: "订阅助手",
  primaryClassName: "subscriptions-list-panel",
  assistantClassName: "subscription-assistant-panel",
  gridClassName: "subscriptions-workspace",
  flowWhenClosed: true,
};

export function Subscriptions() {
  const { token } = useSession();
  const [params, setParams] = useSearchParams();
  const account = useAccount();
  const scope = useRequestScope();
  const titleRef = useRef<HTMLHeadingElement>(null);
  const assistantButton = useRef<HTMLButtonElement>(null);
  const primaryRef = useRef<HTMLElement>(null);
  const assistantRef = useRef<HTMLElement>(null);
  const [view, setView] = useState<WorkspaceView>("assistant");
  const focusAfterDelete = useRef(false);
  const pending = useRef(new Set<number>());
  const [pendingIDs, setPendingIDs] = useState(new Set<number>());
  const [deleting, setDeleting] = useState<Subscription>();
  const [actionError, setActionError] = useState<{
    id: number;
    error: unknown;
  }>();
  const [notice, setNotice] = useState("");
  const [page, setPage] = useState(1),
    [filter, setFilter] = useState(""),
    [editing, setEditing] = useState<Subscription | null | undefined>();
  const list = useResource(
    useCallback(
      (signal: AbortSignal) =>
        request<Page<Subscription>>(
          `/subscriptions?page=${page}&page_size=20${filter ? "&enabled=" + filter : ""}`,
          { token, signal },
        ),
      [token, page, filter],
    ),
  );
  const options = useResource(
    useCallback(
      (signal: AbortSignal) => request<Source[]>("/sources", { token, signal }),
      [token],
    ),
  );
  const creatableSources =
    options.data?.filter((source) => source.allowed_categories.length > 0) ??
    [];
  useEffect(() => {
    if (
      !list.data?.items.some((s) =>
        ["pending", "processing"].includes(s.backfill.state),
      )
    )
      return;
    const timer = setTimeout(list.reload, 5000);
    return () => clearTimeout(timer);
  }, [list.data, list.reload]);
  useEffect(() => {
    if (!list.loading && list.data?.page === page) {
      const lastPage = Math.max(1, Math.ceil(list.data.total / 20));
      if (page > lastPage) setPage(lastPage);
    }
  }, [list.data, list.loading, page]);
  useEffect(() => {
    if (focusAfterDelete.current && !deleting && editing === undefined) {
      focusAfterDelete.current = false;
      titleRef.current?.focus();
    }
  }, [deleting, editing, notice]);
  function reloadActions() {
    setActionError(undefined);
    setDeleting(undefined);
    list.reload();
  }
  async function act(subscription: Subscription, action: "toggle" | "delete") {
    if (pending.current.has(subscription.id)) return;
    const signal = scope();
    pending.current.add(subscription.id);
    setPendingIDs(new Set(pending.current));
    setNotice("");
    setActionError(undefined);
    try {
      if (action === "delete") {
        await deleteSubscription(subscription, token, signal);
      } else {
        await request(`/subscriptions/${subscription.id}`, {
          token,
          signal,
          method: "PATCH",
          body: { enabled: !subscription.enabled },
          etag: `"${subscription.version}"`,
        });
      }
      if (signal.aborted) return;
      if (action === "delete") {
        focusAfterDelete.current = true;
        setDeleting((current) =>
          current?.id === subscription.id ? undefined : current,
        );
      }
      setNotice(
        action === "delete"
          ? "订阅已删除。"
          : subscription.enabled
            ? "订阅已暂停。"
            : "订阅已启用。",
      );
      list.reload();
    } catch (error) {
      if (!signal.aborted) setActionError({ id: subscription.id, error });
    } finally {
      pending.current.delete(subscription.id);
      if (!signal.aborted) setPendingIDs(new Set(pending.current));
    }
  }
  const assistantOpen = params.get("assistant") === "subscription";
  useEffect(() => {
    if (!assistantOpen) return;
    setView("assistant");
    const frame = requestAnimationFrame(() =>
      assistantRef.current
        ?.querySelector<HTMLTextAreaElement>("textarea")
        ?.focus({ preventScroll: true }),
    );
    return () => cancelAnimationFrame(frame);
  }, [assistantOpen]);
  const closeAssistant = () => {
    setParams((p) => {
      const next = new URLSearchParams(p);
      next.delete("assistant");
      next.delete("conversation");
      return next;
    });
    setView("primary");
    assistantButton.current?.focus({ preventScroll: true });
  };
  return (
    <div
      className={`subscriptions-page${assistantOpen ? " has-assistant" : ""}`}
    >
      <PageTitle
        headingRef={titleRef}
        title="订阅"
        description="一个订阅关注一个分类，关键词匹配标题或摘要。"
      >
        <div className="react-actions" role="group" aria-label="创建订阅">
          <button
            className="button button-primary"
            disabled={!creatableSources.length || !account.data}
            onClick={() => {
              setNotice("");
              setEditing(null);
            }}
          >
            <Plus size={16} aria-hidden="true" />
            新建订阅
          </button>
          <button
            ref={assistantButton}
            aria-expanded={assistantOpen}
            className="button"
            onClick={() => {
              setView("assistant");
              setParams((p) => {
                const n = new URLSearchParams(p);
                n.set("assistant", "subscription");
                return n;
              });
              if (assistantOpen)
                assistantRef.current
                  ?.querySelector<HTMLTextAreaElement>("textarea")
                  ?.focus({ preventScroll: true });
            }}
          >
            AI 创建订阅
          </button>
        </div>
      </PageTitle>
      <ResizableWorkspace
        config={subscriptionLayout}
        enabled={assistantOpen}
        view={view}
        onViewChange={setView}
        primaryRef={primaryRef}
        assistantRef={assistantRef}
        onClose={closeAssistant}
        primary={
          <div className="subscriptions-content">
            {notice && <SuccessNotice>{notice}</SuccessNotice>}
            {actionError && actionError.id !== deleting?.id && (
              <ErrorNotice error={actionError.error} retry={reloadActions} />
            )}
            <div className="toolbar">
              <label>
                状态{" "}
                <select
                  value={filter}
                  onChange={(e) => {
                    setFilter(e.target.value);
                    setPage(1);
                  }}
                >
                  <option value="">全部</option>
                  <option value="true">已启用</option>
                  <option value="false">已暂停</option>
                </select>
              </label>
              <span className="result-count">
                {list.data ? `${list.data.total} 个订阅` : ""}
              </span>
            </div>
            {!!account.error && (
              <ErrorNotice error={account.error} retry={account.reload} />
            )}
            {!!options.error && (
              <ErrorNotice error={options.error} retry={options.reload} />
            )}
            {options.data && !options.error && !creatableSources.length && (
              <p className="muted" role="status">
                暂无可用论文来源，暂时无法手动创建订阅。
              </p>
            )}
            {list.loading ? (
              <Loading />
            ) : list.error ? (
              <ErrorNotice error={list.error} retry={list.reload} />
            ) : (
              list.data && (
                <>
                  <Card className="card-flush">
                    {list.data.items.length > 0 ? (
                      <table
                        className="subscription-table"
                        aria-label="订阅列表"
                      >
                        <thead>
                          <tr>
                            {[
                              "订阅名称",
                              "来源与规则",
                              "状态",
                              "邮件配置",
                              "历史回填",
                              "操作",
                            ].map((label) => (
                              <th scope="col" key={label}>
                                {label}
                              </th>
                            ))}
                          </tr>
                        </thead>
                        <tbody>
                          {list.data.items.map((s) => (
                            <tr key={s.id}>
                              <td>
                                <h2>{s.name}</h2>
                                {s.objective && (
                                  <p className="muted">{s.objective}</p>
                                )}
                              </td>
                              <td>
                                <span className="cell-label">来源与规则</span>
                                <span>{s.source.name}</span>{" "}
                                <Badge tone="info">{s.rules.category}</Badge>
                                <div className="badge-row">
                                  {s.rules.keywords.length ? (
                                    s.rules.keywords.map((k) => (
                                      <Badge key={k}>{k}</Badge>
                                    ))
                                  ) : (
                                    <span className="muted">整个分类</span>
                                  )}
                                </div>
                              </td>
                              <td>
                                <span className="cell-label">状态</span>
                                <Badge tone={s.enabled ? "success" : "neutral"}>
                                  {s.enabled ? "已启用" : "已暂停"}
                                </Badge>
                              </td>
                              <td>
                                <span className="cell-label">邮件配置</span>
                                <p>每天 {s.max_items_per_digest} 篇</p>
                                <p className="muted">
                                  AI{" "}
                                  {s.digest_ai_enabled
                                    ? s.digest_ai_language === "zh"
                                      ? "中文导读"
                                      : "英文导读"
                                    : "关闭"}
                                </p>
                              </td>
                              <td>
                                <span className="cell-label">历史回填</span>
                                <div className="backfill-state" role="status">
                                  <BackfillBadge state={s.backfill.state} />
                                  <span className="muted">
                                    已处理 {s.backfill.processed}
                                    <br />
                                    匹配 {s.backfill.matched}
                                  </span>
                                </div>
                              </td>
                              <td>
                                <div
                                  className="subscription-actions"
                                  role="group"
                                  aria-label={`${s.name}的操作`}
                                  aria-busy={pendingIDs.has(s.id)}
                                >
                                  <Button
                                    variant="ghost"
                                    aria-label={`编辑 ${s.name}`}
                                    disabled={pendingIDs.has(s.id)}
                                    onClick={() => {
                                      setNotice("");
                                      setActionError(undefined);
                                      setEditing(s);
                                    }}
                                  >
                                    <Pencil size={14} aria-hidden="true" />
                                    编辑
                                  </Button>
                                  <Button
                                    variant="ghost"
                                    aria-label={`${s.enabled ? "暂停" : "启用"} ${s.name}`}
                                    disabled={pendingIDs.has(s.id)}
                                    onClick={() => void act(s, "toggle")}
                                  >
                                    {s.enabled ? (
                                      <Pause size={14} aria-hidden="true" />
                                    ) : (
                                      <Play size={14} aria-hidden="true" />
                                    )}
                                    {s.enabled ? "暂停" : "启用"}
                                  </Button>
                                  <Button
                                    variant="danger"
                                    aria-label={`删除 ${s.name}`}
                                    disabled={pendingIDs.has(s.id)}
                                    onClick={() => {
                                      setNotice("");
                                      setActionError(undefined);
                                      setDeleting(s);
                                    }}
                                  >
                                    <Trash2 size={14} aria-hidden="true" />
                                    删除
                                  </Button>
                                </div>
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    ) : (
                      <EmptyState
                        title={filter ? "没有符合条件的订阅" : "还没有订阅"}
                      >
                        {filter
                          ? "试试切换状态筛选。"
                          : "创建订阅后，将自动回填最近七天的本地论文。"}
                      </EmptyState>
                    )}
                    <Pagination
                      page={page}
                      total={list.data.total}
                      pageSize={20}
                      onChange={setPage}
                    />
                  </Card>
                </>
              )
            )}
          </div>
        }
        assistant={
          <AgentChat
            kind="subscription"
            onCreated={list.reload}
            onClose={closeAssistant}
          />
        }
      />
      {deleting && (
        <Modal title="删除订阅" onClose={() => setDeleting(undefined)}>
          <div className="react-form">
            <p>
              确认删除订阅“<strong>{deleting.name}</strong>
              ”？已匹配的阅读记录会保留。
            </p>
            {actionError?.id === deleting.id && (
              <ErrorNotice error={actionError.error} retry={reloadActions} />
            )}
            <div className="form-footer">
              <Button onClick={() => setDeleting(undefined)}>取消</Button>
              <Button
                variant="danger"
                disabled={pendingIDs.has(deleting.id)}
                onClick={() => void act(deleting, "delete")}
              >
                {pendingIDs.has(deleting.id) ? "正在删除…" : "确认删除"}
              </Button>
            </div>
          </div>
        </Modal>
      )}
      {editing !== undefined &&
        options.data &&
        account.data &&
        (editing !== null || creatableSources.length > 0) && (
          <SubscriptionEditor
            existing={editing}
            sources={editing ? options.data : creatableSources}
            defaults={account.data}
            onClose={() => setEditing(undefined)}
            onSaved={(message) => {
              setEditing(undefined);
              setNotice(message ?? "");
              list.reload();
            }}
          />
        )}
    </div>
  );
}
function BackfillBadge({ state }: { state: string }) {
  const label: Record<string, string> = {
    pending: "等待处理",
    processing: "处理中",
    complete: "完成",
    failed: "失败",
    cancelled: "已取消",
    none: "未启动",
  };
  return (
    <Badge
      tone={
        state === "complete"
          ? "success"
          : state === "failed"
            ? "danger"
            : ["pending", "processing"].includes(state)
              ? "info"
              : "neutral"
      }
    >
      {label[state] ?? state}
    </Badge>
  );
}
function SubscriptionEditor({
  existing,
  sources,
  defaults,
  onClose,
  onSaved,
}: {
  existing: Subscription | null;
  sources: Source[];
  defaults: Profile;
  onClose: () => void;
  onSaved: (message?: string) => void;
}) {
  const { token } = useSession();
  const signal = useRequestScope();
  const [value, setValue] = useState<SubscriptionInput>(() => ({
    source_id: existing?.source.id ?? sources[0]?.id,
    name: existing?.name ?? "",
    objective: existing?.objective ?? "",
    enabled: existing?.enabled ?? true,
    max_items_per_digest:
      existing?.max_items_per_digest ?? defaults.max_items_per_digest,
    digest_ai_enabled: existing?.digest_ai_enabled ?? false,
    digest_ai_language: existing?.digest_ai_language ?? "zh",
    rules: existing?.rules ?? {
      category: sources[0]?.allowed_categories[0] ?? "",
      keywords: [],
    },
  }));
  const [keywords, setKeywords] = useState(value.rules.keywords.join("\n")),
    [error, setError] = useState<unknown>(),
    [busy, setBusy] = useState(false);
  const set = <K extends keyof SubscriptionInput>(
    key: K,
    v: SubscriptionInput[K],
  ) => setValue((x) => ({ ...x, [key]: v }));
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    const { source_id, enabled, ...editable } = value;
    const body = {
      ...editable,
      ...(!existing ? { source_id, enabled } : {}),
      rules: {
        ...value.rules,
        keywords: keywords
          .split(/[\n,，]/)
          .map((s) => s.trim())
          .filter(Boolean),
      },
    };
    try {
      await request("/subscriptions" + (existing ? "/" + existing.id : ""), {
        token,
        signal: signal(),
        method: existing ? "PATCH" : "POST",
        body,
        etag: existing ? `"${existing.version}"` : undefined,
      });
      if (!signal().aborted)
        onSaved(
          existing ? "订阅已保存。" : "订阅已创建，历史论文将在后台回填。",
        );
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal title={existing ? "编辑订阅" : "新建订阅"} onClose={onClose}>
      <form className="react-form" onSubmit={save}>
        {!!error && <ErrorNotice error={error} retry={() => onSaved()} />}
        <fieldset className="form-group">
          <legend>基本信息</legend>
          <label>
            订阅名称
            <input
              required
              maxLength={100}
              value={value.name}
              onChange={(e) => set("name", e.target.value)}
            />
          </label>
          <label>
            研究目标
            <textarea
              maxLength={500}
              value={value.objective ?? ""}
              onChange={(e) => set("objective", e.target.value || null)}
            />
          </label>
        </fieldset>
        <fieldset className="form-group">
          <legend>匹配规则</legend>
          <div className="form-grid">
            <label>
              来源
              <select
                disabled={!!existing}
                value={value.source_id}
                onChange={(e) => {
                  const s = sources.find(
                    (s) => s.id === Number(e.target.value),
                  );
                  if (!s) return;
                  setValue((v) => ({
                    ...v,
                    source_id: s.id,
                    rules: {
                      ...v.rules,
                      category: s.allowed_categories[0] ?? "",
                    },
                  }));
                }}
              >
                {sources.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              分类
              <select
                value={value.rules.category}
                onChange={(e) =>
                  set("rules", { ...value.rules, category: e.target.value })
                }
              >
                {sources
                  .find((s) => s.id === value.source_id)
                  ?.allowed_categories.map((c) => (
                    <option key={c}>{c}</option>
                  ))}
              </select>
            </label>
          </div>
          <label>
            关键词（每行一个，留空匹配整个分类）
            <textarea
              value={keywords}
              onChange={(e) => setKeywords(e.target.value)}
            />
          </label>
        </fieldset>
        <fieldset className="form-group">
          <legend>邮件设置</legend>
          <div className="form-grid">
            <label
              className={existing ? "subscription-digest-limit" : undefined}
            >
              每日论文上限
              <input
                type="number"
                min={1}
                max={20}
                required
                value={value.max_items_per_digest}
                onChange={(e) =>
                  set("max_items_per_digest", Number(e.target.value))
                }
              />
            </label>
            {!existing && (
              <label className="react-check">
                <input
                  type="checkbox"
                  checked={value.enabled}
                  onChange={(e) => set("enabled", e.target.checked)}
                />
                启用订阅
              </label>
            )}
            <label className="react-check">
              <input
                type="checkbox"
                checked={value.digest_ai_enabled}
                onChange={(e) => set("digest_ai_enabled", e.target.checked)}
              />
              每日邮件 AI 导读
            </label>
            <label>
              AI 导读语言
              <select
                value={value.digest_ai_language}
                onChange={(e) =>
                  set("digest_ai_language", e.target.value as "zh" | "en")
                }
              >
                <option value="zh">中文</option>
                <option value="en">English</option>
              </select>
            </label>
          </div>
        </fieldset>
        <div className="form-footer">
          <button className="button button-primary" disabled={busy}>
            {busy ? "正在提交…" : "保存订阅"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
