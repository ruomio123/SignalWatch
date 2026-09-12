import { type Page } from "@playwright/test";
export const profile = {
  id: 1,
  email: "reader@example.test",
  timezone: "Asia/Shanghai",
  digest_time: "08:00",
  max_items_per_digest: 10,
  ai_enabled: true,
  ai_language: "zh",
};
export const source = {
  id: 1,
  source_key: "arxiv",
  name: "arXiv",
  kind: "arxiv",
  rule_types: ["category", "include_keyword"],
  allowed_categories: ["cs.AI", "cs.CL"],
};
export const subscription = {
  id: 1,
  name: "研究订阅",
  objective: null,
  source,
  enabled: true,
  version: 3,
  max_items_per_digest: 10,
  digest_ai_enabled: true,
  digest_ai_language: "en",
  rules: { category: "cs.AI", keywords: ["agent"] },
  backfill: { state: "complete", processed: 2, matched: 1 },
};
export const paper = {
  id: 1,
  title: "Agent Planning",
  abstract: "A study of agent planning.",
  authors: ["Ada"],
  categories: ["cs.AI"],
  published_at: "2026-09-10",
  arxiv_url: "https://arxiv.org/abs/1234",
  pdf_url: "https://arxiv.org/pdf/1234",
  matches: [],
};
export async function setup(page: Page) {
  await page.addInitScript(() =>
    localStorage.setItem("signalwatch.access_token", "test-token"),
  );
  await page.route("**/api/v2/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v2/auth/refresh") {
      await route.fulfill({
        status: 401,
        json: { code: "UNAUTHORIZED", message: "session expired" },
      });
      return;
    }
    if (path === "/api/v2/auth/logout") {
      await route.fulfill({ status: 204 });
      return;
    }
    const values: Record<string, unknown> = {
      "/api/v2/me": profile,
      "/api/v2/sources": [source],
      "/api/v2/subscriptions": {
        items: [subscription],
        total: 1,
        page: 1,
        page_size: 20,
      },
      "/api/v2/papers": { items: [paper], total: 1, page: 1, page_size: 20 },
      "/api/v2/papers/1": paper,
      "/api/v2/ai/configuration": { configured: false, usable: false },
      "/api/v2/ai/providers": {
        items: [
          {
            id: "qwen",
            name: "Qwen",
            models: [{ id: "qwen-plus", name: "Plus" }],
          },
        ],
      },
      "/api/v2/ai/usage": { items: [] },
    };
    await route.fulfill({ status: 200, json: values[path] ?? { items: [] } });
  });
}
