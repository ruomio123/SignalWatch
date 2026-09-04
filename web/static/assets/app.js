const API_ROOT = "/api/v1";
const TOKEN_KEY = "signalwatch.access_token";

const app = document.querySelector("#app");
const state = {
  token: readToken(),
  profile: null,
  health: null,
  readiness: null,
};

class ApiError extends Error {
  constructor(status, payload = {}) {
    super(payload.message || "请求未能完成");
    this.status = status;
    this.code = payload.code || "UNKNOWN_ERROR";
    this.requestId = payload.request_id || "";
  }
}

function readToken() {
  try {
    return window.localStorage.getItem(TOKEN_KEY) || "";
  } catch {
    return "";
  }
}

function storeToken(token) {
  state.token = token;
  try {
    window.localStorage.setItem(TOKEN_KEY, token);
  } catch {
    // The in-memory token still keeps the current tab usable.
  }
}

function clearSession() {
  state.token = "";
  state.profile = null;
  try {
    window.localStorage.removeItem(TOKEN_KEY);
  } catch {
    // Ignore storage restrictions when signing out.
  }
}

async function apiRequest(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.body) headers.set("Content-Type", "application/json");
  if (options.auth && state.token) headers.set("Authorization", `Bearer ${state.token}`);

  let response;
  try {
    const isRootEndpoint = path === "/healthz" || path === "/readyz";
    const requestPath = isRootEndpoint || path.startsWith("/api/")
      ? path
      : `${API_ROOT}${path.startsWith("/") ? path : `/${path}`}`;
    response = await fetch(requestPath, {
      ...options,
      headers,
    });
  } catch {
    throw new ApiError(0, { message: "无法连接 SignalWatch 服务，请确认 API 已启动。" });
  }

  const text = await response.text();
  let payload = {};
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = { message: "服务返回了无法识别的响应。" };
    }
  }

  if (!response.ok) {
    if (response.status === 401 && options.auth) {
      clearSession();
      navigate("/login?expired=1");
    }
    throw new ApiError(response.status, payload);
  }
  return payload;
}

function icon(name) {
  const paths = {
    pulse: '<path d="M3 12h4l2.2-7 4.1 14 2.4-8 1.8 3H21"/>',
    arrowRight: '<path d="M5 12h14M13 6l6 6-6 6"/>',
    check: '<path d="m5 12 4 4L19 6"/>',
    shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/><path d="m9 12 2 2 4-4"/>',
    clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
    filter: '<path d="M4 5h16M7 12h10M10 19h4"/>',
    mail: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/>',
    eye: '<path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6S2 12 2 12Z"/><circle cx="12" cy="12" r="2.5"/>',
    eyeOff: '<path d="m3 3 18 18M10.6 6.2A10 10 0 0 1 12 6c6.5 0 10 6 10 6a16 16 0 0 1-2.1 2.8M6.5 6.5C3.6 8.3 2 12 2 12s3.5 6 10 6a9.8 9.8 0 0 0 4.1-.9M9.9 9.9a3 3 0 0 0 4.2 4.2"/>',
    info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/>',
    layout: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M9 21V9"/>',
    settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1a1.7 1.7 0 0 0 1.9.3A1.7 1.7 0 0 0 10 3v-.2h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z"/>',
    layers: '<path d="m12 2 9 5-9 5-9-5 9-5Z"/><path d="m3 12 9 5 9-5M3 17l9 5 9-5"/>',
    logOut: '<path d="M10 17l5-5-5-5M15 12H3M15 4h4a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-4"/>',
    calendar: '<rect x="3" y="5" width="18" height="16" rx="2"/><path d="M16 3v4M8 3v4M3 10h18"/>',
    activity: '<path d="M4 13h4l2-6 4 11 2-5h4"/>',
    inbox: '<path d="M4 4h16l2 10v5a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-5L4 4Z"/><path d="M2 14h5l2 3h6l2-3h5"/>',
    user: '<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>',
    bell: '<path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4"/>',
    save: '<path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2Z"/><path d="M17 21v-8H7v8M7 3v5h8"/>',
    quote: '<path d="M9 11H4a1 1 0 0 0-1 1v5a1 1 0 0 0 1 1h4a1 1 0 0 0 1-1v-7a6 6 0 0 0-6-6M21 11h-5a1 1 0 0 0-1 1v5a1 1 0 0 0 1 1h4a1 1 0 0 0 1-1v-7a6 6 0 0 0-6-6"/>',
    chevronLeft: '<path d="m15 18-6-6 6-6"/>',
    x: '<path d="M18 6 6 18M6 6l12 12"/>',
  };
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths[name] || paths.activity}</svg>`;
}

function brand(dark = false) {
  return `<a class="brand${dark ? " brand-on-dark" : ""}" href="/" data-route aria-label="SignalWatch 首页">
    <span class="brand-mark">${icon("pulse")}</span><span>SignalWatch</span>
  </a>`;
}

function escapeHTML(value) {
  return String(value == null ? "" : value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function navigate(target, replace = false) {
  if (replace) window.history.replaceState({}, "", target);
  else window.history.pushState({}, "", target);
  window.scrollTo({ top: 0, behavior: "instant" });
  route();
}

function landingPage() {
  const primaryTarget = state.token ? "/app" : "/register";
  const primaryLabel = state.token ? "进入工作台" : "开始";
  const tickerItems = ["arXiv · cs.AI", "自然语言处理", "信息检索", "机器学习", "软件工程", "每日摘要"];
  const ticker = [...tickerItems, ...tickerItems]
    .map((item) => `<span class="ticker-item">${item}</span>`)
    .join("");

  return `<div class="landing">
    <a class="skip-link" href="#main">跳到主要内容</a>
    <nav class="site-nav" aria-label="主导航">
      ${brand()}
      <div class="nav-links">
        <a class="nav-link" href="#features">工作方式</a>
        <a class="nav-link" href="#roadmap">产品能力</a>
      </div>
      <div class="nav-actions">
        ${state.token ? "" : '<a class="nav-link" href="/login" data-route>登录</a>'}
        <a class="button button-primary button-small" href="${primaryTarget}" data-route>${primaryLabel} ${icon("arrowRight")}</a>
      </div>
    </nav>

    <main id="main">
      <section class="hero">
        <div class="hero-copy">
          <p class="eyebrow">为持续探索的人而做</p>
          <h1>把信息噪声，<br>调成你的<em>研究信号</em></h1>
          <p class="hero-description">SignalWatch 持续监听学术信息源，按你的主题、作者与关键词筛出真正值得读的内容，在合适的时间送到你面前。</p>
          <div class="hero-actions">
            <a class="button button-primary" href="${primaryTarget}" data-route>${primaryLabel} ${icon("arrowRight")}</a>
            <a class="button button-ghost" href="#features">看看如何工作</a>
          </div>
          <div class="trust-line">
            <span>${icon("check")} 精细化订阅规则</span>
            <span>${icon("check")} 按时区定时送达</span>
            <span>${icon("check")} 每日数量可控</span>
          </div>
        </div>

        <div class="hero-visual" aria-label="论文信号预览">
          <span class="orb orb-one"></span><span class="orb orb-two"></span>
          <div class="preview-card">
            <div class="preview-topbar"><div class="preview-dots"><i></i><i></i><i></i></div><span class="preview-live">SIGNAL LIVE</span></div>
            <div class="preview-content">
              <div class="preview-heading"><div><span>今日信号</span><strong>3 篇值得关注</strong></div><span>SEP 04</span></div>
              <article class="paper-preview">
                <div class="paper-meta"><span class="paper-tag">cs.AI</span><span>12 分钟前</span></div>
                <h3>Adaptive Test-Time Learning for Reliable Models</h3>
                <p>命中：test-time adaptation · calibration</p>
              </article>
              <article class="paper-preview">
                <div class="paper-meta"><span class="paper-tag">cs.CL</span><span>38 分钟前</span></div>
                <h3>When Language Models Know What They Know</h3>
                <p>命中：uncertainty · language models</p>
              </article>
            </div>
          </div>
          <div class="signal-score"><span>SIGNAL SCORE</span><strong>94</strong>/100<small>高度匹配你的研究兴趣</small></div>
        </div>
      </section>

      <div class="ticker" aria-hidden="true"><div class="ticker-track">${ticker}</div></div>

      <section class="section" id="features">
        <div class="section-heading">
          <div><p class="eyebrow">少一点翻找，多一点发现</p><h2>三步建立你的研究雷达</h2></div>
          <p>把重复的信息筛选交给系统，把有限的注意力留给真正重要的问题。</p>
        </div>
        <div class="feature-grid">
          <article class="feature-card"><span class="feature-number">01 / 设定范围</span><span class="feature-icon">${icon("filter")}</span><h3>定义你的信号</h3><p>组合分类、作者、包含和排除关键词，让订阅规则贴近你的研究边界。</p></article>
          <article class="feature-card"><span class="feature-number">02 / 持续监听</span><span class="feature-icon">${icon("pulse")}</span><h3>自动追踪来源</h3><p>系统按计划检查 arXiv 等来源，把新增内容与每一条订阅规则进行匹配。</p></article>
          <article class="feature-card"><span class="feature-number">03 / 按时送达</span><span class="feature-icon">${icon("clock")}</span><h3>收到克制的摘要</h3><p>选择时区、送达时间和数量上限，获得不过载、可执行的每日阅读清单。</p></article>
        </div>
      </section>

      <section class="cta-wrap" id="roadmap">
        <div class="cta-panel">
          <div class="cta-copy"><p class="eyebrow">你的注意力值得被保护</p><h2>从今天开始，<br>只追踪重要的信号。</h2><p>账户与个性化偏好已可用，订阅匹配能力正在接入。</p></div>
          <a class="button button-lime" href="${primaryTarget}" data-route>${primaryLabel} ${icon("arrowRight")}</a>
        </div>
      </section>
    </main>
    <footer class="site-footer">${brand()}<span>© ${new Date().getFullYear()} SignalWatch · Built for focused research.</span></footer>
  </div>`;
}

function authPage(mode) {
  const isLogin = mode === "login";
  const expired = new URLSearchParams(window.location.search).get("expired") === "1";
  return `<div class="auth-shell">
    <section class="auth-panel">
      ${brand()}
      <main class="auth-main">
        <div class="auth-card">
          <p class="eyebrow">${isLogin ? "欢迎回来" : "建立你的研究雷达"}</p>
          <h1>${isLogin ? "继续捕捉信号" : "创建 SignalWatch"}</h1>
          <p class="auth-intro">${expired ? "登录状态已失效，请重新登录。" : isLogin ? "登录后查看服务状态并管理你的摘要偏好。" : "只需一个账户，即可保存你的追踪和送达偏好。"}</p>
          <form id="auth-form" novalidate data-mode="${mode}">
            <div class="form-field">
              <label for="email">邮箱地址</label>
              <div class="input-wrap">${icon("mail")}<input class="form-input" id="email" name="email" type="email" autocomplete="email" placeholder="you@example.com" required></div>
              <p class="field-error" id="email-error" aria-live="polite"></p>
            </div>
            <div class="form-field">
              <div class="label-row"><label for="password">密码</label>${isLogin ? "" : "<span>至少 8 个字符</span>"}</div>
              <div class="input-wrap">${icon("shield")}<input class="form-input" id="password" name="password" type="password" autocomplete="${isLogin ? "current-password" : "new-password"}" placeholder="输入你的密码" required><button class="password-toggle" type="button" aria-label="显示密码" data-password-toggle>${icon("eye")}</button></div>
              <p class="field-error" id="password-error" aria-live="polite"></p>
            </div>
            ${isLogin ? "" : `<p class="form-note">${icon("info")}密码按原样保存为安全哈希，不会自动移除首尾空格；UTF-8 编码后最多 72 字节。</p>`}
            <button class="button button-primary button-full" type="submit" data-submit>${isLogin ? "登录工作台" : "创建账户"} ${icon("arrowRight")}</button>
          </form>
          <p class="auth-switch">${isLogin ? "还没有账户？" : "已经有账户？"} <a class="text-link" href="${isLogin ? "/register" : "/login"}" data-route>${isLogin ? "立即注册" : "直接登录"}</a></p>
        </div>
      </main>
    </section>
    <aside class="auth-visual" aria-label="SignalWatch 产品说明">
      <div class="auth-quote"><span class="auth-quote-mark">${icon("quote")}</span><blockquote>“真正稀缺的不是信息，<br>是知道什么值得注意。”</blockquote><p>SignalWatch 希望成为研究者安静的后台助手：持续监听、克制筛选、准时送达。</p></div>
    </aside>
  </div>`;
}

function appLayout(content, active = "dashboard") {
  const profile = state.profile || {};
  const email = escapeHTML(profile.email || "正在读取账户…");
  const initial = escapeHTML((profile.email || "S").slice(0, 1).toUpperCase());
  const isReady = state.readiness && state.readiness.status === "ready";
  return `<div class="app-shell">
    <aside class="sidebar">
      ${brand(true)}
      <p class="sidebar-label">工作区</p>
      <nav class="sidebar-nav" aria-label="工作区导航">
        <a class="sidebar-link ${active === "dashboard" ? "active" : ""}" href="/app" data-route>${icon("layout")}<span>概览</span></a>
        <span class="sidebar-link sidebar-link-muted">${icon("layers")}<span>订阅</span><span class="soon-tag">即将开放</span></span>
        <a class="sidebar-link ${active === "settings" ? "active" : ""}" href="/settings" data-route>${icon("settings")}<span>偏好设置</span></a>
      </nav>
      <div class="sidebar-bottom">
        <div class="service-mini"><div class="service-mini-row"><span>服务状态</span><span class="status-dot ${state.readiness ? (isReady ? "online" : "offline") : ""}"></span></div></div>
        <div class="account-chip"><span class="avatar">${initial}</span><span class="account-copy"><strong>${email}</strong><span>研究者账户</span></span><button class="logout-button" type="button" data-logout aria-label="退出登录" title="退出登录">${icon("logOut")}</button></div>
      </div>
    </aside>
    <nav class="mobile-bar" aria-label="移动端导航">
      <a class="mobile-link ${active === "dashboard" ? "active" : ""}" href="/app" data-route>${icon("layout")}<span>概览</span></a>
      <a class="mobile-link ${active === "settings" ? "active" : ""}" href="/settings" data-route>${icon("settings")}<span>设置</span></a>
      <button class="mobile-link" type="button" data-logout>${icon("logOut")}<span>退出</span></button>
    </nav>
    <main class="workspace"><div class="workspace-inner">${content}</div></main>
  </div>`;
}

function dashboardPage() {
  const profile = state.profile;
  const today = new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric", weekday: "long" }).format(new Date());
  const hour = new Date().getHours();
  const greeting = hour < 6 ? "夜深了" : hour < 12 ? "早上好" : hour < 18 ? "下午好" : "晚上好";
  const ready = state.readiness && state.readiness.status === "ready";
  const checked = Boolean(state.readiness);
  const deliveryTime = (profile && profile.digest_time) || "--:--";
  const timezone = escapeHTML((profile && profile.timezone) || "读取中");
  const maxItems = profile && profile.max_items_per_digest != null ? profile.max_items_per_digest : "--";
  const systemBanner = checked
    ? `<div class="system-banner ${ready ? "" : "error"}">${icon(ready ? "check" : "info")}<span>${ready ? "API、MySQL 与 Redis 均已就绪，当前服务运行正常。" : "依赖服务尚未全部就绪，请检查 MySQL 与 Redis 连接。"}</span></div>`
    : `<div class="system-banner">${icon("activity")}<span>正在检查 SignalWatch 服务状态…</span></div>`;

  const content = `<header class="workspace-header"><div><p class="eyebrow">SIGNAL DESK</p><h1>${greeting}</h1><p>这是你的 SignalWatch 控制台，账户能力已连接到真实后端。</p></div><span class="date-chip">${icon("calendar")} ${today}</span></header>
    ${systemBanner}
    <section class="metric-grid" aria-label="账户摘要">
      <article class="metric-card"><div class="metric-top"><span>摘要送达</span><span class="metric-icon">${icon("clock")}</span></div><div class="metric-value"><strong>${escapeHTML(deliveryTime)}</strong><span>每日 · ${timezone}</span></div></article>
      <article class="metric-card"><div class="metric-top"><span>单次上限</span><span class="metric-icon">${icon("inbox")}</span></div><div class="metric-value"><strong>${escapeHTML(maxItems)}</strong><span>条信号 / 摘要</span></div></article>
      <article class="metric-card"><div class="metric-top"><span>活跃订阅</span><span class="metric-icon">${icon("layers")}</span></div><div class="metric-value"><strong>0</strong><span>订阅 API 待接入</span></div></article>
    </section>
    <section class="dashboard-grid">
      <article class="content-card"><header class="card-header"><h2>今日信号</h2><span>订阅能力预览</span></header><div class="empty-signals"><div><div class="radar"><span class="radar-dot"></span></div><h3>雷达正在等待订阅</h3><p>数据库已经具备订阅、规则与来源结构；等对应 API 接入后，这里会成为你的每日论文信号流。</p></div></div></article>
      <article class="content-card"><header class="card-header"><h2>能力进度</h2><span>当前版本</span></header><div class="roadmap-list">
        <div class="roadmap-item"><span class="roadmap-check">${icon("check")}</span><span class="roadmap-copy"><strong>账户与安全认证</strong><span>注册、登录与 JWT 鉴权已完成</span></span></div>
        <div class="roadmap-item"><span class="roadmap-check">${icon("check")}</span><span class="roadmap-copy"><strong>摘要偏好</strong><span>时区、时间和数量上限可保存</span></span></div>
        <div class="roadmap-item"><span class="roadmap-check pending">${icon("activity")}</span><span class="roadmap-copy"><strong>订阅管理</strong><span>数据表已准备，业务 API 待实现</span></span></div>
        <div class="roadmap-item"><span class="roadmap-check pending">${icon("activity")}</span><span class="roadmap-copy"><strong>内容采集与匹配</strong><span>Worker 当前仅提供运行心跳</span></span></div>
      </div></article>
    </section>`;
  return appLayout(content, "dashboard");
}

const commonTimezones = [
  "UTC", "Asia/Shanghai", "Asia/Hong_Kong", "Asia/Tokyo", "Asia/Seoul",
  "Asia/Singapore", "Asia/Kolkata", "Asia/Dubai", "Europe/London", "Europe/Paris",
  "Europe/Berlin", "America/New_York", "America/Chicago", "America/Denver",
  "America/Los_Angeles", "America/Toronto", "America/Vancouver", "Australia/Sydney",
  "Pacific/Auckland",
];

function settingsPage() {
  const profile = state.profile || {};
  const currentTimezone = profile.timezone || "UTC";
  const timezones = [...new Set([currentTimezone, ...commonTimezones])]
    .map((timezone) => `<option value="${escapeHTML(timezone)}" ${timezone === currentTimezone ? "selected" : ""}>${escapeHTML(timezone)}</option>`)
    .join("");
  const maxItems = Number(profile.max_items_per_digest || 50);
  const createdAt = profile.created_at
    ? new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "short", day: "numeric" }).format(new Date(profile.created_at))
    : "—";
  const initial = escapeHTML((profile.email || "S").slice(0, 1).toUpperCase());
  const content = `<header class="workspace-header"><div><p class="eyebrow">PREFERENCES</p><h1>偏好设置</h1><p>这些设置会决定每日摘要在何时、以多大规模送达。</p></div><a class="button button-ghost button-small" href="/app" data-route>${icon("chevronLeft")} 返回概览</a></header>
    <section class="settings-layout">
      <article class="content-card settings-card">
        <h2>摘要与时区</h2><p>时间按所选 IANA 时区解释。所有修改都会直接保存到你的 SignalWatch 账户。</p>
        <form class="settings-form" id="settings-form">
          <div class="form-row">
            <div class="form-field"><label for="timezone">所在时区</label><select class="form-select" id="timezone" name="timezone">${timezones}</select></div>
            <div class="form-field"><label for="digest-time">期望送达时间</label><input class="form-input" id="digest-time" name="digest_time" type="time" value="${escapeHTML(profile.digest_time || "08:00")}" required></div>
          </div>
          <div class="setting-divider"></div>
          <div class="form-field"><div class="label-row"><label for="max-items">每次摘要数量上限</label><span>1–50 条</span></div><div class="range-row"><input class="range-input" id="max-items" name="max_items_per_digest" type="range" min="1" max="50" step="1" value="${maxItems}"><output class="number-output" id="max-items-output" for="max-items">${maxItems}</output></div></div>
          <div class="settings-actions"><button class="button button-primary" type="submit" data-submit>${icon("save")} 保存设置</button></div>
        </form>
      </article>
      <aside class="content-card profile-panel">
        <div class="profile-hero"><span class="profile-avatar">${initial}</span><h3>${escapeHTML(profile.email || "正在读取…")}</h3><p>SignalWatch ID · ${escapeHTML(profile.id || "—")}</p></div>
        <div class="profile-details"><div class="detail-row"><span>账户状态</span><strong class="status-pill">${profile.status === "active" ? "正常" : escapeHTML(profile.status || "—")}</strong></div><div class="detail-row"><span>注册时间</span><strong>${escapeHTML(createdAt)}</strong></div><div class="detail-row"><span>认证方式</span><strong>Bearer JWT</strong></div></div>
      </aside>
    </section>`;
  return appLayout(content, "settings");
}

function loadingPage(active) {
  const content = `<header class="workspace-header"><div><p class="eyebrow">SIGNALWATCH</p><h1>正在同步账户</h1><p>正在从 API 安全读取你的个性化设置…</p></div></header><section class="metric-grid"><div class="metric-card"><div class="skeleton">正在加载账户信息</div></div><div class="metric-card"><div class="skeleton">正在加载账户信息</div></div><div class="metric-card"><div class="skeleton">正在加载账户信息</div></div></section>`;
  return appLayout(content, active);
}

function errorPage() {
  return `<main class="error-page"><div><strong>404</strong><h1>没有捕捉到这个信号</h1><p>这个页面不存在，或它已经被移动。</p><a class="button button-primary" href="/" data-route>返回首页 ${icon("arrowRight")}</a></div></main>`;
}

function translateError(error) {
  const messages = {
    AUTH_INVALID_CREDENTIALS: "邮箱或密码不正确。",
    AUTH_UNAUTHORIZED: "登录状态已失效，请重新登录。",
    EMAIL_ALREADY_REGISTERED: "这个邮箱已经注册，可以直接登录。",
    VALIDATION_ERROR: "提交的信息不符合要求，请检查后重试。",
    INTERNAL_ERROR: "服务暂时遇到问题，请稍后再试。",
  };
  return messages[error.code] || error.message || "请求未能完成，请稍后重试。";
}

function showToast(title, message, type = "success") {
  let region = document.querySelector(".toast-region");
  if (!region) {
    region = document.createElement("div");
    region.className = "toast-region";
    region.setAttribute("aria-live", "polite");
    document.body.append(region);
  }
  const toast = document.createElement("div");
  toast.className = `toast ${type}`;
  toast.innerHTML = `<span class="toast-icon">${icon(type === "error" ? "info" : "check")}</span><span><strong></strong><span></span></span><button type="button" aria-label="关闭提示">${icon("x")}</button>`;
  toast.querySelector("strong").textContent = title;
  toast.querySelector("span span").textContent = message;
  const remove = () => toast.remove();
  toast.querySelector("button").addEventListener("click", remove);
  region.append(toast);
  window.setTimeout(remove, 4500);
}

function setButtonBusy(button, busy, idleText, busyText) {
  button.disabled = busy;
  button.innerHTML = busy ? `${busyText} ${icon("activity")}` : idleText;
}

function setupAuthForm() {
  const form = document.querySelector("#auth-form");
  if (!form) return;
  const password = form.elements.password;
  const toggle = form.querySelector("[data-password-toggle]");
  toggle.addEventListener("click", () => {
    const visible = password.type === "text";
    password.type = visible ? "password" : "text";
    toggle.setAttribute("aria-label", visible ? "显示密码" : "隐藏密码");
    toggle.innerHTML = icon(visible ? "eye" : "eyeOff");
  });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const mode = form.dataset.mode;
    const email = form.elements.email.value.trim();
    const passwordValue = password.value;
    const emailError = form.querySelector("#email-error");
    const passwordError = form.querySelector("#password-error");
    emailError.textContent = "";
    passwordError.textContent = "";
    form.elements.email.removeAttribute("aria-invalid");
    password.removeAttribute("aria-invalid");

    let valid = true;
    if (!email || !form.elements.email.validity.valid) {
      emailError.textContent = "请输入有效的邮箱地址。";
      form.elements.email.setAttribute("aria-invalid", "true");
      valid = false;
    }
    const passwordBytes = new TextEncoder().encode(passwordValue).length;
    if (!passwordValue || (mode === "register" && ([...passwordValue].length < 8 || passwordBytes > 72))) {
      passwordError.textContent = mode === "register" ? "密码需至少 8 个字符，且不能超过 72 字节。" : "请输入密码。";
      password.setAttribute("aria-invalid", "true");
      valid = false;
    }
    if (!valid) return;

    const button = form.querySelector("[data-submit]");
    const idleText = button.innerHTML;
    setButtonBusy(button, true, idleText, mode === "login" ? "正在登录" : "正在创建");
    try {
      if (mode === "register") {
        await apiRequest("/auth/register", { method: "POST", body: JSON.stringify({ email, password: passwordValue }) });
      }
      const session = await apiRequest("/auth/login", { method: "POST", body: JSON.stringify({ email, password: passwordValue }) });
      storeToken(session.access_token);
      await loadAccount();
      navigate("/app", true);
      showToast(mode === "login" ? "登录成功" : "账户已创建", "欢迎来到你的 SignalWatch 工作台。", "success");
    } catch (error) {
      if (error instanceof ApiError) {
        showToast("操作未完成", translateError(error), "error");
      } else {
        showToast("操作未完成", "发生了意外错误，请稍后重试。", "error");
      }
      setButtonBusy(button, false, idleText, "");
    }
  });
}

function setupSettingsForm() {
  const form = document.querySelector("#settings-form");
  if (!form) return;
  const range = form.elements.max_items_per_digest;
  const output = form.querySelector("#max-items-output");
  range.addEventListener("input", () => { output.textContent = range.value; });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = form.querySelector("[data-submit]");
    const idleText = button.innerHTML;
    setButtonBusy(button, true, idleText, "正在保存");
    try {
      state.profile = await apiRequest("/me", {
        method: "PATCH",
        auth: true,
        body: JSON.stringify({
          timezone: form.elements.timezone.value,
          digest_time: form.elements.digest_time.value,
          max_items_per_digest: Number(range.value),
        }),
      });
      app.innerHTML = settingsPage();
      setupPage();
      showToast("设置已保存", "新的摘要偏好已经同步到账户。", "success");
    } catch (error) {
      showToast("保存失败", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
      if (document.body.contains(button)) setButtonBusy(button, false, idleText, "");
    }
  });
}

async function loadAccount() {
  if (!state.token) return;
  state.profile = await apiRequest("/me", { method: "GET", auth: true });
}

async function loadSystemStatus() {
  const [health, readiness] = await Promise.allSettled([
    apiRequest("/healthz"),
    apiRequest("/readyz"),
  ]);
  state.health = health.status === "fulfilled" ? health.value : { status: "offline" };
  state.readiness = readiness.status === "fulfilled" ? readiness.value : { status: "not_ready" };
}

async function renderProtected(pathname) {
  if (!state.token) {
    navigate("/login", true);
    return;
  }

  const active = pathname === "/settings" ? "settings" : "dashboard";
  if (!state.profile) {
    app.innerHTML = loadingPage(active);
    setupPage();
    try {
      await loadAccount();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return;
      app.innerHTML = appLayout(`<header class="workspace-header"><div><p class="eyebrow">CONNECTION ERROR</p><h1>账户暂时无法读取</h1><p>${escapeHTML(translateError(error))}</p></div></header><button class="button button-primary" type="button" data-retry>重新尝试</button>`, active);
      setupPage();
      const retry = document.querySelector("[data-retry]");
      if (retry) retry.addEventListener("click", route);
      return;
    }
  }

  if (active === "dashboard") await loadSystemStatus();
  app.innerHTML = active === "settings" ? settingsPage() : dashboardPage();
  setupPage();
}

function setupPage() {
  document.querySelectorAll("[data-route]").forEach((link) => {
    link.addEventListener("click", (event) => {
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      event.preventDefault();
      navigate(link.getAttribute("href"));
    });
  });
  document.querySelectorAll("[data-logout]").forEach((button) => {
    button.addEventListener("click", () => {
      clearSession();
      navigate("/", true);
      showToast("已安全退出", "本机保存的访问令牌已经清除。", "success");
    });
  });
  setupAuthForm();
  setupSettingsForm();
}

function route() {
  const pathname = window.location.pathname;
  if ((pathname === "/login" || pathname === "/register") && state.token) {
    navigate("/app", true);
    return;
  }
  if (pathname === "/") {
    app.innerHTML = landingPage();
    setupPage();
    return;
  }
  if (pathname === "/login" || pathname === "/register") {
    app.innerHTML = authPage(pathname.slice(1));
    setupPage();
    return;
  }
  if (pathname === "/app" || pathname === "/settings") {
    renderProtected(pathname);
    return;
  }
  app.innerHTML = errorPage();
  setupPage();
}

window.addEventListener("popstate", route);
route();
