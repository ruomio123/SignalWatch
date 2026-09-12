import { useCallback, useState } from "react";
import { useAccount } from "../lib/account";
import { useSession } from "../lib/session";
import { useResource } from "../lib/useResource";
import { request, aiFailureMessage } from "../lib/api";
import type { Usage, FeatureUsage, AICall } from "../lib/types";
import { Card, Loading, ErrorNotice, EmptyState, Badge } from "./Common";
export function UsageTable() {
  const account = useAccount();
  const { token } = useSession();
  const usage = useResource(
    useCallback(
      (signal: AbortSignal) =>
        request<{ items: Usage[]; today?: FeatureUsage[] }>("/ai/usage", {
          token,
          signal,
        }),
      [token],
    ),
  );
  return (
    <Card className="card-flush">
      <header className="card-header">
        <h2>最近 30 天用量（UTC）</h2>
      </header>
      <p className="settings-description">
        按 UTC
        日期统计调用尝试，包含失败和结果未知；这是应用记录，不是供应商账单。
      </p>
      {usage.data?.today?.map((u) => (
        <p key={u.feature} className="settings-description">
          {featureName(u.feature)}：今日 {u.calls} 次 ·{" "}
          {u.daily_limit === 0 ? "每日不限次数" : `剩余 ${u.remaining} 次`} ·
          最短间隔 {u.min_interval_seconds} 秒
          {u.reset_at &&
            ` · 恢复时间 ${new Date(u.reset_at).toLocaleString(undefined, { timeZone: account.data?.timezone ?? "UTC" })}`}
        </p>
      ))}
      {usage.loading ? (
        <Loading />
      ) : usage.error ? (
        <ErrorNotice error={usage.error} retry={usage.reload} />
      ) : (
        <div className="usage-table">
          <table>
            <thead>
              <tr>
                <th>日期 / 功能</th>
                <th>调用 / 成功 / 失败 / 未知</th>
                <th>输入 / 输出 Token</th>
                <th>缺失用量</th>
              </tr>
            </thead>
            <tbody>
              {!usage.data?.items.length && (
                <tr>
                  <td colSpan={4}>
                    <EmptyState title="暂无调用记录">
                      使用 AI 解读或测试连接后，用量将显示在这里。
                    </EmptyState>
                  </td>
                </tr>
              )}
              {usage.data?.items.map((u) => (
                <tr key={u.day + u.feature}>
                  <td>
                    {u.day} / {featureName(u.feature)}
                  </td>
                  <td>
                    {u.calls} / {u.succeeded} / {u.failed} / {u.unknown ?? 0}
                  </td>
                  <td>
                    {u.input_tokens} / {u.output_tokens}
                  </td>
                  <td>{u.usage_missing}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function featureName(f: string) {
  return (
    (
      {
        config_test: "配置验证",
        paper: "论文解读",
        digest: "邮件导读",
        subscription_agent: "订阅助手",
        paper_qa: "论文问答",
      } as Record<string, string>
    )[f] ?? f
  );
}
export function CallHistory() {
  const { token } = useSession();
  const [feature, setFeature] = useState("");
  const [status, setStatus] = useState("");
  const [page, setPage] = useState(1);
  const calls = useResource(
    useCallback(
      (signal: AbortSignal) =>
        request<{ items: AICall[]; total: number }>(
          `/ai/calls?page=${page}&page_size=10${feature ? `&feature=${feature}` : ""}${status ? `&status=${status}` : ""}`,
          { token, signal },
        ),
      [token, page, feature, status],
    ),
  );
  return (
    <Card className="card-flush">
      <header className="card-header">
        <h2>最近调用与诊断</h2>
        <button className="button" onClick={calls.reload}>
          重新查询
        </button>
      </header>
      <div className="react-actions">
        <label>
          调用功能
          <select
            value={feature}
            onChange={(e) => {
              setFeature(e.target.value);
              setPage(1);
            }}
          >
            <option value="">全部功能</option>
            {[
              "config_test",
              "paper",
              "digest",
              "subscription_agent",
              "paper_qa",
            ].map((f) => (
              <option key={f} value={f}>
                {featureName(f)}
              </option>
            ))}
          </select>
        </label>
        <label>
          调用结果
          <select
            value={status}
            onChange={(e) => {
              setStatus(e.target.value);
              setPage(1);
            }}
          >
            <option value="">全部结果</option>
            <option value="succeeded">成功</option>
            <option value="failed">失败</option>
            <option value="unknown">结果未知</option>
            <option value="started">调用中</option>
          </select>
        </label>
      </div>
      {calls.loading ? (
        <Loading />
      ) : calls.error ? (
        <ErrorNotice error={calls.error} retry={calls.reload} />
      ) : (
        <>
          <div className="usage-table">
            <table>
              <thead>
                <tr>
                  <th>时间 / 功能</th>
                  <th>模型</th>
                  <th>结果 / 原因</th>
                  <th>耗时 / 诊断编号</th>
                </tr>
              </thead>
              <tbody>
                {!calls.data?.items.length && (
                  <tr>
                    <td colSpan={4}>
                      <EmptyState title="暂无详细调用记录">
                        仅保留最近 30 天记录，历史汇总不受影响。
                      </EmptyState>
                    </td>
                  </tr>
                )}
                {calls.data?.items.map((c) => (
                  <tr key={c.id}>
                    <td>
                      {new Date(c.created_at).toLocaleString()} /{" "}
                      {featureName(c.feature)}
                    </td>
                    <td>
                      {c.provider} / {c.model}
                    </td>
                    <td>
                      <Badge
                        tone={
                          c.status === "succeeded"
                            ? "success"
                            : c.status === "failed"
                              ? "danger"
                              : "warning"
                        }
                      >
                        {
                          (
                            {
                              succeeded: "成功",
                              failed: "失败",
                              unknown: "结果未知",
                              started: "调用中",
                            } as Record<string, string>
                          )[c.status]
                        }
                      </Badge>
                      {c.failure_code && (
                        <p>{aiFailureMessage(c.failure_code)}</p>
                      )}
                    </td>
                    <td>
                      {(c.duration_ms / 1000).toFixed(1)} 秒<br />
                      {c.id}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="react-actions">
            <button
              className="button"
              disabled={page === 1}
              onClick={() => setPage((p) => p - 1)}
            >
              上一页
            </button>
            <span>第 {page} 页</span>
            <button
              className="button"
              disabled={page * 10 >= (calls.data?.total ?? 0)}
              onClick={() => setPage((p) => p + 1)}
            >
              下一页
            </button>
          </div>
        </>
      )}
    </Card>
  );
}
