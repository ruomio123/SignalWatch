import { useEffect, useState } from "react";
import { Check, FileCheck2, MessageSquare } from "lucide-react";
import { request } from "../lib/api";
import { useSession } from "../lib/session";
import { useRequestScope } from "../lib/useRequestScope";
import { useAccount } from "../lib/account";
import type { SubscriptionInput } from "../lib/types";
import { Badge, ErrorNotice } from "./Common";

export type Draft = {
  id: string;
  version: number;
  payload: SubscriptionInput;
  expires_at: string;
  subscription_id?: number;
};

export function SubscriptionDraftCard({
  draft,
  latest,
  disabled,
  onConfirm,
  onAdjust,
  onSaved,
}: {
  draft: Draft;
  latest: boolean;
  disabled: boolean;
  onConfirm: (draft: Draft) => Promise<void>;
  onAdjust: () => void;
  onSaved: () => void;
}) {
  const account = useAccount();
  const [now, setNow] = useState(Date.now);
  const [editing, setEditing] = useState(false);
  const expires = new Date(draft.expires_at).getTime();
  useEffect(() => {
    const tick = () => setNow(Date.now());
    tick();
    // Drafts can expire while the conversation is idle.
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, [expires]);
  const valid = latest && expires > now;
  const value = draft.payload;
  return (
    <section
      className="subscription-draft"
      aria-label={`订阅草案：${value.name}`}
    >
      <header className="subscription-draft-header">
        <FileCheck2 size={22} aria-hidden="true" />
        <div>
          <h3>订阅草案</h3>
          <p>请检查以下配置，确认后才会正式创建。</p>
        </div>
        <span aria-label={`订阅状态：${value.enabled ? "启用" : "暂停"}`}>
          <Badge tone={value.enabled ? "success" : "neutral"}>
            {value.enabled ? "启用" : "暂停"}
          </Badge>
        </span>
      </header>
      <dl className="subscription-draft-fields">
        <div>
          <dt>订阅名称</dt>
          <dd>{value.name}</dd>
        </div>
        <div>
          <dt>论文分类</dt>
          <dd>{value.rules.category}</dd>
        </div>
        <div>
          <dt>关键词（OR）</dt>
          <dd>{value.rules.keywords.join(" · ") || "不限关键词"}</dd>
        </div>
        <div>
          <dt>每次篇数</dt>
          <dd>最多 {value.max_items_per_digest} 篇</dd>
        </div>
        <div>
          <dt>发送安排</dt>
          <dd>
            {account.error ? (
              "发送安排暂不可用"
            ) : account.data ? (
              <>
                每天 · {account.data.digest_time.slice(0, 5)}
                <small>{account.data.timezone}</small>
              </>
            ) : (
              "发送安排加载中"
            )}
            <small>跟随账户偏好</small>
          </dd>
        </div>
        <div>
          <dt>邮件内容</dt>
          <dd>
            {value.digest_ai_enabled
              ? value.digest_ai_language === "zh"
                ? "中文 AI 导读"
                : "英文 AI 导读"
              : "AI 导读关闭"}
          </dd>
        </div>
        {value.objective && (
          <div className="subscription-draft-objective">
            <dt>研究目标</dt>
            <dd>{value.objective}</dd>
          </div>
        )}
      </dl>
      <div className="subscription-draft-footer">
        <p className="subscription-draft-meta">
          草案版本 {draft.version} · 有效至{" "}
          {new Date(draft.expires_at).toLocaleString()}
        </p>
        {draft.subscription_id ? (
          <p role="status">已创建订阅 #{draft.subscription_id}</p>
        ) : (
          <>
            {!valid && (
              <p role="status">
                {latest
                  ? "此草案已过期，请重新生成。"
                  : "此草案已被新版替代，请查看最新草案。"}
              </p>
            )}
            <div className="subscription-draft-actions">
              <button className="button" disabled={disabled} onClick={onAdjust}>
                <MessageSquare size={16} aria-hidden="true" />
                继续调整
              </button>
              <button
                className="button button-primary"
                disabled={!valid || disabled || editing}
                onClick={() => void onConfirm(draft)}
              >
                <Check size={16} aria-hidden="true" />
                确认创建订阅
              </button>
            </div>
            {valid && (
              <DraftEditor
                draft={draft}
                disabled={disabled}
                saved={onSaved}
                editing={editing}
                setEditing={setEditing}
              />
            )}
          </>
        )}
      </div>
    </section>
  );
}

function DraftEditor({
  draft,
  disabled,
  saved,
  editing,
  setEditing,
}: {
  draft: Draft;
  disabled: boolean;
  saved: () => void;
  editing: boolean;
  setEditing: (value: boolean) => void;
}) {
  const { token } = useSession();
  const scope = useRequestScope();
  const [value, setValue] = useState(draft.payload);
  const [keywords, setKeywords] = useState(
    draft.payload.rules.keywords.join(", "),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  if (!editing)
    return (
      <button
        className="button"
        disabled={disabled}
        onClick={() => {
          setValue(draft.payload);
          setKeywords(draft.payload.rules.keywords.join(", "));
          setEditing(true);
        }}
      >
        编辑草案
      </button>
    );
  return (
    <form
      className="react-form"
      onSubmit={async (e) => {
        e.preventDefault();
        if (busy || disabled) return;
        setBusy(true);
        setError(undefined);
        try {
          await request(`/agent/subscription-drafts/${draft.id}`, {
            token,
            signal: scope(),
            method: "PATCH",
            body: {
              version: draft.version,
              payload: {
                ...value,
                rules: {
                  ...value.rules,
                  keywords: keywords
                    .split(/[,，]/)
                    .map((s) => s.trim())
                    .filter(Boolean),
                },
              },
            },
          });
          setEditing(false);
          saved();
        } catch (e) {
          setError(e);
        } finally {
          setBusy(false);
        }
      }}
    >
      {!!error && <ErrorNotice error={error} />}
      <fieldset
        disabled={busy || disabled}
        className="subscription-draft-editor"
      >
        <label>
          草案名称
          <input
            required
            maxLength={100}
            value={value.name}
            onChange={(e) => setValue({ ...value, name: e.target.value })}
          />
        </label>
        <label>
          草案分类
          <input
            required
            value={value.rules.category}
            onChange={(e) =>
              setValue({
                ...value,
                rules: { ...value.rules, category: e.target.value },
              })
            }
          />
        </label>
        <label>
          草案关键词（逗号分隔，OR）
          <input
            value={keywords}
            onChange={(e) => setKeywords(e.target.value)}
          />
        </label>
        <label>
          每封篇数
          <input
            type="number"
            required
            min={1}
            max={20}
            value={value.max_items_per_digest}
            onChange={(e) =>
              setValue({
                ...value,
                max_items_per_digest: Number(e.target.value),
              })
            }
          />
        </label>
        <label>
          <input
            type="checkbox"
            checked={value.digest_ai_enabled}
            onChange={(e) =>
              setValue({ ...value, digest_ai_enabled: e.target.checked })
            }
          />
          启用草案 AI 导读
        </label>
        <label>
          导读语言
          <select
            value={value.digest_ai_language}
            onChange={(e) =>
              setValue({
                ...value,
                digest_ai_language: e.target.value as "zh" | "en",
              })
            }
          >
            <option value="zh">中文</option>
            <option value="en">英文</option>
          </select>
        </label>
        <button className="button" disabled={busy || disabled}>
          保存草案修改
        </button>
        <button
          type="button"
          className="button"
          onClick={() => setEditing(false)}
        >
          取消编辑
        </button>
      </fieldset>
    </form>
  );
}
