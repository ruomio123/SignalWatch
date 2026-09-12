import { useCallback, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { KeyRound, Plus, Pencil, Trash2, ShieldCheck } from "lucide-react";
import { useAccount } from "../lib/account";
import { useSession } from "../lib/session";
import { useResource } from "../lib/useResource";
import { useRequestScope } from "../lib/useRequestScope";
import { request, configETag, ApiError } from "../lib/api";
import { useCountdown } from "../lib/useCountdown";
import type { Configuration, Provider } from "../lib/types";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorNotice,
  Loading,
  Modal,
  PageTitle,
  SuccessNotice,
} from "../components/Common";
import { CallHistory, UsageTable } from "../components/AIUsage";

const credentialID = (c: Configuration) => c.id ?? c.generation!;
const credentialName = (c: Configuration) => c.name || `${c.provider} API`;
const date = (v?: string) => (v ? new Date(v).toLocaleString() : "尚未使用");

export function APIKeys() {
  const { token } = useSession();
  const account = useAccount();
  const scope = useRequestScope();
  const [params, setParams] = useSearchParams();
  const activity = params.get("tab") === "activity";
  const [editing, setEditing] = useState<Configuration | "new">();
  const [removing, setRemoving] = useState<Configuration>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const [notice, setNotice] = useState("");
  const data = useResource(
    useCallback(
      async (signal: AbortSignal) => {
        const [credentials, providers] = await Promise.all([
          request<{ items: Configuration[] }>("/ai/credentials", {
            token,
            signal,
          }),
          request<{ items: Provider[] }>("/ai/providers", { token, signal }),
        ]);
        return { credentials: credentials.items, providers: providers.items };
      },
      [token],
    ),
  );
  function changed(message: string) {
    setNotice(message);
    setError(undefined);
    data.reload();
    account.reload();
  }
  async function action(
    c: Configuration,
    action: "test" | "default" | "delete",
  ) {
    setBusy(true);
    setError(undefined);
    setNotice("");
    try {
      await request(
        action === "default"
          ? "/ai/default-selection"
          : `/ai/credentials/${credentialID(c)}${action === "test" ? "/test" : ""}`,
        {
          token,
          signal: scope(),
          method:
            action === "delete"
              ? "DELETE"
              : action === "default"
                ? "PUT"
                : "POST",
          etag: configETag(c),
          ...(action === "default"
            ? {
                body: {
                  provider: c.provider,
                  model: c.model,
                  generation: c.generation,
                  version: c.version,
                },
              }
            : {}),
        },
      );
      if (scope().aborted) return;
      setRemoving(undefined);
      changed(
        action === "test"
          ? "连接验证成功。"
          : action === "default"
            ? "已更新邮件与摘要使用的默认 API。"
            : "API Key 已删除。",
      );
    } catch (e) {
      if (!scope().aborted) {
        setError(e);
        data.reload();
      }
    } finally {
      if (!scope().aborted) setBusy(false);
    }
  }
  const back = params.get("return_to");
  const safeBack =
    back?.startsWith("/") && !back.startsWith("//") && !back.includes("\\")
      ? back
      : undefined;
  const currentDefault = data.data?.credentials.find((c) => c.is_default);
  return (
    <>
      <PageTitle
        title="API 管理"
        description="连接你的模型服务，为邮件导读和研究助手选择合适的 API。"
      />
      {safeBack && (
        <Link className="button api-return" to={safeBack}>
          返回原对话
        </Link>
      )}
      <div className="api-tabs" role="tablist" aria-label="API 管理视图">
        {[false, true].map((value) => (
          <button
            key={String(value)}
            role="tab"
            aria-selected={activity === value}
            onClick={() => {
              setParams((p) => {
                const next = new URLSearchParams(p);
                if (value) next.set("tab", "activity");
                else next.delete("tab");
                return next;
              });
            }}
          >
            {value ? "用量与调用记录" : "API Key"}
          </button>
        ))}
      </div>
      {activity ? (
        <div className="api-activity">
          <UsageTable />
          <CallHistory />
        </div>
      ) : (
        <Card className="api-card card-flush">
          <header className="card-header api-card-header">
            <div>
              <h2>
                API Key{" "}
                <span className="api-count">
                  {data.data?.credentials.length ?? 0}
                </span>
              </h2>
              <p className="settings-description">
                每条 Key 独立保存，同一家供应商也可以添加多条。
              </p>
            </div>
            <Button
              variant="primary"
              disabled={busy || !data.data?.providers.length}
              onClick={() => {
                setError(undefined);
                setEditing("new");
              }}
            >
              <Plus size={16} />
              新建 API Key
            </Button>
          </header>
          <div className="api-summary">
            <ShieldCheck size={18} />
            <p>
              密钥加密保存，列表仅显示尾号。
              <br />
              <span>
                {currentDefault
                  ? `邮件与摘要默认使用：${credentialName(currentDefault)}`
                  : "尚未设置默认 API，可在列表中选择。"}{" "}
                对话可单独选择 API 和模型。
              </span>
            </p>
          </div>
          {notice && (
            <div className="api-feedback">
              <SuccessNotice>{notice}</SuccessNotice>
            </div>
          )}
          {!!error && !removing && (
            <div className="api-feedback">
              <ErrorNotice error={error} retry={data.reload} />
            </div>
          )}
          {data.loading ? (
            <Loading />
          ) : data.error ? (
            <ErrorNotice error={data.error} retry={data.reload} />
          ) : !data.data?.credentials.length ? (
            <EmptyState title="还没有 API Key">
              <p>点击右上角“新建 API Key”，添加你在模型供应商处获取的密钥。</p>
            </EmptyState>
          ) : (
            <div className="api-table-wrap">
              <table className="api-table">
                <thead>
                  <tr>
                    <th>名称</th>
                    <th>供应商 / 模型</th>
                    <th>API Key</th>
                    <th>创建时间</th>
                    <th>上次使用</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {data.data.credentials.map((c) => (
                    <tr key={credentialID(c)}>
                      <td>
                        <div className="api-entry-name">
                          <KeyRound size={17} />
                          <strong>{credentialName(c)}</strong>
                        </div>
                        <div className="api-badges">
                          <Badge tone={c.usable ? "success" : "warning"}>
                            {c.usable ? "可用" : "需检查"}
                          </Badge>
                          {c.is_default && <Badge tone="info">默认</Badge>}
                        </div>
                      </td>
                      <td>
                        <span>
                          {data.data!.providers.find((p) => p.id === c.provider)
                            ?.name ?? c.provider}
                        </span>
                        <small className="api-model">{c.model}</small>
                      </td>
                      <td>
                        <code className="api-masked-key">{c.masked_key}</code>
                      </td>
                      <td className="api-date">
                        {c.created_at ? date(c.created_at) : "—"}
                      </td>
                      <td className="api-date">{date(c.last_used_at)}</td>
                      <td>
                        <div className="api-row-actions">
                          <Button
                            variant="ghost"
                            disabled={busy}
                            onClick={() => {
                              setError(undefined);
                              setEditing(c);
                            }}
                          >
                            <Pencil size={14} />
                            编辑
                          </Button>
                          <Button
                            variant="ghost"
                            disabled={busy}
                            onClick={() => void action(c, "test")}
                          >
                            验证
                          </Button>
                          <Button
                            variant="ghost"
                            disabled={busy || !c.usable || c.is_default}
                            onClick={() => void action(c, "default")}
                          >
                            设为默认
                          </Button>
                          <Button
                            variant="ghost"
                            className="api-delete"
                            disabled={busy}
                            onClick={() => {
                              setError(undefined);
                              setRemoving(c);
                            }}
                          >
                            <Trash2 size={14} />
                            删除
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}
      {editing && (
        <CredentialEditor
          key={
            editing === "new"
              ? "new"
              : `${credentialID(editing)}:${editing.version}`
          }
          credential={editing === "new" ? undefined : editing}
          providers={data.data?.providers ?? []}
          close={() => setEditing(undefined)}
          reloaded={setEditing}
          saved={() => {
            setEditing(undefined);
            changed("API Key 已验证并保存。");
          }}
        />
      )}
      {removing && (
        <Modal
          title="删除 API Key"
          onClose={() => {
            if (!busy) setRemoving(undefined);
          }}
        >
          <div className="react-form">
            <p>
              确认删除“{credentialName(removing)}”（{removing.masked_key}）？
            </p>
            <p className="settings-description">
              {removing.is_default
                ? "此条是默认 API，删除后会关闭相关 AI 开关。普通邮件继续发送；可再将其他条目设为默认。"
                : "删除后，此条 API 将无法用于新的调用，其他 API Key 不受影响。"}
            </p>
            {!!error && <ErrorNotice error={error} />}
            <div className="form-footer">
              <Button disabled={busy} onClick={() => setRemoving(undefined)}>
                取消
              </Button>
              <Button
                variant="danger"
                disabled={busy}
                onClick={() => void action(removing, "delete")}
              >
                {busy ? "正在删除…" : "确认删除"}
              </Button>
            </div>
          </div>
        </Modal>
      )}
    </>
  );
}

function CredentialEditor({
  credential,
  providers,
  close,
  saved,
  reloaded,
}: {
  credential?: Configuration;
  providers: Provider[];
  close: () => void;
  saved: () => void;
  reloaded: (c: Configuration) => void;
}) {
  const { token } = useSession();
  const scope = useRequestScope();
  const [name, setName] = useState(credential?.name ?? "");
  const [provider, setProvider] = useState(
    credential?.provider ?? providers[0]?.id ?? "",
  );
  const [model, setModel] = useState(
    credential?.model ?? providers[0]?.models[0]?.id ?? "",
  );
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const [retryAt, setRetryAt] = useState<number>();
  const remaining = useCountdown(retryAt);
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy || remaining > 0) return;
    setBusy(true);
    setError(undefined);
    const secret = key;
    setKey("");
    try {
      await request(
        credential
          ? `/ai/credentials/${credentialID(credential)}`
          : "/ai/credentials",
        {
          token,
          signal: scope(),
          method: credential ? "PUT" : "POST",
          etag: credential ? configETag(credential) : undefined,
          body: {
            name: name.trim(),
            model,
            ...(credential ? {} : { provider }),
            ...(secret ? { api_key: secret } : {}),
          },
        },
      );
      if (!scope().aborted) saved();
    } catch (e) {
      if (!scope().aborted) {
        setError(e);
        if (e instanceof ApiError && e.retryAfterSeconds)
          setRetryAt(Date.now() + e.retryAfterSeconds * 1000);
      }
    } finally {
      if (!scope().aborted) setBusy(false);
    }
  }
  async function reload() {
    if (!credential) return;
    try {
      const c = await request<Configuration>(
        `/ai/credentials/${credentialID(credential)}`,
        { token, signal: scope() },
      );
      if (!scope().aborted) reloaded(c);
    } catch (e) {
      if (!scope().aborted) setError(e);
    }
  }
  return (
    <Modal
      title={credential ? "编辑 API Key" : "新建 API Key"}
      onClose={() => {
        if (!busy) close();
      }}
    >
      <form className="react-form api-key-form" onSubmit={submit}>
        <p className="settings-description">
          填写供应商提供的 API Key，验证连接后保存。
          {credential && "密钥留空即可保留原 Key。"}
        </p>
        <label>
          名称
          <input
            required
            maxLength={80}
            autoFocus
            value={name}
            disabled={busy}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如：GLM 日常研究"
          />
        </label>
        <div className="form-grid">
          <label>
            供应商
            <select
              value={provider}
              required
              disabled={busy || !!credential}
              onChange={(e) => {
                setProvider(e.target.value);
                setModel(
                  providers.find((p) => p.id === e.target.value)?.models[0]
                    ?.id ?? "",
                );
                setKey("");
              }}
            >
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            默认模型
            <select
              value={model}
              required
              disabled={busy}
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
        <label>
          API Key
          <input
            type="password"
            autoComplete="new-password"
            required={!credential}
            maxLength={1024}
            value={key}
            disabled={busy}
            onChange={(e) => setKey(e.target.value)}
            placeholder={
              credential
                ? `留空保留原密钥 ${credential.masked_key}`
                : "输入 API Key"
            }
          />
        </label>
        {!!error && <ErrorNotice error={error} />}
        {credential && error instanceof ApiError && error.status === 409 && (
          <Button onClick={() => void reload()}>重新载入条目</Button>
        )}
        <div className="form-footer">
          <Button disabled={busy} onClick={close}>
            取消
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={
              busy ||
              remaining > 0 ||
              !name.trim() ||
              !provider ||
              !model ||
              (!credential && !key.trim())
            }
          >
            {busy
              ? "正在验证…"
              : remaining > 0
                ? `${remaining} 秒后重试`
                : "验证并保存"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
