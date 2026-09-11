import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { useRequestScope } from "../lib/useRequestScope";
import { request } from "../lib/api";
import { useSession } from "../lib/session";
import { ArrowRight, Eye, EyeOff, FileText, Mail, Rss } from "lucide-react";
import {
  Badge,
  Brand,
  Button,
  Card,
  Field,
  IconButton,
  ErrorNotice,
} from "../components/Common";
export function Auth({ register = false }: { register?: boolean }) {
  const signal = useRequestScope();
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const [visible, setVisible] = useState(false);
  const { login } = useSession();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const data = new FormData(e.currentTarget);
    const email = String(data.get("email"));
    const password = String(data.get("password"));
    setError(undefined);
    if (
      register &&
      (password !== data.get("confirmation") ||
        [...password].length < 8 ||
        !/\p{L}/u.test(password) ||
        !/\p{N}/u.test(password) ||
        new TextEncoder().encode(password).length > 72)
    ) {
      setError(
        new Error(
          "密码至少 8 个字符且包含字母和数字，两次输入须一致，最多 72 字节。",
        ),
      );
      return;
    }
    setBusy(true);
    try {
      if (register)
        await request("/auth/register", {
          method: "POST",
          signal: signal(),
          body: { email, password },
        });
      const value = await request<{ access_token: string }>("/auth/login", {
        method: "POST",
        signal: signal(),
        body: { email, password },
      });
      login(value.access_token);
      const next = params.get("next") ?? "/app";
      navigate(
        /^\/(app|papers|subscriptions|settings)(\?|$)/.test(next)
          ? next
          : "/app",
        { replace: true },
      );
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  const nextQuery = params.get("next")
    ? "?next=" + encodeURIComponent(params.get("next")!)
    : "";
  return (
    <main className="auth-page">
      <section className="auth-card">
        <Link className="brand" to="/">
          <Brand />
        </Link>
        <header className="auth-heading">
          <h1>{register ? "创建账户" : "欢迎回来"}</h1>
          <p>
            {register
              ? "开启属于你的研究订阅。"
              : "登录后，继续关注你的研究方向。"}
          </p>
        </header>
        {error !== undefined && <ErrorNotice error={error} />}
        <form onSubmit={submit}>
          <Field label="邮箱" htmlFor="auth-email">
            <input
              id="auth-email"
              name="email"
              type="email"
              autoComplete="email"
              required
              placeholder="you@example.com"
            />
          </Field>
          <Field label="密码" htmlFor="auth-password">
            <span className="password-input">
              <input
                id="auth-password"
                name="password"
                type={visible ? "text" : "password"}
                autoComplete={register ? "new-password" : "current-password"}
                required
              />
              <IconButton
                label={visible ? "隐藏密码" : "显示密码"}
                aria-pressed={visible}
                onClick={() => setVisible(!visible)}
              >
                {visible ? <EyeOff size={17} /> : <Eye size={17} />}
              </IconButton>
            </span>
          </Field>
          {register && (
            <>
              <p className="field-hint">
                至少 8 个字符，包含字母和数字，最多 72 字节。
              </p>
              <Field label="确认密码" htmlFor="auth-confirmation">
                <input
                  id="auth-confirmation"
                  name="confirmation"
                  type="password"
                  autoComplete="new-password"
                  required
                />
              </Field>
            </>
          )}
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? "正在提交…" : register ? "注册并登录" : "登录"}
            <ArrowRight size={16} aria-hidden="true" />
          </Button>
        </form>
        <p className="auth-footer">
          {register ? "已有账户？" : "还没有账户？"}{" "}
          <Link to={(register ? "/login" : "/register") + nextQuery}>
            {register ? "登录" : "创建新账户"}
          </Link>
        </p>
      </section>
    </main>
  );
}
export function Landing() {
  return (
    <div className="landing">
      <header className="site-nav">
        <nav className="site-nav-inner" aria-label="网站导航">
          <Link to="/" className="brand">
            <Brand />
          </Link>
          <div className="actions">
            <Link to="/login">登录</Link>
            <Link to="/register" className="button button-primary">
              开始使用
            </Link>
          </div>
        </nav>
      </header>
      <main>
        <section className="hero">
          <Badge tone="info">
            <Rss size={13} aria-hidden="true" />
            为研究留出专注的时间
          </Badge>
          <h1>
            关注研究方向，
            <br />
            <span>发现值得阅读的论文。</span>
          </h1>
          <p>
            用分类和关键词追踪
            arXiv，在你选择的时间收到每日邮件。让新的研究进展，自然融入你的阅读节奏。
          </p>
          <div className="actions">
            <Link to="/register" className="button button-primary">
              建立研究订阅
              <ArrowRight size={16} />
            </Link>
            <Link to="/login" className="button">
              登录工作台
            </Link>
          </div>
        </section>
        <section className="landing-features" aria-label="产品能力">
          {[
            {
              title: "订阅研究方向",
              text: "选择分类，添加关键词，持续关注你关心的问题。新订阅会回填最近七天的本地论文。",
              icon: Rss,
            },
            {
              title: "发现匹配论文",
              text: "集中阅读与你的订阅匹配的论文，查看命中原因，也可连接自己的 AI 服务获取解读。",
              icon: FileText,
            },
            {
              title: "按时收到每日邮件",
              text: "按你的时区和阅读时间汇总论文。每个订阅可独立设置篇数上限与 AI 导读。",
              icon: Mail,
            },
          ].map(({ title, text, icon: Icon }) => (
            <Card key={title}>
              <div className="feature-icon">
                <Icon size={20} aria-hidden="true" />
              </div>
              <h2>{title}</h2>
              <p>{text}</p>
            </Card>
          ))}
        </section>
      </main>
      <footer className="site-footer">
        SignalWatch · 让研究进展，有序抵达。
      </footer>
    </div>
  );
}
