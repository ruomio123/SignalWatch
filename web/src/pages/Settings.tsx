import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { useAccount } from "../lib/account";
import { useSession } from "../lib/session";
import { useRequestScope } from "../lib/useRequestScope";
import { request } from "../lib/api";
import type { Profile } from "../lib/types";
import {
  ErrorNotice,
  Loading,
  PageTitle,
  SuccessNotice,
} from "../components/Common";
export function Settings() {
  const profile = useAccount();
  const [message, setMessage] = useState("");
  return (
    <>
      <PageTitle
        title="偏好设置"
        description="安排邮件发送时间，设置你的日常阅读偏好。"
      />
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
        <p className="settings-description">
          需要连接模型服务？前往 <Link to="/api-keys">API 管理</Link> 添加或管理
          API Key。
        </p>
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
        <label className="react-check preference-toggle">
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
