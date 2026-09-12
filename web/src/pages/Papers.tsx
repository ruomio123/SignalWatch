import { AgentDialog } from "../components/AgentChat";
import { useCountdown } from "../lib/useCountdown";
import { useCallback, useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Search, ExternalLink, FileText, Sparkles } from "lucide-react";
import { useAccount } from "../lib/account";
import { aiFailureMessage, request } from "../lib/api";
import { useSession } from "../lib/session";
import { useResource } from "../lib/useResource";
import type {
  Page,
  Paper,
  Subscription,
  SummaryResponse,
  Language,
} from "../lib/types";
import {
  ErrorNotice,
  Loading,
  Modal,
  PageTitle,
  Pagination,
  safeURL,
  Badge,
  Card,
  EmptyState,
} from "../components/Common";
export function Papers() {
  const { token } = useSession();
  const [params, setParams] = useSearchParams();
  const [page, setPage] = useState(1),
    [subscription, setSubscription] = useState(
      params.get("subscription_id") ?? "",
    ),
    [q, setQ] = useState(""),
    [search, setSearch] = useState("");
  const selected = Number(params.get("paper_id"));
  const list = useResource(
    useCallback(
      (signal: AbortSignal) =>
        request<Page<Paper>>(
          `/papers?page=${page}&page_size=20${subscription ? "&subscription_id=" + subscription : ""}${search ? "&q=" + encodeURIComponent(search) : ""}`,
          { token, signal },
        ),
      [token, page, subscription, search],
    ),
  );
  const subs = useResource(
    useCallback(
      (signal: AbortSignal) =>
        request<Page<Subscription>>("/subscriptions?page_size=100", {
          token,
          signal,
        }),
      [token],
    ),
  );
  return (
    <>
      <PageTitle title="匹配论文" description="只展示与你的订阅匹配的论文。" />
      <form
        className="toolbar"
        onSubmit={(e) => {
          e.preventDefault();
          setSearch(q);
          setPage(1);
        }}
      >
        <label className="search-field">
          搜索
          <span className="search-input">
            <Search size={16} aria-hidden="true" />
            <input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="标题或摘要"
            />
          </span>
        </label>
        <label>
          订阅
          <select
            value={subscription}
            onChange={(e) => {
              setSubscription(e.target.value);
              setPage(1);
            }}
          >
            <option value="">全部订阅</option>
            {subs.data?.items.map((s) => (
              <option value={s.id} key={s.id}>
                {s.name}
              </option>
            ))}
          </select>
        </label>
        <button className="button button-primary">搜索</button>
      </form>
      {list.loading ? (
        <Loading />
      ) : list.error ? (
        <ErrorNotice error={list.error} retry={list.reload} />
      ) : (
        list.data && (
          <>
            <Card className="card-flush">
              <section className="paper-list" aria-label="论文列表">
                {list.data.items.map((p) => (
                  <article className="paper-row" key={p.id}>
                    <div className="paper-meta">
                      {p.categories.map((c) => (
                        <Badge key={c} tone="info">
                          {c}
                        </Badge>
                      ))}
                      <time>{p.published_at.slice(0, 10)}</time>
                    </div>
                    <h2>
                      <button
                        className="paper-title"
                        onClick={() =>
                          setParams((prev) => {
                            const next = new URLSearchParams(prev);
                            next.set("paper_id", String(p.id));
                            return next;
                          })
                        }
                      >
                        {p.title}
                      </button>
                    </h2>
                    <p className="paper-authors">{p.authors.join(", ")}</p>
                    <p className="paper-abstract clamp">{p.abstract}</p>
                    <div className="badge-row">
                      {p.matches.map((m) => (
                        <Badge key={m.subscription_id}>
                          {m.subscription_name}
                          {m.subscription_active ? "" : "（已暂停）"}
                        </Badge>
                      ))}
                    </div>
                  </article>
                ))}
              </section>
              {!list.data.total && (
                <EmptyState
                  title={
                    search || subscription
                      ? "没有符合条件的论文"
                      : "暂无匹配论文"
                  }
                >
                  {search || subscription
                    ? "试试其他关键词或订阅。"
                    : "历史回填完成后，匹配论文会出现在这里。"}
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
      {selected > 0 && params.get("assistant") === "paper" && (
        <AgentDialog
          kind="paper"
          paperID={selected}
          onClose={() =>
            setParams((p) => {
              const n = new URLSearchParams(p);
              n.delete("assistant");
              n.delete("conversation");
              return n;
            })
          }
        />
      )}
      {selected > 0 && params.get("assistant") !== "paper" && (
        <PaperDetail
          key={selected}
          id={selected}
          onClose={() =>
            setParams((prev) => {
              const next = new URLSearchParams(prev);
              next.delete("paper_id");
              return next;
            })
          }
        />
      )}
    </>
  );
}
function PaperDetail({ id, onClose }: { id: number; onClose: () => void }) {
  const [, setParams] = useSearchParams();
  const { token } = useSession();
  const paper = useResource(
    useCallback(
      (signal: AbortSignal) =>
        request<Paper>("/papers/" + id, { token, signal }),
      [token, id],
    ),
  );
  return (
    <Modal title="论文详情" onClose={onClose} wide>
      {paper.loading ? (
        <Loading />
      ) : paper.error ? (
        <ErrorNotice error={paper.error} retry={paper.reload} />
      ) : (
        paper.data && (
          <>
            <div className="paper-reading">
              <div className="paper-meta">
                {paper.data.categories.map((c) => (
                  <Badge key={c} tone="info">
                    {c}
                  </Badge>
                ))}
                <time>{paper.data.published_at.slice(0, 10)}</time>
              </div>
              <h2>{paper.data.title}</h2>
              <p className="paper-authors">{paper.data.authors.join(", ")}</p>
              <section className="reading-section">
                <h3>摘要</h3>
                <p className="react-abstract">{paper.data.abstract}</p>
              </section>
              {paper.data.comments && <p>{paper.data.comments}</p>}
              <div className="react-actions">
                <a
                  className="button"
                  href={safeURL(paper.data.arxiv_url)}
                  target="_blank"
                  rel="noreferrer"
                >
                  <ExternalLink size={15} aria-hidden="true" />
                  arXiv 原文
                </a>
                <a
                  className="button"
                  href={safeURL(paper.data.pdf_url)}
                  target="_blank"
                  rel="noreferrer"
                >
                  <FileText size={15} aria-hidden="true" />
                  PDF
                </a>
              </div>
            </div>
            <button
              className="button button-primary"
              onClick={() =>
                setParams((p) => {
                  const n = new URLSearchParams(p);
                  n.set("assistant", "paper");
                  n.delete("conversation");
                  return n;
                })
              }
            >
              AI 全文对话
            </button>
            <PaperSummary id={id} />
          </>
        )
      )}
    </Modal>
  );
}
function PaperSummary({ id }: { id: number }) {
  const preferences = useAccount();
  if (preferences.loading) return <Loading />;
  if (preferences.error)
    return <ErrorNotice error={preferences.error} retry={preferences.reload} />;
  return (
    <PaperSummaryContent
      id={id}
      initialLanguage={preferences.data?.ai_language ?? "zh"}
    />
  );
}
function PaperSummaryContent({
  id,
  initialLanguage,
}: {
  id: number;
  initialLanguage: Language;
}) {
  const { token } = useSession();
  const [language, setLanguage] = useState<Language>(initialLanguage),
    [attempt, setAttempt] = useState(0),
    [result, setResult] = useState<SummaryResponse>(),
    [error, setError] = useState<unknown>(),
    [loading, setLoading] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    setResult(undefined);
    setError(undefined);
    setLoading(true);
    async function load(create = false) {
      try {
        const value = await request<SummaryResponse>(
          "/papers/" +
            id +
            "/ai-summary" +
            (create ? "" : "?language=" + language),
          {
            token,
            signal: controller.signal,
            method: create ? "POST" : "GET",
            body: create ? { language } : undefined,
          },
        );
        if (!current) return;
        setResult(value);
        setLoading(false);
        if (
          value.items.some(
            (i) => i.state === "pending" || i.state === "processing",
          )
        )
          timer = setTimeout(() => void load(), 2500);
      } catch (e) {
        if (current) {
          setError(e);
          setLoading(false);
        }
      }
    }
    void load(attempt > 0);
    return () => {
      current = false;
      controller.abort();
      if (timer) clearTimeout(timer);
    };
  }, [id, token, language, attempt]);
  const item = result?.items.find((i) => i.language === language);
  const wait = useCountdown(
    item?.retry_at ? Date.parse(item.retry_at) : undefined,
  );
  return (
    <section className="content-card ai-summary">
      <h3>
        <Sparkles size={18} aria-hidden="true" />
        AI 论文解读
      </h3>
      <label>
        解读语言
        <select
          value={language}
          onChange={(e) => {
            setAttempt(0);
            setLanguage(e.target.value as Language);
          }}
        >
          <option value="zh">中文</option>
          <option value="en">English</option>
        </select>
      </label>
      {!!error && <ErrorNotice error={error} />}
      <div role="status">
        <Badge
          tone={
            item?.state === "ready"
              ? "success"
              : item?.state === "failed"
                ? "danger"
                : "info"
          }
        >
          {loading
            ? "正在读取…"
            : item?.state === "ready"
              ? "已生成"
              : item?.state === "pending" || item?.state === "processing"
                ? "正在生成…"
                : item?.state === "failed"
                  ? "生成失败，可重试"
                  : "可请求生成解读"}
        </Badge>
      </div>
      {item?.failure_code && (
        <p role="alert">{aiFailureMessage(item.failure_code)}</p>
      )}
      {wait > 0 && <p role="status">请等待 {wait} 秒后手动重试。</p>}
      {item?.content && (
        <>
          <p>{item.content.summary}</p>
          <h4>主要贡献</h4>
          <ul>
            {item.content.contributions.map((c, i) => (
              <li key={i}>{c}</li>
            ))}
          </ul>
          <h4>方法</h4>
          <p>{item.content.method}</p>
          <h4>应用</h4>
          <ul>
            {item.content.applications.map((a, i) => (
              <li key={i}>
                {a.text}
                {a.inferred ? "（推测）" : ""}
              </li>
            ))}
          </ul>
          <p>{item.content.limitations}</p>
          {item.content.evidence.map((e, i) => (
            <blockquote key={i}>{e.quote}</blockquote>
          ))}
        </>
      )}
      <button
        className="button button-ghost"
        disabled={
          wait > 0 ||
          loading ||
          item?.state === "pending" ||
          item?.state === "processing"
        }
        onClick={() => setAttempt((n) => n + 1)}
      >
        生成 / 重新调用模型
      </button>
    </section>
  );
}
