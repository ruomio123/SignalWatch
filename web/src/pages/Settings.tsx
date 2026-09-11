import { useCallback, useState, type FormEvent } from "react";
import { useCountdown } from "../lib/useCountdown";
import { useAccount } from "../lib/account";
import { useRequestScope } from "../lib/useRequestScope";
import { request, configETag, ApiError, aiFailureMessage } from "../lib/api";
import { useSession } from "../lib/session";
import { useResource } from "../lib/useResource";
import type {
  Configuration,
  Provider,
  Profile,
  Usage,
  FeatureUsage,
  AICall,
} from "../lib/types";
import {
  ErrorNotice,
  Loading,
  PageTitle,
  Badge,
  Card,
  EmptyState,
  SuccessNotice,
} from "../components/Common";
export function Settings() {
  const profile = useAccount();
  const [message, setMessage] = useState("");
  return (
    <>
      <PageTitle title="偏好设置" description="配置每日阅读节奏与 AI 服务。" />
      <div className="settings-stack">
        {message && <SuccessNotice>{message}</SuccessNotice>}
        {profile.loading ? (
          <Loading />
        ) : profile.error ? (
          <ErrorNotice error={profile.error} retry={profile.reload} />
        ) : (
          profile.data && (
            <Preferences
              key={JSON.stringify(profile.data)}
              profile={profile.data}
              onStart={() => setMessage("")}
              saved={() => {
                setMessage("阅读偏好已保存。");
                profile.reload();
              }}
            />
          )
        )}
        <AISettings onChanged={profile.reload} />
      </div>
    </>
  );
}
function Preferences({
  profile,
  saved,
  onStart,
}: {
  profile: Profile;
  saved: () => void;
  onStart: () => void;
}) {
  const { token } = useSession();
  const signal = useRequestScope();
  const [value, setValue] = useState(profile),
    [error, setError] = useState<unknown>(),
    [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    onStart();
    setBusy(true);
    setError(undefined);
    try {
      await request("/me", {
        token,
        signal: signal(),
        method: "PATCH",
        body: {
          timezone: value.timezone,
          digest_time: value.digest_time,
          max_items_per_digest: value.max_items_per_digest,
          ai_enabled: value.ai_enabled,
          ai_language: value.ai_language,
        },
      });
      if (!signal().aborted) saved();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="content-card react-form" onSubmit={submit}>
      <header>
        <h2>阅读偏好</h2>
        <p className="settings-description">
          安排每日阅读节奏，按你的时区发送邮件。
        </p>
      </header>
      <div className="settings-form form-stack">
        {!!error && <ErrorNotice error={error} />}
        <div className="form-grid">
          <label>
            时区
            <input
              required
              value={value.timezone}
              onChange={(e) =>
                setValue((v) => ({ ...v, timezone: e.target.value }))
              }
              placeholder="Asia/Shanghai"
            />
          </label>
          <label>
            每日邮件时间
            <input
              required
              type="time"
              value={value.digest_time}
              onChange={(e) =>
                setValue((v) => ({ ...v, digest_time: e.target.value }))
              }
            />
          </label>
        </div>
        <label>
          新订阅默认论文上限
          <input
            required
            type="number"
            min={1}
            max={20}
            value={value.max_items_per_digest}
            onChange={(e) =>
              setValue((v) => ({
                ...v,
                max_items_per_digest: Number(e.target.value),
              }))
            }
          />
        </label>
        <label className="react-check">
          <input
            type="checkbox"
            checked={value.ai_enabled}
            onChange={(e) =>
              setValue((v) => ({ ...v, ai_enabled: e.target.checked }))
            }
          />
          启用论文 AI 解读
        </label>
        <label>
          默认解读语言
          <select
            value={value.ai_language}
            onChange={(e) =>
              setValue((v) => ({
                ...v,
                ai_language: e.target.value as "zh" | "en",
              }))
            }
          >
            <option value="zh">中文</option>
            <option value="en">English</option>
          </select>
        </label>
        <button className="button button-primary" disabled={busy}>
          {busy ? "正在保存…" : "保存偏好"}
        </button>
      </div>
    </form>
  );
}
function AISettings({ onChanged }: { onChanged: () => void }) {
  const [message, setMessage] = useState("");
  const [usageRevision, setUsageRevision] = useState(0);
  const { token } = useSession();
  const result = useResource(
    useCallback(
      async (signal: AbortSignal) => {
        const [configuration, providers] = await Promise.all([
          request<Configuration>("/ai/configuration", { token, signal }),
          request<{ items: Provider[] }>("/ai/providers", { token, signal }),
        ]);
        return { configuration, providers: providers.items };
      },
      [token],
    ),
  );
  return (
    <>
      <Card className="settings-section">
        <header>
          <h2>AI 供应商</h2>
          <p className="settings-description">
            连接自己的模型服务，用于论文解读与每日导读。
          </p>
        </header>
        {message && <SuccessNotice>{message}</SuccessNotice>}
        {result.loading ? (
          <Loading />
        ) : result.error ? (
          <ErrorNotice error={result.error} retry={result.reload} />
        ) : (
          result.data && (
            <ConfigurationForm
              key={JSON.stringify(result.data.configuration)}
              configuration={result.data.configuration}
              providers={result.data.providers}
              reload={result.reload}
              onStart={() => setMessage("")}
              onSettled={() => setUsageRevision((n) => n + 1)}
              saved={(message) => {
                setMessage(message);
                result.reload();
                onChanged();
              }}
            />
          )
        )}
      </Card>
      <UsageTable key={usageRevision} />
      <CallHistory key={"calls-" + usageRevision} />
    </>
  );
}
function ConfigurationForm({
  configuration: c,
  providers,
  reload,
  saved,
  onStart,
  onSettled,
}: {
  configuration: Configuration;
  providers: Provider[];
  reload: () => void;
  saved: (message: string) => void;
  onStart: () => void;
  onSettled: () => void;
}) {
  const { token } = useSession();
  const signal = useRequestScope();
  const [provider, setProvider] = useState(
      c.provider ?? providers[0]?.id ?? "",
    ),
    [model, setModel] = useState(c.model ?? providers[0]?.models[0]?.id ?? ""),
    [secret, setSecret] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(),
    [remove, setRemove] = useState(false);
  const [operation, setOperation] = useState("save");
  const [retryAt, setRetryAt] = useState<number>();
  const remaining = useCountdown(retryAt);
  const rejectedCurrent =
    operation === "test" &&
    error instanceof ApiError &&
    error.code === "AI_CONFIGURATION_INVALID";
  const dirty =
    !c.configured || provider !== c.provider || model !== c.model || !!secret;
  const needsKey = !c.configured || provider !== c.provider;
  async function perform(action: "save" | "test" | "delete") {
    if (busy) return;
    onStart();
    setOperation(action);
    setBusy(true);
    setError(undefined);
    const key = secret;
    setSecret("");
    const current = signal();
    try {
      await request("/ai/configuration" + (action === "test" ? "/test" : ""), {
        token,
        signal: current,
        etag: configETag(c),
        method:
          action === "save" ? "PUT" : action === "test" ? "POST" : "DELETE",
        body:
          action === "save"
            ? { provider, model, ...(key ? { api_key: key } : {}) }
            : undefined,
      });
      if (!current.aborted)
        saved(
          action === "delete"
            ? "AI 配置已删除，论文解读与订阅 AI 导读已关闭。"
            : action === "save"
              ? "AI 配置已验证并保存。"
              : "当前配置连接测试成功。",
        );
    } catch (e) {
      if (!current.aborted) {
        setError(e);
        if (e instanceof ApiError && e.retryAfterSeconds)
          setRetryAt(Date.now() + e.retryAfterSeconds * 1000);
      }
    } finally {
      if (!current.aborted) {
        setBusy(false);
        onSettled();
      }
    }
  }
  if (!providers.length)
    return (
      <EmptyState title="AI 服务暂未开放">
        供应商开放后，可在这里配置模型。
      </EmptyState>
    );
  return (
    <div className="react-form settings-form">
      {!!error && (
        <ErrorNotice
          error={error}
          retry={
            error instanceof ApiError &&
            error.code === "AI_CONFIGURATION_VERSION_CONFLICT"
              ? reload
              : undefined
          }
        />
      )}
      {error instanceof ApiError && (error.callId || error.requestId) && (
        <p className="field-hint">
          诊断编号：{error.callId || error.requestId}
        </p>
      )}
      {remaining > 0 && <p role="status">请等待 {remaining} 秒后手动重试。</p>}
      <h3>当前生效配置</h3>
      <div role="status" className="configuration-status">
        {c.configured ? (
          <>
            <Badge tone={c.usable && !rejectedCurrent ? "success" : "warning"}>
              {c.usable && !rejectedCurrent ? "可用" : "需要检查"}
            </Badge>
            <span>
              {c.provider} / {c.model}
            </span>
            <span className="muted">{c.masked_key}</span>
            {!c.usable && c.unusable_reason && (
              <span className="muted">{c.unusable_reason}</span>
            )}
          </>
        ) : (
          <Badge>尚未配置供应商</Badge>
        )}
      </div>
      <h3>待保存修改</h3>
      <div className="form-grid">
        <label>
          供应商
          <select
            disabled={busy}
            value={provider}
            onChange={(e) => {
              setProvider(e.target.value);
              setModel(
                providers.find((p) => p.id === e.target.value)?.models[0]?.id ??
                  "",
              );
            }}
          >
            {providers.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name || p.id}
              </option>
            ))}
          </select>
        </label>
        <label>
          模型
          <select
            disabled={busy}
            value={model}
            onChange={(e) => setModel(e.target.value)}
          >
            {providers
              .find((p) => p.id === provider)
              ?.models.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name || m.id}
                </option>
              ))}
          </select>
        </label>
      </div>
      <label>
        API Key
        <input
          type="password"
          autoComplete="new-password"
          disabled={busy}
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
        />
      </label>
      <p className="field-hint">
        同一供应商下密钥留空可使用已保存密钥，更换供应商需输入新密钥。验证会调用供应商；提交后清空密钥输入。
      </p>
      {busy && (
        <p role="status" className="muted">
          {operation === "delete"
            ? "正在删除配置…"
            : `正在验证 ${model}，模型响应最多等待 30 秒…`}
        </p>
      )}
      <div className="react-actions">
        <button
          className="button button-primary"
          disabled={busy || remaining > 0 || !dirty || (needsKey && !secret)}
          onClick={() => void perform("save")}
        >
          验证并保存
        </button>
        {c.configured && !dirty && (
          <button
            className="button"
            disabled={busy || remaining > 0}
            onClick={() => void perform("test")}
          >
            测试当前配置
          </button>
        )}
        {c.configured && (
          <button
            className="button button-danger"
            disabled={busy}
            onClick={() => setRemove(true)}
          >
            删除 AI 配置
          </button>
        )}
      </div>
      {remove && (
        <div role="alert" className="confirmation">
          <p>删除配置会同时关闭论文解读与订阅 AI 导读。确认删除？</p>
          <button
            className="button button-danger"
            disabled={busy}
            onClick={() => void perform("delete")}
          >
            确认删除配置
          </button>
          <button className="button" onClick={() => setRemove(false)}>
            取消
          </button>
        </div>
      )}
    </div>
  );
}
function UsageTable() {
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
      } as Record<string, string>
    )[f] ?? f
  );
}
function CallHistory() {
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
            {["config_test", "paper", "digest"].map((f) => (
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
