import { useCallback } from "react";
import { Link } from "react-router-dom";
import { ArrowRight, Clock3, FileText, Mail, Rss } from "lucide-react";
import { useSession } from "../lib/session";
import { useAccount } from "../lib/account";
import { request } from "../lib/api";
import { useResource } from "../lib/useResource";
import type { Page, Paper, Subscription } from "../lib/types";
import {
  Card,
  EmptyState,
  ErrorNotice,
  Loading,
  PageTitle,
} from "../components/Common";
export function Dashboard() {
  const { token } = useSession();
  const profile = useAccount();
  const result = useResource(
    useCallback(
      async (signal: AbortSignal) => {
        const [subscriptions, papers] = await Promise.all([
          request<Page<Subscription>>(
            "/subscriptions?enabled=true&page_size=1",
            { token, signal },
          ),
          request<Page<Paper>>("/papers?page_size=3", { token, signal }),
        ]);
        return { subscriptions, papers };
      },
      [token],
    ),
  );
  return (
    <>
      <PageTitle
        title="研究概览"
        description="欢迎回来，看看最近有哪些值得关注的研究。"
      >
        <Link className="button button-primary" to="/subscriptions">
          <Rss size={16} />
          管理订阅
        </Link>
      </PageTitle>
      {result.loading || profile.loading ? (
        <Loading />
      ) : result.error || profile.error ? (
        <ErrorNotice
          error={result.error || profile.error}
          retry={() => {
            result.reload();
            profile.reload();
          }}
        />
      ) : (
        result.data &&
        profile.data && (
          <>
            <section className="metric-grid" aria-label="研究统计">
              {[
                {
                  label: "活跃订阅",
                  value: result.data.subscriptions.total,
                  hint: "持续追踪你的研究方向",
                  icon: Rss,
                },
                {
                  label: "匹配论文",
                  value: result.data.papers.total,
                  hint: "来自你的订阅匹配",
                  icon: FileText,
                },
                {
                  label: "每日邮件时间",
                  value: profile.data.digest_time,
                  hint: profile.data.timezone,
                  icon: Clock3,
                },
                {
                  label: "新订阅默认篇数",
                  value: profile.data.max_items_per_digest,
                  hint: "每个订阅可单独调整",
                  icon: Mail,
                },
              ].map(({ label, value, hint, icon: Icon }) => (
                <article className="metric-card" key={label}>
                  <p className="metric-label">
                    {label}
                    <Icon size={16} aria-hidden="true" />
                  </p>
                  <p className="metric-value">{value}</p>
                  <p className="metric-caption">{hint}</p>
                </article>
              ))}
            </section>
            <Card className="card-flush">
              <header className="card-header">
                <h2>
                  <FileText size={16} />
                  最近匹配
                </h2>
                <Link to="/papers">查看全部</Link>
              </header>
              {result.data.papers.items.length ? (
                result.data.papers.items.map((p) => (
                  <Link
                    className="dashboard-paper"
                    to={"/papers?paper_id=" + p.id}
                    key={p.id}
                  >
                    <span className="avatar">
                      <FileText size={16} />
                    </span>
                    <div>
                      <strong>{p.title}</strong>
                      <p>
                        {p.categories.join(" · ")} ·{" "}
                        {p.published_at.slice(0, 10)}
                      </p>
                    </div>
                    <ArrowRight size={16} />
                  </Link>
                ))
              ) : (
                <EmptyState title="等待你的第一篇匹配论文">
                  创建订阅后，后台会回填最近七天的本地论文。
                </EmptyState>
              )}
              <footer className="card-footer">
                <Link className="button button-ghost" to="/subscriptions">
                  调整研究方向
                  <ArrowRight size={15} />
                </Link>
              </footer>
            </Card>
          </>
        )
      )}
    </>
  );
}
