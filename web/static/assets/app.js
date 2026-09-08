const API_ROOT = "/api/v1";
const TOKEN_KEY = "signalwatch.access_token";

const app = document.querySelector("#app");
const state = {
  token: readToken(),
  profile: null,
  activeSubscriptionTotal: null,
  sources: null,
  subscriptionPage: null,
  subscriptionFilters: {
    page: 1,
    enabled: "",
    sourceId: "",
  },
  subscriptionEditor: null,
  paperPage: null,
  paperSubscriptions: [],
  paperFilters: {
    page: 1,
    subscriptionId: "",
  },
  paperDetail: null,
  dashboardPapers: null,
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
  state.activeSubscriptionTotal = null;
  state.sources = null;
  state.subscriptionPage = null;
  state.subscriptionEditor = null;
  state.paperPage = null;
  state.paperSubscriptions = [];
  state.paperFilters = { page: 1, subscriptionId: "" };
  state.paperDetail = null;
  state.dashboardPapers = null;
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
    throw new ApiError(0, { message: "暂时无法连接 SignalWatch，请稍后重试。" });
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
    chevronRight: '<path d="m9 18 6-6-6-6"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L8 18l-4 1 1-4Z"/>',
    trash: '<path d="M3 6h18M8 6V4h8v2M19 6l-1 15H6L5 6M10 11v5M14 11v5"/>',
    pause: '<path d="M8 5v14M16 5v14"/>',
    play: '<path d="m8 5 11 7-11 7Z"/>',
    refresh: '<path d="M20 7h-6V1M4 17h6v6"/><path d="M20 7a9 9 0 0 0-15-2L2 8M4 17a9 9 0 0 0 15 2l3-3"/>',
    x: '<path d="M18 6 6 18M6 6l12 12"/>',
    external: '<path d="M14 3h7v7M10 14 21 3"/><path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5"/>',
    book: '<path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2Z"/>',
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
          <div class="cta-copy"><p class="eyebrow">你的注意力值得被保护</p><h2>从今天开始，<br>只追踪重要的信号。</h2><p>先整理你的关注范围，再用清晰的订阅规则保护每天有限的注意力。</p></div>
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
          <p class="auth-intro">${expired ? "登录状态已失效，请重新登录。" : isLogin ? "登录后继续管理你的订阅和阅读偏好。" : "只需一个账户，即可保存你的订阅和阅读偏好。"}</p>
          <form id="auth-form" novalidate data-mode="${mode}">
            <div class="form-field">
              <label for="email">邮箱地址</label>
              <div class="input-wrap">${icon("mail")}<input class="form-input" id="email" name="email" type="email" autocomplete="email" placeholder="you@example.com" required></div>
              <p class="field-error" id="email-error" aria-live="polite"></p>
            </div>
            <div class="form-field">
              <div class="label-row"><label for="password">密码</label>${isLogin ? "" : "<span>至少 8 个字符</span>"}</div>
              <div class="input-wrap">${icon("shield")}<input class="form-input" id="password" name="password" type="password" autocomplete="${isLogin ? "current-password" : "new-password"}" placeholder="输入你的密码" required><button class="password-toggle" type="button" aria-label="显示密码" data-password-toggle data-password-target="password">${icon("eye")}</button></div>
              <p class="field-error" id="password-error" aria-live="polite"></p>
            </div>
            ${isLogin ? "" : `<div class="form-field"><label for="confirm-password">再次输入密码</label><div class="input-wrap">${icon("shield")}<input class="form-input" id="confirm-password" name="confirm_password" type="password" autocomplete="new-password" placeholder="再次输入相同密码" required><button class="password-toggle" type="button" aria-label="显示确认密码" data-password-toggle data-password-target="confirm-password">${icon("eye")}</button></div><p class="field-error" id="confirm-password-error" aria-live="polite"></p></div><p class="form-note">${icon("info")}密码必须同时包含字母和数字，最多 72 个 UTF-8 字节。</p>`}
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
  return `<div class="app-shell">
    <aside class="sidebar">
      ${brand(true)}
      <p class="sidebar-label">工作区</p>
      <nav class="sidebar-nav" aria-label="工作区导航">
        <a class="sidebar-link ${active === "dashboard" ? "active" : ""}" href="/app" data-route>${icon("layout")}<span>概览</span></a>
        <a class="sidebar-link ${active === "papers" ? "active" : ""}" href="/papers" data-route>${icon("book")}<span>匹配论文</span></a>
        <a class="sidebar-link ${active === "subscriptions" ? "active" : ""}" href="/subscriptions" data-route>${icon("layers")}<span>订阅</span></a>
        <a class="sidebar-link ${active === "settings" ? "active" : ""}" href="/settings" data-route>${icon("settings")}<span>偏好设置</span></a>
      </nav>
      <div class="sidebar-bottom">
        <div class="account-chip"><span class="avatar">${initial}</span><span class="account-copy"><strong>${email}</strong><span>研究者账户</span></span><button class="logout-button" type="button" data-logout aria-label="退出登录" title="退出登录">${icon("logOut")}</button></div>
      </div>
    </aside>
    <nav class="mobile-bar" aria-label="移动端导航">
      <a class="mobile-link ${active === "dashboard" ? "active" : ""}" href="/app" data-route>${icon("layout")}<span>概览</span></a>
      <a class="mobile-link ${active === "papers" ? "active" : ""}" href="/papers" data-route>${icon("book")}<span>论文</span></a>
      <a class="mobile-link ${active === "subscriptions" ? "active" : ""}" href="/subscriptions" data-route>${icon("layers")}<span>订阅</span></a>
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
  const deliveryTime = (profile && profile.digest_time) || "--:--";
  const timezone = escapeHTML((profile && profile.timezone) || "读取中");
  const maxItems = profile && profile.max_items_per_digest != null ? profile.max_items_per_digest : "--";
  const activeSubscriptions = state.activeSubscriptionTotal == null ? "--" : state.activeSubscriptionTotal;
  const matchedPapers = state.dashboardPapers == null ? "--" : state.dashboardPapers.total;
  const recentPapers = state.dashboardPapers && state.dashboardPapers.items.length
    ? `<div class="dashboard-paper-list">${state.dashboardPapers.items.map((paper) => `<button class="dashboard-paper" type="button" data-open-paper="${paper.id}"><span><strong>${escapeHTML(paper.title)}</strong><small>${escapeHTML(paper.categories.join(" · "))} · ${escapeHTML(formatDateTime(paper.first_seen_at))}</small></span>${icon("chevronRight")}</button>`).join("")}</div>`
    : `<div class="empty-signals"><div><div class="radar"><span class="radar-dot"></span></div><h3>${activeSubscriptions > 0 ? "正在等待新的研究信号" : "创建你的第一条订阅"}</h3><p>${activeSubscriptions > 0 ? "Matcher 会在论文进入最近 48 小时抓取窗口后，按分类和标题摘要关键词建立匹配。" : "先创建订阅，系统才会抓取对应分类并生成匹配论文。"}</p><a class="button button-primary button-small empty-action" href="${activeSubscriptions > 0 ? "/papers" : "/subscriptions"}" data-route>${activeSubscriptions > 0 ? "查看匹配论文" : "创建订阅"} ${icon("arrowRight")}</a></div></div>`;

  const content = `<header class="workspace-header"><div><p class="eyebrow">SIGNAL DESK</p><h1>${greeting}</h1><p>查看你的阅读偏好，管理持续关注的研究主题。</p></div><span class="date-chip">${icon("calendar")} ${today}</span></header>
    <section class="metric-grid" aria-label="账户摘要">
      <article class="metric-card"><div class="metric-top"><span>阅读时间偏好</span><span class="metric-icon">${icon("clock")}</span></div><div class="metric-value"><strong>${escapeHTML(deliveryTime)}</strong><span>每日 · ${timezone}</span></div></article>
      <article class="metric-card"><div class="metric-top"><span>每次阅读上限</span><span class="metric-icon">${icon("inbox")}</span></div><div class="metric-value"><strong>${escapeHTML(maxItems)}</strong><span>条内容</span></div></article>
      <article class="metric-card"><div class="metric-top"><span>活跃订阅</span><span class="metric-icon">${icon("layers")}</span></div><div class="metric-value"><strong>${escapeHTML(activeSubscriptions)}</strong><span>最多可启用 20 条</span></div></article>
      <article class="metric-card"><div class="metric-top"><span>匹配论文</span><span class="metric-icon">${icon("book")}</span></div><div class="metric-value"><strong>${escapeHTML(matchedPapers)}</strong><span>按论文去重</span></div></article>
    </section>
    <section class="dashboard-grid">
      <article class="content-card"><header class="card-header"><h2>最近匹配</h2><a class="text-link" href="/papers" data-route>查看全部</a></header>${recentPapers}</article>
      <article class="content-card"><header class="card-header"><h2>使用建议</h2><span>保持关注范围清晰</span></header><div class="roadmap-list">
        <div class="roadmap-item"><span class="roadmap-check">${icon("check")}</span><span class="roadmap-copy"><strong>设置阅读偏好</strong><span>选择适合自己的时区、时间和数量上限</span></span></div>
        <div class="roadmap-item"><span class="roadmap-check">${icon("layers")}</span><span class="roadmap-copy"><strong>定义关注范围</strong><span>使用分类、作者和包含关键词描述主题</span></span></div>
        <div class="roadmap-item"><span class="roadmap-check">${icon("filter")}</span><span class="roadmap-copy"><strong>减少无关内容</strong><span>通过排除关键词持续优化每条订阅</span></span></div>
      </div></article>
    </section>`;
  return appLayout(content, "dashboard");
}

function formatDateTime(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

function formatFullDateTime(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "long",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

function safeExternalURL(value) {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" || parsed.protocol === "http:" ? escapeHTML(parsed.href) : "#";
  } catch {
    return "#";
  }
}

function truncateText(value, limit = 260) {
  const normalized = String(value || "").trim().replace(/\s+/gu, " ");
  return [...normalized].length > limit ? `${[...normalized].slice(0, limit).join("")}…` : normalized;
}

function paperSubscriptionOptions(selectedId = "") {
  const options = (state.paperSubscriptions || []).map((subscription) => {
    const selected = String(subscription.id) === String(selectedId) ? "selected" : "";
    return `<option value="${subscription.id}" ${selected}>${escapeHTML(subscription.name)} · ${escapeHTML(subscription.rules.categories[0] || "")}</option>`;
  });
  options.unshift(`<option value="" ${selectedId === "" ? "selected" : ""}>全部订阅</option>`);
  return options.join("");
}

function matchReason(match, detailed = false) {
  const keywords = match.matched_keywords || [];
  return `<div class="match-reason">
    <div class="match-reason-title"><strong>${escapeHTML(match.subscription_name)}</strong><span class="match-state ${match.subscription_active ? "active" : "inactive"}">${match.subscription_active ? "监听中" : "历史订阅"}</span></div>
    <div class="match-tags"><span class="paper-category">${escapeHTML(match.category)}</span>${keywords.length ? keywords.map((keyword) => `<span class="match-keyword">${escapeHTML(keyword)}</span>`).join("") : '<span class="match-keyword category-only">仅分类命中</span>'}</div>
    ${detailed ? `<small>首次匹配于 ${escapeHTML(formatFullDateTime(match.matched_at))}</small>` : ""}
  </div>`;
}

function paperCard(item) {
  const authors = (item.authors || []).slice(0, 4).join(" · ");
  const extraAuthors = (item.authors || []).length > 4 ? ` 等 ${item.authors.length} 位作者` : "";
  return `<article class="matched-paper-card">
    <header class="paper-card-header">
      <div class="paper-card-meta"><span>${escapeHTML(item.arxiv_id)}</span><span>发现于 ${escapeHTML(formatDateTime(item.first_seen_at))}</span></div>
      <div class="paper-categories">${(item.categories || []).map((category) => `<span class="paper-category">${escapeHTML(category)}</span>`).join("")}</div>
    </header>
    <button class="paper-title-button" type="button" data-open-paper="${item.id}"><h2>${escapeHTML(item.title)}</h2></button>
    <p class="paper-authors">${escapeHTML(authors)}${escapeHTML(extraAuthors)}</p>
    <p class="paper-abstract">${escapeHTML(truncateText(item.abstract))}</p>
    <div class="paper-match-summary">${(item.matches || []).map((match) => matchReason(match)).join("")}</div>
    <footer class="paper-card-actions">
      <button class="button button-ghost button-small" type="button" data-open-paper="${item.id}">${icon("book")} 查看详情</button>
      <a class="button button-ghost button-small" href="${safeExternalURL(item.arxiv_url)}" target="_blank" rel="noopener noreferrer">arXiv ${icon("external")}</a>
      <a class="button button-primary button-small" href="${safeExternalURL(item.pdf_url)}" target="_blank" rel="noopener noreferrer">PDF ${icon("external")}</a>
    </footer>
  </article>`;
}

function paperDetailModal() {
  const item = state.paperDetail;
  if (!item) return "";
  return `<div class="modal-backdrop" data-close-paper>
    <article class="paper-detail-modal" role="dialog" aria-modal="true" aria-labelledby="paper-detail-title" data-paper-modal-panel>
      <header class="paper-detail-header">
        <div><p class="eyebrow">MATCHED PAPER · ${escapeHTML(item.arxiv_id)}</p><h2 id="paper-detail-title">${escapeHTML(item.title)}</h2></div>
        <button class="icon-button" type="button" data-close-paper aria-label="关闭论文详情">${icon("x")}</button>
      </header>
      <div class="paper-detail-body">
        <div class="paper-detail-meta"><span><strong>发布时间</strong>${escapeHTML(formatFullDateTime(item.published_at))}</span><span><strong>arXiv 更新</strong>${escapeHTML(formatFullDateTime(item.arxiv_updated_at))}</span><span><strong>本地发现</strong>${escapeHTML(formatFullDateTime(item.first_seen_at))}</span></div>
        <div class="paper-categories">${(item.categories || []).map((category) => `<span class="paper-category">${escapeHTML(category)}</span>`).join("")}</div>
        <section class="paper-detail-section"><h3>作者</h3><p>${escapeHTML((item.authors || []).join(" · "))}</p></section>
        <section class="paper-detail-section"><h3>摘要</h3><p>${escapeHTML(item.abstract)}</p></section>
        <section class="paper-detail-section"><h3>匹配原因</h3><div class="paper-detail-matches">${(item.matches || []).map((match) => matchReason(match, true)).join("")}</div></section>
      </div>
      <footer class="paper-detail-actions"><a class="button button-ghost" href="${safeExternalURL(item.arxiv_url)}" target="_blank" rel="noopener noreferrer">打开 arXiv ${icon("external")}</a><a class="button button-primary" href="${safeExternalURL(item.pdf_url)}" target="_blank" rel="noopener noreferrer">查看 PDF ${icon("external")}</a></footer>
    </article>
  </div>`;
}

function papersPage() {
  const result = state.paperPage || { items: [], page: 1, page_size: 20, total: 0 };
  const filters = state.paperFilters;
  const totalPages = Math.max(1, Math.ceil(result.total / result.page_size));
  const list = result.items.length
    ? `<div class="matched-paper-list">${result.items.map(paperCard).join("")}</div>`
    : `<div class="subscriptions-empty papers-empty"><span>${icon("book")}</span><h2>${filters.subscriptionId ? "这条订阅还没有匹配论文" : "还没有捕捉到论文信号"}</h2><p>${filters.subscriptionId ? "可以切换到全部订阅，或等待下一轮 Collector 完成抓取和匹配。" : "创建并启用订阅后，系统会在后台抓取最近 48 小时的 arXiv 更新并显示匹配结果。"}</p><a class="button button-primary" href="/subscriptions" data-route>${icon("plus")} 管理订阅</a></div>`;
  const content = `<header class="workspace-header subscriptions-heading"><div><p class="eyebrow">MATCHED PAPERS</p><h1>匹配论文</h1><p>每篇论文只展示一次，并保留所有命中订阅与首次匹配原因。</p></div><span class="date-chip">${icon("activity")} 本地确定性匹配</span></header>
    <section class="subscription-toolbar paper-toolbar" aria-label="匹配论文筛选">
      <div><strong>${result.total}</strong><span>篇论文</span></div>
      <label><span>命中订阅</span><select class="form-select compact-select" data-filter-paper-subscription>${paperSubscriptionOptions(filters.subscriptionId)}</select></label>
      <button class="icon-button" type="button" data-refresh-papers aria-label="刷新匹配论文" title="刷新">${icon("refresh")}</button>
    </section>
    ${list}
    ${result.total > result.page_size ? `<nav class="pagination" aria-label="匹配论文分页"><button class="button button-ghost button-small" type="button" data-paper-page="${result.page - 1}" ${result.page <= 1 ? "disabled" : ""}>${icon("chevronLeft")} 上一页</button><span>第 ${result.page} / ${totalPages} 页</span><button class="button button-ghost button-small" type="button" data-paper-page="${result.page + 1}" ${result.page >= totalPages ? "disabled" : ""}>下一页 ${icon("chevronRight")}</button></nav>` : ""}
    ${paperDetailModal()}`;
  return appLayout(content, "papers");
}

function ruleValues(value) {
  const seen = new Set();
  return String(value || "")
    .split(/[\n,，]+/u)
    .map((item) => item.trim().replace(/\s+/gu, " "))
    .filter((item) => {
      const key = item.toLocaleLowerCase();
      if (!item || seen.has(key)) return false;
      seen.add(key);
      return true;
    });
}

function sourceOptions(selectedId = "", includeAll = false, extraSource = null) {
  const sources = [...(state.sources || [])];
  if (extraSource && !sources.some((source) => source.id === extraSource.id)) sources.push(extraSource);
  const options = sources.map((source) => {
    const selected = String(source.id) === String(selectedId) ? "selected" : "";
    return `<option value="${source.id}" ${selected}>${escapeHTML(source.name)} · ${escapeHTML(source.kind)}</option>`;
  });
  if (includeAll) options.unshift(`<option value="" ${selectedId === "" ? "selected" : ""}>全部来源</option>`);
  else options.unshift('<option value="">请选择来源</option>');
  return options.join("");
}

function rulePills(values, emptyText) {
  if (!values || values.length === 0) return `<span class="rule-empty">${emptyText}</span>`;
  return values.map((value) => `<span class="rule-pill">${escapeHTML(value)}</span>`).join("");
}

function subscriptionCard(item) {
  const objective = item.objective || "未填写研究目标";
  return `<article class="subscription-card" data-subscription-card="${item.id}">
    <div class="subscription-main">
      <header class="subscription-card-header">
        <div><div class="subscription-meta"><span class="source-badge">${escapeHTML(item.source.name)}</span><span>v${item.version}</span><span>更新于 ${escapeHTML(formatDateTime(item.updated_at))}</span></div><h2>${escapeHTML(item.name)}</h2></div>
        <span class="subscription-status ${item.enabled ? "enabled" : "paused"}">${item.enabled ? "监听中" : "已暂停"}</span>
      </header>
      <p class="subscription-objective">${escapeHTML(objective)}</p>
      <div class="rule-groups">
        <div class="rule-group"><strong>分类</strong><div>${rulePills(item.rules.categories, "未配置")}</div></div>
        <div class="rule-group"><strong>作者</strong><div>${rulePills(item.rules.authors, "不限作者")}</div></div>
        <div class="rule-group"><strong>包含关键词</strong><div>${rulePills(item.rules.include_keywords, "不限关键词")}</div></div>
        <div class="rule-group exclude"><strong>排除关键词</strong><div>${rulePills(item.rules.exclude_keywords, "不排除")}</div></div>
      </div>
    </div>
    <footer class="subscription-actions">
      <button class="button button-ghost button-small" type="button" data-edit-subscription="${item.id}">${icon("edit")} 编辑</button>
      <button class="button button-ghost button-small" type="button" data-toggle-subscription="${item.id}" data-version="${item.version}" data-enabled="${item.enabled}">${icon(item.enabled ? "pause" : "play")} ${item.enabled ? "暂停" : "启用"}</button>
      <button class="icon-button danger" type="button" data-delete-subscription="${item.id}" data-version="${item.version}" data-name="${escapeHTML(item.name)}" aria-label="删除 ${escapeHTML(item.name)}" title="删除">${icon("trash")}</button>
    </footer>
  </article>`;
}

function categoryChoices(source, selected = []) {
  if (!source) return '<p class="category-hint">请先选择来源。</p>';
  if (!(source.rule_types || []).includes("category")) return '<p class="category-hint error">这个来源不支持分类规则，暂时无法创建订阅。</p>';
  const allowed = source.allowed_categories || [];
  if (allowed.length === 0) return '<p class="category-hint error">这个来源没有可用分类，暂时无法创建订阅。</p>';
  const selectedCategory = selected[0] || "";
  return allowed.map((category) => `<label class="category-choice"><input type="radio" name="categories" value="${escapeHTML(category)}" ${selectedCategory === category ? "checked" : ""}><span>${escapeHTML(category)}</span></label>`).join("");
}

function subscriptionEditorModal() {
  const editor = state.subscriptionEditor;
  if (!editor) return "";
  const item = editor.item;
  const editing = editor.mode === "edit";
  const selectedSourceId = editing ? item.source.id : editor.sourceId;
  const source = (state.sources || []).find((candidate) => String(candidate.id) === String(selectedSourceId)) || (editing ? item.source : null);
  const rules = editing ? item.rules : { categories: [], authors: [], include_keywords: [], exclude_keywords: [] };
  const ruleTypes = new Set(source ? source.rule_types || [] : []);
  const authorDisabled = source && !ruleTypes.has("author") ? "disabled" : "";
  const includeDisabled = source && !ruleTypes.has("include_keyword") ? "disabled" : "";
  const excludeDisabled = source && !ruleTypes.has("exclude_keyword") ? "disabled" : "";
  return `<div class="modal-backdrop" data-close-editor>
    <section class="subscription-modal" role="dialog" aria-modal="true" aria-labelledby="subscription-editor-title" data-modal-panel>
      <header class="modal-header"><div><p class="eyebrow">${editing ? "EDIT SIGNAL" : "NEW SIGNAL"}</p><h2 id="subscription-editor-title">${editing ? "编辑订阅" : "创建订阅"}</h2></div><button class="icon-button" type="button" data-close-editor aria-label="关闭">${icon("x")}</button></header>
      <form id="subscription-form" class="subscription-form" data-mode="${editor.mode}" data-version="${editing ? item.version : ""}" data-id="${editing ? item.id : ""}" novalidate>
        <div class="form-row">
          <div class="form-field"><label for="subscription-source">信息来源</label><select class="form-select" id="subscription-source" name="source_id" ${editing ? "disabled" : ""} required>${sourceOptions(selectedSourceId, false, editing ? item.source : null)}</select><p class="input-help">创建后不可更换来源。</p></div>
          <div class="form-field"><label for="subscription-name">订阅名称</label><input class="form-input plain-input" id="subscription-name" name="name" maxlength="100" value="${editing ? escapeHTML(item.name) : ""}" placeholder="例如：测试时自适应" required><p class="field-error" data-error="name"></p></div>
        </div>
        <div class="form-field"><div class="label-row"><label for="subscription-objective">研究目标</label><span>可选 · 最多 500 字</span></div><textarea class="form-textarea" id="subscription-objective" name="objective" maxlength="500" rows="3" placeholder="这条订阅希望帮你持续关注什么？">${editing ? escapeHTML(item.objective || "") : ""}</textarea></div>
        <fieldset class="rule-fieldset"><legend>分类 <span>请选择 1 个</span></legend><div class="category-grid" data-category-choices>${categoryChoices(source, rules.categories)}</div><p class="field-error" data-error="categories"></p></fieldset>
        <div class="form-row rule-text-row">
          <div class="form-field"><div class="label-row"><label for="subscription-authors">作者</label><span>最多 20 个</span></div><textarea class="form-textarea" id="subscription-authors" name="authors" rows="3" placeholder="${authorDisabled ? "当前来源不支持作者规则" : "每行或逗号分隔"}" ${authorDisabled}>${escapeHTML(rules.authors.join("\n"))}</textarea></div>
          <div class="form-field"><div class="label-row"><label for="subscription-includes">包含关键词</label><span>最多 30 个</span></div><textarea class="form-textarea" id="subscription-includes" name="include_keywords" rows="3" placeholder="${includeDisabled ? "当前来源不支持包含关键词" : "例如：calibration, uncertainty"}" ${includeDisabled}>${escapeHTML(rules.include_keywords.join("\n"))}</textarea></div>
        </div>
        <div class="form-field"><div class="label-row"><label for="subscription-excludes">排除关键词</label><span>最多 30 个</span></div><textarea class="form-textarea" id="subscription-excludes" name="exclude_keywords" rows="2" placeholder="${excludeDisabled ? "当前来源不支持排除关键词" : "不希望命中的主题"}" ${excludeDisabled}>${escapeHTML(rules.exclude_keywords.join("\n"))}</textarea><p class="field-error" data-error="rules"></p></div>
        <label class="enabled-control"><input type="checkbox" name="enabled" ${!editing || item.enabled ? "checked" : ""}><span><strong>立即启用</strong><small>启用的订阅会计入每人最多 20 条的上限。</small></span></label>
        <footer class="modal-actions"><button class="button button-ghost" type="button" data-close-editor>取消</button><button class="button button-primary" type="submit" data-submit>${editing ? `${icon("save")} 保存修改` : `${icon("plus")} 创建订阅`}</button></footer>
      </form>
    </section>
  </div>`;
}

function subscriptionsPage() {
  const result = state.subscriptionPage || { items: [], page: 1, page_size: 20, total: 0 };
  const filters = state.subscriptionFilters;
  const totalPages = Math.max(1, Math.ceil(result.total / result.page_size));
  const list = result.items.length
    ? `<div class="subscription-list">${result.items.map(subscriptionCard).join("")}</div>`
    : `<div class="subscriptions-empty"><span>${icon("layers")}</span><h2>${filters.enabled || filters.sourceId ? "没有符合筛选条件的订阅" : "建立第一条研究信号"}</h2><p>${filters.enabled || filters.sourceId ? "调整筛选条件，或创建一条新的订阅。" : "选择来源和分类，再用作者、包含及排除关键词收窄范围。"}</p><button class="button button-primary" type="button" data-new-subscription>${icon("plus")} 创建订阅</button></div>`;
  const content = `<header class="workspace-header subscriptions-heading"><div><p class="eyebrow">SUBSCRIPTIONS</p><h1>订阅管理</h1><p>集中管理你关注的信息来源、分类、作者和关键词。</p></div><button class="button button-primary" type="button" data-new-subscription>${icon("plus")} 新建订阅</button></header>
    <section class="subscription-toolbar" aria-label="订阅筛选">
      <div><strong>${result.total}</strong><span>条订阅</span></div>
      <label><span>状态</span><select class="form-select compact-select" data-filter-enabled><option value="" ${filters.enabled === "" ? "selected" : ""}>全部状态</option><option value="true" ${filters.enabled === "true" ? "selected" : ""}>监听中</option><option value="false" ${filters.enabled === "false" ? "selected" : ""}>已暂停</option></select></label>
      <label><span>来源</span><select class="form-select compact-select" data-filter-source>${sourceOptions(filters.sourceId, true)}</select></label>
      <button class="icon-button" type="button" data-refresh-subscriptions aria-label="刷新订阅" title="刷新">${icon("refresh")}</button>
    </section>
    ${list}
    ${result.total > result.page_size ? `<nav class="pagination" aria-label="订阅分页"><button class="button button-ghost button-small" type="button" data-subscription-page="${result.page - 1}" ${result.page <= 1 ? "disabled" : ""}>${icon("chevronLeft")} 上一页</button><span>第 ${result.page} / ${totalPages} 页</span><button class="button button-ghost button-small" type="button" data-subscription-page="${result.page + 1}" ${result.page >= totalPages ? "disabled" : ""}>下一页 ${icon("chevronRight")}</button></nav>` : ""}
    ${subscriptionEditorModal()}`;
  return appLayout(content, "subscriptions");
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
        <div class="profile-details"><div class="detail-row"><span>账户状态</span><strong class="status-pill">${profile.status === "active" ? "正常" : escapeHTML(profile.status || "—")}</strong></div><div class="detail-row"><span>注册时间</span><strong>${escapeHTML(createdAt)}</strong></div><div class="detail-row"><span>当前时区</span><strong>${escapeHTML(currentTimezone)}</strong></div></div>
      </aside>
    </section>`;
  return appLayout(content, "settings");
}

function loadingPage(active) {
  const content = `<header class="workspace-header"><div><p class="eyebrow">SIGNALWATCH</p><h1>正在准备你的工作区</h1><p>正在读取你的账户、偏好和订阅信息…</p></div></header><section class="metric-grid"><div class="metric-card"><div class="skeleton">正在加载账户信息</div></div><div class="metric-card"><div class="skeleton">正在加载账户信息</div></div><div class="metric-card"><div class="skeleton">正在加载账户信息</div></div></section>`;
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
    SOURCE_NOT_FOUND: "信息来源不存在或已经停用。",
    SUBSCRIPTION_LIMIT_REACHED: "最多只能同时启用 20 条订阅，请先暂停其他订阅。",
    SUBSCRIPTION_NOT_FOUND: "订阅不存在、已删除，或你没有访问权限。",
    SUBSCRIPTION_VERSION_CONFLICT: "订阅已被其他请求修改，请刷新后重试。",
    PAPER_NOT_FOUND: "论文不存在，或它不属于你的匹配结果。",
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
  form.querySelectorAll("[data-password-toggle]").forEach((toggle) => {
    toggle.addEventListener("click", () => {
      const input = form.querySelector(`#${toggle.dataset.passwordTarget}`);
      const visible = input.type === "text";
      input.type = visible ? "password" : "text";
      toggle.setAttribute("aria-label", visible ? "显示密码" : "隐藏密码");
      toggle.innerHTML = icon(visible ? "eye" : "eyeOff");
    });
  });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const mode = form.dataset.mode;
    const email = form.elements.email.value.trim();
    const passwordValue = password.value;
    const confirmPassword = form.elements.confirm_password;
    const emailError = form.querySelector("#email-error");
    const passwordError = form.querySelector("#password-error");
    const confirmPasswordError = form.querySelector("#confirm-password-error");
    emailError.textContent = "";
    passwordError.textContent = "";
    if (confirmPasswordError) confirmPasswordError.textContent = "";
    form.elements.email.removeAttribute("aria-invalid");
    password.removeAttribute("aria-invalid");
    if (confirmPassword) confirmPassword.removeAttribute("aria-invalid");

    let valid = true;
    if (!email || !form.elements.email.validity.valid) {
      emailError.textContent = "请输入有效的邮箱地址。";
      form.elements.email.setAttribute("aria-invalid", "true");
      valid = false;
    }
    const passwordBytes = new TextEncoder().encode(passwordValue).length;
    if (!passwordValue) {
      passwordError.textContent = "请输入密码。";
      password.setAttribute("aria-invalid", "true");
      valid = false;
    } else if (mode === "register" && ([...passwordValue].length < 8 || passwordBytes > 72)) {
      passwordError.textContent = "密码需至少 8 个字符，且不能超过 72 个 UTF-8 字节。";
      password.setAttribute("aria-invalid", "true");
      valid = false;
    } else if (mode === "register" && (!/\p{L}/u.test(passwordValue) || !/\p{N}/u.test(passwordValue))) {
      passwordError.textContent = "密码必须同时包含字母和数字。";
      password.setAttribute("aria-invalid", "true");
      valid = false;
    }
    if (mode === "register" && !confirmPassword.value) {
      confirmPasswordError.textContent = "请再次输入密码。";
      confirmPassword.setAttribute("aria-invalid", "true");
      valid = false;
    } else if (mode === "register" && confirmPassword.value !== passwordValue) {
      confirmPasswordError.textContent = "两次输入的密码不一致。";
      confirmPassword.setAttribute("aria-invalid", "true");
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

function renderSubscriptions() {
  app.innerHTML = subscriptionsPage();
  setupPage();
}

function renderPapers() {
  app.innerHTML = papersPage();
  setupPage();
}

async function loadPapers() {
  const filters = state.paperFilters;
  const query = new URLSearchParams({
    page: String(filters.page),
    page_size: "20",
  });
  if (filters.subscriptionId !== "") query.set("subscription_id", filters.subscriptionId);
  state.paperPage = await apiRequest(`/papers?${query}`, { method: "GET", auth: true });
}

async function loadPaperWorkspace() {
  const [subscriptions] = await Promise.all([
    apiRequest("/subscriptions?page=1&page_size=100", { method: "GET", auth: true }),
    loadPapers(),
  ]);
  state.paperSubscriptions = subscriptions.items;
  state.paperDetail = null;
  const requestedPaper = new URLSearchParams(window.location.search).get("paper");
  if (requestedPaper && /^\d+$/u.test(requestedPaper)) {
    state.paperDetail = await apiRequest(`/papers/${requestedPaper}`, { method: "GET", auth: true });
  }
}

async function refreshPapers(message = "") {
  await loadPapers();
  renderPapers();
  if (message) showToast("匹配论文已刷新", message, "success");
}

async function loadSubscriptions() {
  const filters = state.subscriptionFilters;
  const query = new URLSearchParams({
    page: String(filters.page),
    page_size: "20",
  });
  if (filters.enabled !== "") query.set("enabled", filters.enabled);
  if (filters.sourceId !== "") query.set("source_id", filters.sourceId);
  state.subscriptionPage = await apiRequest(`/subscriptions?${query}`, { method: "GET", auth: true });
}

async function loadSubscriptionWorkspace() {
  const [sources] = await Promise.all([
    apiRequest("/sources", { method: "GET", auth: true }),
    loadSubscriptions(),
  ]);
  state.sources = sources;
}

function closeSubscriptionEditor() {
  state.subscriptionEditor = null;
  renderSubscriptions();
}

async function refreshSubscriptions(message = "") {
  await loadSubscriptions();
  renderSubscriptions();
  if (message) showToast("订阅已刷新", message, "success");
}

function validateSubscriptionForm(form) {
  const name = form.elements.name.value.trim();
  const categories = [...form.querySelectorAll('input[name="categories"]:checked')].map((input) => input.value);
  const includeKeywords = ruleValues(form.elements.include_keywords.value);
  const rules = {
    categories,
    include_keywords: includeKeywords,
  };
  form.querySelectorAll("[data-error]").forEach((element) => { element.textContent = ""; });
  form.elements.name.removeAttribute("aria-invalid");

  if (!name || [...name].length > 100) {
    form.querySelector('[data-error="name"]').textContent = "请输入 1–100 个字符的订阅名称。";
    form.elements.name.setAttribute("aria-invalid", "true");
    return null;
  }
  if (categories.length !== 1) {
    form.querySelector('[data-error="categories"]').textContent = "请选择 1 个分类。";
    return null;
  }
  if (includeKeywords.length > 30 || includeKeywords.some((value) => [...value].length > 100)) {
    form.querySelector('[data-error="rules"]').textContent = "关键词最多 30 个，且每一项不能超过 100 个字符。";
    return null;
  }

  const objective = form.elements.objective.value.trim();
  return {
    source_id: Number(form.elements.source_id.value),
    name,
    objective: objective || null,
    enabled: form.elements.enabled.checked,
    rules,
  };
}

async function handleSubscriptionConflict(error) {
  if (!(error instanceof ApiError)) return false;
  if (error.status === 401) return true;
  if (error.code !== "SUBSCRIPTION_VERSION_CONFLICT") return false;
  state.subscriptionEditor = null;
  await loadSubscriptions();
  renderSubscriptions();
  showToast("订阅已发生变化", "服务器上的版本更新，列表已刷新，请重新操作。", "error");
  return true;
}

function setupSubscriptionForm() {
  const form = document.querySelector("#subscription-form");
  if (!form) return;
  const sourceSelect = form.elements.source_id;
  if (!sourceSelect.disabled) {
    sourceSelect.addEventListener("change", () => {
      state.subscriptionEditor.sourceId = sourceSelect.value;
      const source = (state.sources || []).find((candidate) => String(candidate.id) === sourceSelect.value);
      form.querySelector("[data-category-choices]").innerHTML = categoryChoices(source, []);
      const supported = new Set(source ? source.rule_types || [] : []);
      const controls = [
        [form.elements.authors, "author", "每行或逗号分隔", "当前来源不支持作者规则"],
        [form.elements.include_keywords, "include_keyword", "例如：calibration, uncertainty", "当前来源不支持包含关键词"],
        [form.elements.exclude_keywords, "exclude_keyword", "不希望命中的主题", "当前来源不支持排除关键词"],
      ];
      controls.forEach(([control, ruleType, availablePlaceholder, unavailablePlaceholder]) => {
        control.disabled = Boolean(source) && !supported.has(ruleType);
        control.placeholder = control.disabled ? unavailablePlaceholder : availablePlaceholder;
        if (control.disabled) control.value = "";
      });
    });
  }

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const payload = validateSubscriptionForm(form);
    if (!payload) return;
    if (!payload.source_id) {
      showToast("请选择来源", "创建订阅前需要选择一个可用信息来源。", "error");
      return;
    }

    const button = form.querySelector("[data-submit]");
    const idleText = button.innerHTML;
    setButtonBusy(button, true, idleText, "正在保存");
    try {
      if (form.dataset.mode === "edit") {
        delete payload.source_id;
        await apiRequest(`/subscriptions/${form.dataset.id}`, {
          method: "PATCH",
          auth: true,
          headers: { "If-Match": `"${form.dataset.version}"` },
          body: JSON.stringify(payload),
        });
      } else {
        await apiRequest("/subscriptions", {
          method: "POST",
          auth: true,
          body: JSON.stringify(payload),
        });
        state.subscriptionFilters.page = 1;
      }
      state.subscriptionEditor = null;
      await loadSubscriptions();
      renderSubscriptions();
      showToast(form.dataset.mode === "edit" ? "订阅已更新" : "订阅已创建", "规则已安全保存。", "success");
    } catch (error) {
      if (await handleSubscriptionConflict(error)) return;
      showToast("保存失败", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
      if (document.body.contains(button)) setButtonBusy(button, false, idleText, "");
    }
  });
}

function setupSubscriptionsPage() {
  document.querySelectorAll("[data-new-subscription]").forEach((button) => {
    button.addEventListener("click", () => {
      state.subscriptionEditor = {
        mode: "create",
        sourceId: state.sources && state.sources.length ? String(state.sources[0].id) : "",
      };
      renderSubscriptions();
      const nameInput = document.querySelector("#subscription-name");
      if (nameInput) nameInput.focus();
    });
  });

  document.querySelectorAll("[data-close-editor]").forEach((element) => {
    element.addEventListener("click", (event) => {
      if (element.hasAttribute("data-modal-panel")) return;
      if (event.target.closest("[data-modal-panel]") && event.currentTarget.classList.contains("modal-backdrop")) return;
      closeSubscriptionEditor();
    });
  });
  const modalPanel = document.querySelector("[data-modal-panel]");
  if (modalPanel) modalPanel.addEventListener("click", (event) => event.stopPropagation());

  document.querySelectorAll("[data-edit-subscription]").forEach((button) => {
    button.addEventListener("click", async () => {
      const idleText = button.innerHTML;
      setButtonBusy(button, true, idleText, "读取中");
      try {
        const item = await apiRequest(`/subscriptions/${button.dataset.editSubscription}`, { method: "GET", auth: true });
        state.subscriptionEditor = { mode: "edit", item };
        renderSubscriptions();
        const nameInput = document.querySelector("#subscription-name");
        if (nameInput) nameInput.focus();
      } catch (error) {
        showToast("无法打开订阅", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
        if (document.body.contains(button)) setButtonBusy(button, false, idleText, "");
      }
    });
  });

  document.querySelectorAll("[data-toggle-subscription]").forEach((button) => {
    button.addEventListener("click", async () => {
      const enabled = button.dataset.enabled === "true";
      const idleText = button.innerHTML;
      setButtonBusy(button, true, idleText, enabled ? "暂停中" : "启用中");
      try {
        await apiRequest(`/subscriptions/${button.dataset.toggleSubscription}`, {
          method: "PATCH",
          auth: true,
          headers: { "If-Match": `"${button.dataset.version}"` },
          body: JSON.stringify({ enabled: !enabled }),
        });
        await refreshSubscriptions(enabled ? "该订阅已暂停。" : "该订阅已开始监听。 ");
      } catch (error) {
        if (await handleSubscriptionConflict(error)) return;
        showToast("状态修改失败", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
        if (document.body.contains(button)) setButtonBusy(button, false, idleText, "");
      }
    });
  });

  document.querySelectorAll("[data-delete-subscription]").forEach((button) => {
    button.addEventListener("click", async () => {
      if (!window.confirm(`确认删除“${button.dataset.name}”？删除后不会出现在列表中。`)) return;
      button.disabled = true;
      try {
        await apiRequest(`/subscriptions/${button.dataset.deleteSubscription}`, {
          method: "DELETE",
          auth: true,
          headers: { "If-Match": `"${button.dataset.version}"` },
        });
        if (state.subscriptionPage.items.length === 1 && state.subscriptionFilters.page > 1) state.subscriptionFilters.page--;
        await refreshSubscriptions("订阅已删除。 ");
      } catch (error) {
        if (await handleSubscriptionConflict(error)) return;
        showToast("删除失败", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
        if (document.body.contains(button)) button.disabled = false;
      }
    });
  });

  const enabledFilter = document.querySelector("[data-filter-enabled]");
  if (enabledFilter) enabledFilter.addEventListener("change", async (event) => {
    state.subscriptionFilters.enabled = event.target.value;
    state.subscriptionFilters.page = 1;
    try {
      await refreshSubscriptions();
    } catch (error) {
      showToast("筛选失败", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
    }
  });
  const sourceFilter = document.querySelector("[data-filter-source]");
  if (sourceFilter) sourceFilter.addEventListener("change", async (event) => {
    state.subscriptionFilters.sourceId = event.target.value;
    state.subscriptionFilters.page = 1;
    try {
      await refreshSubscriptions();
    } catch (error) {
      showToast("筛选失败", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
    }
  });
  const refreshButton = document.querySelector("[data-refresh-subscriptions]");
  if (refreshButton) refreshButton.addEventListener("click", async () => {
    try {
      await refreshSubscriptions("已同步服务器上的最新状态。");
    } catch (error) {
      showToast("刷新失败", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
    }
  });
  document.querySelectorAll("[data-subscription-page]").forEach((button) => {
    button.addEventListener("click", async () => {
      state.subscriptionFilters.page = Number(button.dataset.subscriptionPage);
      try {
        await refreshSubscriptions();
        window.scrollTo({ top: 0, behavior: "smooth" });
      } catch (error) {
        showToast("翻页失败", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
      }
    });
  });
  setupSubscriptionForm();
}

function setupPapersPage() {
  document.querySelectorAll("[data-open-paper]").forEach((button) => {
    button.addEventListener("click", async () => {
      const paperID = button.dataset.openPaper;
      if (window.location.pathname !== "/papers") {
        navigate(`/papers?paper=${paperID}`);
        return;
      }
      const idleText = button.innerHTML;
      setButtonBusy(button, true, idleText, "读取中");
      try {
        state.paperDetail = await apiRequest(`/papers/${paperID}`, { method: "GET", auth: true });
        window.history.replaceState({}, "", `/papers?paper=${paperID}`);
        renderPapers();
      } catch (error) {
        showToast("无法打开论文", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
        if (document.body.contains(button)) setButtonBusy(button, false, idleText, "");
      }
    });
  });
  document.querySelectorAll("[data-close-paper]").forEach((element) => {
    element.addEventListener("click", (event) => {
      if (event.target.closest("[data-paper-modal-panel]") && element.classList.contains("modal-backdrop")) return;
      state.paperDetail = null;
      window.history.replaceState({}, "", "/papers");
      renderPapers();
    });
  });
  const modalPanel = document.querySelector("[data-paper-modal-panel]");
  if (modalPanel) modalPanel.addEventListener("click", (event) => event.stopPropagation());

  const subscriptionFilter = document.querySelector("[data-filter-paper-subscription]");
  if (subscriptionFilter) subscriptionFilter.addEventListener("change", async (event) => {
    state.paperFilters.subscriptionId = event.target.value;
    state.paperFilters.page = 1;
    state.paperDetail = null;
    window.history.replaceState({}, "", "/papers");
    try {
      await refreshPapers();
    } catch (error) {
      showToast("筛选失败", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
    }
  });
  const refreshButton = document.querySelector("[data-refresh-papers]");
  if (refreshButton) refreshButton.addEventListener("click", async () => {
    try {
      await refreshPapers("已同步服务器上的最新匹配结果。");
    } catch (error) {
      showToast("刷新失败", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
    }
  });
  document.querySelectorAll("[data-paper-page]").forEach((button) => {
    button.addEventListener("click", async () => {
      state.paperFilters.page = Number(button.dataset.paperPage);
      state.paperDetail = null;
      window.history.replaceState({}, "", "/papers");
      try {
        await refreshPapers();
        window.scrollTo({ top: 0, behavior: "smooth" });
      } catch (error) {
        showToast("翻页失败", error instanceof ApiError ? translateError(error) : "请稍后重试。", "error");
      }
    });
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

async function loadDashboardData() {
  try {
    const [subscriptions, papers] = await Promise.all([
      apiRequest("/subscriptions?page=1&page_size=1&enabled=true", { method: "GET", auth: true }),
      apiRequest("/papers?page=1&page_size=3", { method: "GET", auth: true }),
    ]);
    state.activeSubscriptionTotal = subscriptions.total;
    state.dashboardPapers = papers;
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) throw error;
    state.activeSubscriptionTotal = null;
    state.dashboardPapers = null;
  }
}

async function renderProtected(pathname) {
  if (!state.token) {
    navigate("/login", true);
    return;
  }

  const active = pathname === "/settings" ? "settings" : pathname === "/subscriptions" ? "subscriptions" : pathname === "/papers" ? "papers" : "dashboard";
  if (!state.profile) {
    app.innerHTML = loadingPage(active);
    setupPage();
    try {
      await loadAccount();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return;
      app.innerHTML = appLayout(`<header class="workspace-header"><div><p class="eyebrow">请稍后重试</p><h1>账户暂时无法读取</h1><p>${escapeHTML(translateError(error))}</p></div></header><button class="button button-primary" type="button" data-retry>重新尝试</button>`, active);
      setupPage();
      const retry = document.querySelector("[data-retry]");
      if (retry) retry.addEventListener("click", route);
      return;
    }
  }

  if (active === "dashboard") await loadDashboardData();
  if (active === "subscriptions") {
    app.innerHTML = loadingPage(active);
    setupPage();
    try {
      await loadSubscriptionWorkspace();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return;
      app.innerHTML = appLayout(`<header class="workspace-header"><div><p class="eyebrow">请稍后重试</p><h1>订阅暂时无法读取</h1><p>${escapeHTML(translateError(error))}</p></div></header><button class="button button-primary" type="button" data-retry>重新尝试</button>`, active);
      setupPage();
      const retry = document.querySelector("[data-retry]");
      if (retry) retry.addEventListener("click", route);
      return;
    }
  }
  if (active === "papers") {
    app.innerHTML = loadingPage(active);
    setupPage();
    try {
      await loadPaperWorkspace();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) return;
      app.innerHTML = appLayout(`<header class="workspace-header"><div><p class="eyebrow">请稍后重试</p><h1>匹配论文暂时无法读取</h1><p>${escapeHTML(translateError(error))}</p></div></header><button class="button button-primary" type="button" data-retry>重新尝试</button>`, active);
      setupPage();
      const retry = document.querySelector("[data-retry]");
      if (retry) retry.addEventListener("click", route);
      return;
    }
  }
  app.innerHTML = active === "settings" ? settingsPage() : active === "subscriptions" ? subscriptionsPage() : active === "papers" ? papersPage() : dashboardPage();
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
      showToast("已安全退出", "你已退出当前账户。", "success");
    });
  });
  setupAuthForm();
  setupSettingsForm();
  setupSubscriptionsPage();
  setupPapersPage();
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
  if (pathname === "/app" || pathname === "/papers" || pathname === "/subscriptions" || pathname === "/settings") {
    renderProtected(pathname);
    return;
  }
  app.innerHTML = errorPage();
  setupPage();
}

window.addEventListener("popstate", route);
route();
