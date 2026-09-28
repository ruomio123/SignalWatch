import { test as base, expect, type Route } from "@playwright/test";

export { expect };
export const testOrigin = "http://127.0.0.1:4173";
export const paper = {
  id: 1,
  title: "Agent Planning",
  abstract: "A study of agent planning.",
  authors: ["Ada"],
  categories: ["cs.AI"],
  published_at: "2026-09-10T00:00:00Z",
  arxiv_url: "https://arxiv.org/abs/1706.03762v1",
  pdf_url: "https://arxiv.org/pdf/1706.03762v1",
  matches: [],
};
export const credential = {
  configured: true,
  usable: true,
  is_default: true,
  provider: "glm",
  model: "glm-4.7-flash",
  id: "fixture-credential",
  name: "测试模型",
  generation: "fixture-credential",
  version: 1,
  masked_key: "••••1234",
};
export const conversation = {
  id: "conversation-one",
  kind: "paper",
  paper_id: 1,
  title: "Agent Planning",
  paper_report_ready: false,
};
export const conversationPath = "/agent/conversations/conversation-one";
export const assistantURL =
  "/papers?paper_id=1&assistant=paper&conversation=conversation-one";

export function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

type Handler = (route: Route, url: URL) => Promise<void> | void;
export class AgentAPI {
  private handlers = new Map<string, Handler>();
  readonly unexpected: string[] = [];
  readonly requests: { method: string; path: string; body: unknown }[] = [];

  on(method: string, path: string, handler: Handler) {
    this.handlers.set(`${method.toUpperCase()} /api/v2${path}`, handler);
  }

  json(method: string, path: string, value: unknown, status = 200) {
    this.on(method, path, (route) => route.fulfill({ status, json: value }));
  }

  requestsFor(method: string, path: string) {
    return this.requests.filter(
      (request) =>
        request.method === method.toUpperCase() &&
        new URL(request.path, testOrigin).pathname === `/api/v2${path}`,
    );
  }

  async handle(route: Route) {
    const request = route.request();
    const url = new URL(request.url());
    if (url.origin !== testOrigin) {
      this.unexpected.push(`external request: ${request.method()} ${url.href}`);
      await route.abort("blockedbyclient");
      return;
    }
    if (!/^\/api(?:\/|$)/.test(url.pathname)) {
      await route.continue();
      return;
    }
    const key = `${request.method()} ${url.pathname}`;
    this.requests.push({
      method: request.method(),
      path: url.pathname + url.search,
      body: request.postData() ? request.postDataJSON() : undefined,
    });
    const handler = this.handlers.get(key);
    if (!handler) {
      this.unexpected.push(`${key}${url.search}`);
      await route.fulfill({
        status: 501,
        json: { code: "UNMOCKED_API", message: `Missing test mock: ${key}` },
      });
      return;
    }
    await handler(route, url);
  }
}

export const test = base.extend<{ api: AgentAPI }>({
  api: async ({ context }, use) => {
    const api = new AgentAPI();
    await context.addInitScript(() => {
      localStorage.setItem(
        "signalwatch.session",
        JSON.stringify({
          id: "agent-test-session",
          accessToken: "fixture-token",
        }),
      );
    });
    api.json("GET", "/me", {
      id: 1,
      email: "reader@example.test",
      timezone: "Asia/Shanghai",
      digest_time: "08:00",
      max_items_per_digest: 10,
      ai_enabled: true,
      ai_language: "zh",
    });
    api.json("GET", "/papers", {
      items: [paper],
      total: 1,
      page: 1,
      page_size: 20,
    });
    api.json("GET", "/papers/1", paper);
    api.json("GET", "/subscriptions", {
      items: [],
      total: 0,
      page: 1,
      page_size: 100,
    });
    api.json("GET", "/ai/credentials", { items: [credential] });
    api.json("GET", "/ai/providers", {
      items: [
        {
          id: "glm",
          name: "GLM",
          models: [{ id: "glm-4.7-flash", name: "Flash" }],
        },
      ],
    });
    api.json("GET", "/agent/conversations", {
      items: [conversation],
      page: 1,
      has_more: false,
      next_page: 0,
    });
    api.json("GET", conversationPath, conversation);
    api.json("GET", `${conversationPath}/paper-report`, {
      report: null,
      matches_current_paper: false,
    });
    api.json("GET", `${conversationPath}/messages`, {
      items: [],
      next_before: 0,
    });
    await context.route("**/*", (route) => api.handle(route));
    await use(api);
    expect(
      api.unexpected,
      "Every API and external request must be explicitly mocked",
    ).toEqual([]);
  },
});
