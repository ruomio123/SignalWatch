import { ResizablePaperWorkspace } from "../components/ResizablePaperWorkspace";
import { AgentChat } from "../components/AgentChat";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { useSearchParams } from "react-router-dom";
import {
  Search,
  ExternalLink,
  FileText,
  Sparkles,
  ArrowLeft,
} from "lucide-react";
import { request } from "../lib/api";
import { useSession } from "../lib/session";
import { useResource } from "../lib/useResource";
import type { Page, Paper, Subscription } from "../lib/types";
import {
  ErrorNotice,
  Loading,
  PageTitle,
  Pagination,
  safeURL,
  Badge,
  Card,
  EmptyState,
  Button,
} from "../components/Common";

function positiveID(value: string | null) {
  const id = Number(value);
  return Number.isSafeInteger(id) && id > 0 ? id : 0;
}

export function Papers() {
  const { token } = useSession();
  const [params, setParams] = useSearchParams();
  const page = positiveID(params.get("page")) || 1;
  const subscription = params.get("subscription_id") ?? "";
  const search = params.get("q") ?? "";
  const [q, setQ] = useState(search);
  const selected = positiveID(params.get("paper_id"));
  const listRoot = useRef<HTMLDivElement>(null);
  const listTitle = useRef<HTMLHeadingElement>(null);
  const listPosition = useRef({ y: 0, paperID: 0 });
  const previousSelected = useRef(selected);
  useEffect(() => setQ(search), [search]);
  const updateList = (values: Record<string, string>) =>
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      for (const [key, value] of Object.entries(values)) {
        if (value && !(key === "page" && value === "1")) next.set(key, value);
        else next.delete(key);
      }
      return next;
    });
  const returnToList = () =>
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      next.delete("paper_id");
      next.delete("assistant");
      next.delete("conversation");
      return next;
    });
  const list = useResource(
    useCallback(
      (signal: AbortSignal) =>
        request<Page<Paper>>(
          `/papers?page=${page}&page_size=20${subscription ? "&subscription_id=" + encodeURIComponent(subscription) : ""}${search ? "&q=" + encodeURIComponent(search) : ""}`,
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
  useLayoutEffect(() => {
    const previous = previousSelected.current;
    previousSelected.current = selected;
    if (selected) {
      window.scrollTo(0, 0);
    } else if (previous) {
      const trigger = listRoot.current?.querySelector<HTMLButtonElement>(
        `[data-paper-id="${listPosition.current.paperID}"]`,
      );
      (trigger ?? listTitle.current)?.focus({ preventScroll: true });
      window.scrollTo(0, listPosition.current.y);
    }
  }, [selected]);
  return (
    <>
      <div className="papers-list-page" ref={listRoot} hidden={selected > 0}>
        <PageTitle
          title="匹配论文"
          headingRef={listTitle}
          description="只展示与你的订阅匹配的论文。"
        />
        {params.has("paper_id") && !selected && (
          <ErrorNotice
            error={new Error("论文编号无效，请返回论文列表。")}
            retry={returnToList}
          />
        )}
        <form
          className="toolbar"
          onSubmit={(e) => {
            e.preventDefault();
            updateList({ q, page: "1" });
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
                updateList({ subscription_id: e.target.value, page: "1" });
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
                          data-paper-id={p.id}
                          onClick={() => {
                            listPosition.current = {
                              y: window.scrollY,
                              paperID: p.id,
                            };
                            setParams((prev) => {
                              const next = new URLSearchParams(prev);
                              next.set("paper_id", String(p.id));
                              next.delete("assistant");
                              next.delete("conversation");
                              return next;
                            });
                          }}
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
                  onChange={(next) => updateList({ page: String(next) })}
                />
              </Card>
            </>
          )
        )}
      </div>
      {selected > 0 && (
        <PaperDetail key={selected} id={selected} onBack={returnToList} />
      )}
    </>
  );
}

function PaperDetail({ id, onBack }: { id: number; onBack: () => void }) {
  const { token } = useSession();
  const [params, setParams] = useSearchParams();
  const assistantOpen = params.get("assistant") === "paper";
  const [view, setView] = useState<"paper" | "assistant">(
    assistantOpen ? "assistant" : "paper",
  );
  const root = useRef<HTMLElement>(null);
  const titleRef = useRef<HTMLHeadingElement>(null);
  const assistantButton = useRef<HTMLButtonElement>(null);
  const assistantRef = useRef<HTMLElement>(null);
  const readingRef = useRef<HTMLElement>(null);
  const paper = useResource(
    useCallback(
      (signal: AbortSignal) =>
        request<Paper>(`/papers/${id}`, { token, signal }),
      [id, token],
    ),
  );
  const ready = !!paper.data && !paper.error && !paper.loading;
  useLayoutEffect(() => {
    const element = root.current;
    if (!element) return;
    titleRef.current?.focus({ preventScroll: true });
  }, []);
  useEffect(() => {
    if (!assistantOpen || !ready) return;
    setView("assistant");
    const frame = requestAnimationFrame(() =>
      assistantRef.current
        ?.querySelector<HTMLTextAreaElement>("textarea")
        ?.focus({ preventScroll: true }),
    );
    return () => cancelAnimationFrame(frame);
  }, [assistantOpen, ready]);
  const closeAssistant = () => {
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      next.delete("assistant");
      next.delete("conversation");
      return next;
    });
    setView("paper");
    assistantButton.current?.focus({ preventScroll: true });
  };
  return (
    <section ref={root} className="paper-detail-page" aria-label="论文详情">
      <div className="paper-detail-navigation">
        <Button variant="ghost" onClick={onBack}>
          <ArrowLeft size={16} aria-hidden="true" />
          返回论文列表
        </Button>
      </div>
      <PageTitle title="论文详情" headingRef={titleRef}>
        <button
          ref={assistantButton}
          className="button button-primary"
          disabled={!ready}
          aria-expanded={assistantOpen && ready}
          onClick={() => {
            setView("assistant");
            if (assistantOpen)
              assistantRef.current
                ?.querySelector<HTMLTextAreaElement>("textarea")
                ?.focus({ preventScroll: true });
            else
              setParams((prev) => {
                const next = new URLSearchParams(prev);
                next.set("assistant", "paper");
                next.delete("conversation");
                return next;
              });
          }}
        >
          <Sparkles size={16} aria-hidden="true" />
          AI 论文助手
        </button>
      </PageTitle>
      <ResizablePaperWorkspace
        enabled={assistantOpen && ready}
        view={view}
        onViewChange={setView}
        readingRef={readingRef}
        assistantRef={assistantRef}
        paper={
          paper.loading ? (
            <Loading />
          ) : paper.error ? (
            <ErrorNotice error={paper.error} retry={paper.reload} />
          ) : (
            paper.data && (
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
            )
          )
        }
        assistant={
          <AgentChat
            kind="paper"
            paperID={id}
            paperTitle={paper.data?.title ?? ""}
            onClose={closeAssistant}
          />
        }
      />
    </section>
  );
}
