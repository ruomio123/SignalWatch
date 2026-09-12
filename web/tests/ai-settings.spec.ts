import { test, expect, type Page } from "@playwright/test";
import { setup } from "./fixtures";
const current = {
  id: "first",
  name: "日常研究",
  configured: true,
  usable: true,
  is_default: true,
  provider: "glm",
  model: "glm-5.2",
  generation: "first",
  version: 1,
  masked_key: "••••1234",
  created_at: "2026-09-11T08:00:00Z",
  last_used_at: "2026-09-11T09:00:00Z",
};
async function configure(page: Page) {
  await setup(page);
  const state = {
    items: [current],
    writes: [] as {
      path: string;
      method: string;
      body: Record<string, unknown>;
      etag?: string;
    }[],
  };
  await page.route("**/api/v2/ai/providers", (r) =>
    r.fulfill({
      json: {
        items: [
          {
            id: "glm",
            name: "智谱 GLM",
            models: [
              { id: "glm-5.2", name: "GLM 5.2" },
              { id: "glm-4.7-flash", name: "GLM 4.7 Flash" },
            ],
          },
          {
            id: "qwen",
            name: "Qwen",
            models: [{ id: "qwen3.8-flash", name: "Qwen Flash" }],
          },
        ],
      },
    }),
  );
  await page.route("**/api/v2/ai/credentials**", async (r) => {
    const path = new URL(r.request().url()).pathname;
    const method = r.request().method();
    const body = (r.request().postDataJSON() ?? {}) as Record<string, unknown>;
    const id = path.split("/")[5];
    if (method !== "GET")
      state.writes.push({
        path,
        method,
        body,
        etag: r.request().headers()["if-match"],
      });
    if (method === "POST" && !id) {
      const c = {
        ...current,
        id: "second",
        generation: "second",
        name: String(body.name),
        provider: String(body.provider),
        model: String(body.model),
        is_default: false,
        masked_key: "••••5678",
      };
      state.items.push(c);
      await r.fulfill({ status: 201, json: c });
    } else if (method === "DELETE") {
      state.items = state.items.filter((c) => c.id !== id);
      await r.fulfill({ status: 204 });
    } else if (method === "PUT") {
      state.items = state.items.map((c) =>
        c.id === id
          ? {
              ...c,
              name: String(body.name),
              model: String(body.model),
              version: c.version + 1,
            }
          : c,
      );
      await r.fulfill({ json: state.items.find((c) => c.id === id) });
    } else
      await r.fulfill({
        json: id
          ? state.items.find((c) => c.id === id)
          : { items: state.items },
      });
  });
  await page.route("**/api/v2/ai/default-selection", async (r) => {
    const b = r.request().postDataJSON();
    state.items = state.items.map((c) => ({
      ...c,
      is_default: c.id === b.generation,
    }));
    await r.fulfill({ json: state.items.find((c) => c.is_default) });
  });
  return state;
}

test("preferences and API management are separate, with a button to open creation", async ({
  page,
}) => {
  await configure(page);
  await page.goto("/settings");
  await expect(page.getByLabel("每日邮件时间")).toBeVisible();
  await expect(page.getByLabel("API Key", { exact: true })).toHaveCount(0);
  await page
    .getByRole("navigation")
    .getByRole("link", { name: "API 管理" })
    .click();
  await expect(page).toHaveURL(/\/api-keys$/);
  await expect(page.getByRole("row", { name: /日常研究/ })).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.getByRole("button", { name: "新建 API Key" }).click();
  await expect(
    page.getByRole("dialog", { name: "新建 API Key" }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "新建 API Key" }),
  ).toBeFocused();
});

test("same-provider keys can be added, edited, selected and deleted independently", async ({
  page,
}) => {
  const state = await configure(page);
  await page.goto("/api-keys");
  await page.getByRole("button", { name: "新建 API Key" }).click();
  await page.getByLabel("名称", { exact: true }).fill("GLM 备用");
  await page.getByLabel("API Key", { exact: true }).fill("test-key-5678");
  await page.getByRole("button", { name: "验证并保存" }).click();
  await expect(page.getByRole("row", { name: /GLM 备用/ })).toBeVisible();
  expect(state.items).toHaveLength(2);
  expect(state.writes[0]).toMatchObject({
    method: "POST",
    path: "/api/v2/ai/credentials",
    body: { name: "GLM 备用", provider: "glm" },
  });
  expect(state.writes[0].etag).toBeUndefined();
  const row = page.getByRole("row", { name: /日常研究/ });
  await row.getByRole("button", { name: "编辑", exact: true }).click();
  await expect(
    page.getByRole("combobox", { name: "供应商", exact: true }),
  ).toBeDisabled();
  await page.getByLabel("名称", { exact: true }).fill("研究主力");
  await page
    .getByRole("combobox", { name: "默认模型", exact: true })
    .selectOption("glm-4.7-flash");
  await page.getByRole("button", { name: "验证并保存" }).click();
  await expect(page.getByRole("row", { name: /研究主力/ })).toBeVisible();
  expect(state.writes[1]).toEqual({
    method: "PUT",
    path: "/api/v2/ai/credentials/first",
    body: { name: "研究主力", model: "glm-4.7-flash" },
    etag: '"first:1"',
  });
  await page
    .getByRole("row", { name: /GLM 备用/ })
    .getByRole("button", { name: "设为默认" })
    .click();
  await expect(
    page.getByText("邮件与摘要默认使用：GLM 备用", { exact: false }),
  ).toBeVisible();
  await page
    .getByRole("row", { name: /研究主力/ })
    .getByRole("button", { name: "删除", exact: true })
    .click();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await expect(page.getByRole("row", { name: /研究主力/ })).toHaveCount(0);
  await expect(page.getByRole("row", { name: /GLM 备用/ })).toBeVisible();
  expect(state.writes[2]).toMatchObject({
    method: "DELETE",
    path: "/api/v2/ai/credentials/first",
    etag: '"first:2"',
  });
});

test("validation failures keep form fields, clear secrets, and never retry automatically", async ({
  page,
}) => {
  await configure(page);
  let calls = 0;
  await page.route("**/api/v2/ai/credentials", async (r) => {
    if (r.request().method() === "GET") return r.fallback();
    calls++;
    await r.fulfill({
      status: 429,
      json: {
        code: "AI_PROVIDER_RATE_LIMITED",
        call_id: "call-1",
        retry_after_seconds: 1,
      },
    });
  });
  await page.goto("/api-keys");
  await page.getByRole("button", { name: "新建 API Key" }).click();
  await page.getByLabel("名称", { exact: true }).fill("工作用");
  await page
    .getByRole("combobox", { name: "供应商", exact: true })
    .selectOption("qwen");
  await expect(page.getByRole("button", { name: "验证并保存" })).toBeDisabled();
  await page.getByLabel("API Key", { exact: true }).fill("test-key-1234");
  await page.getByRole("button", { name: "验证并保存" }).click();
  await expect(
    page.getByText("供应商正在限流，请稍后手动重试。", { exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("名称", { exact: true })).toHaveValue("工作用");
  await expect(page.getByLabel("API Key", { exact: true })).toHaveValue("");
  await expect(page.getByRole("button", { name: "验证并保存" })).toBeDisabled();
  expect(calls).toBe(1);
});

test("stale edits preserve the draft until explicit reloading", async ({
  page,
}) => {
  await configure(page);
  let reads = 0;
  await page.route("**/api/v2/ai/credentials/first", async (r) => {
    if (r.request().method() === "PUT")
      return r.fulfill({
        status: 409,
        json: { code: "AI_CONFIGURATION_VERSION_CONFLICT" },
      });
    reads++;
    return r.fulfill({
      json: { ...current, name: "另一设备修改", version: 2 },
    });
  });
  await page.goto("/api-keys");
  await page.getByRole("button", { name: "编辑", exact: true }).click();
  await page.getByLabel("名称", { exact: true }).fill("我的修改");
  await page.getByRole("button", { name: "验证并保存" }).click();
  await expect(page.getByLabel("名称", { exact: true })).toHaveValue(
    "我的修改",
  );
  await page.getByRole("button", { name: "重新载入条目" }).click();
  await expect(page.getByLabel("名称", { exact: true })).toHaveValue(
    "另一设备修改",
  );
  expect(reads).toBe(1);
});

test("a late validation failure cannot replace another route", async ({
  page,
}) => {
  await configure(page);
  let release!: () => void;
  const pending = new Promise<void>((resolve) => (release = resolve));
  await page.route("**/api/v2/ai/credentials/first/test", async (r) => {
    await pending;
    await r
      .fulfill({ status: 504, json: { code: "AI_PROVIDER_TIMEOUT" } })
      .catch(() => {});
  });
  await page.goto("/api-keys");
  await page.getByRole("button", { name: "验证", exact: true }).click();
  await page.getByRole("link", { name: "匹配论文", exact: true }).click();
  release();
  await expect(
    page.getByRole("heading", { name: "匹配论文", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
});

for (const width of [1440, 390])
  test(`API list and editor layout at ${width}px`, async ({ page }, info) => {
    const state = await configure(page);
    state.items.push({
      ...current,
      id: "second",
      generation: "second",
      name: "GLM 实验专用 API",
      is_default: false,
      masked_key: "••••5678",
    });
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/api-keys");
    await expect(page.getByRole("row", { name: /GLM 实验专用/ })).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath(`api-list-${width}.png`),
      fullPage: true,
    });
    await page.getByRole("button", { name: "新建 API Key" }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    expect(
      await page
        .getByRole("dialog")
        .evaluate((el) => el.scrollWidth <= el.clientWidth),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath(`api-editor-${width}.png`),
      fullPage: true,
    });
  });
test("call history filters and pagination query without generating", async ({
  page,
}) => {
  await configure(page);
  const queries: string[] = [];
  let writes = 0;
  await page.route("**/api/v2/ai/calls?*", async (r) => {
    const url = new URL(r.request().url());
    queries.push(url.search);
    await r.fulfill({
      json: {
        total: 21,
        items: [
          {
            id: "diagnostic-" + "x".repeat(45),
            feature: "paper",
            provider: "glm",
            model: "glm-4.7-flash",
            status: "unknown",
            failure_code: "result_unknown",
            created_at: "2026-09-11T00:00:00Z",
            duration_ms: 30000,
            usage_known: false,
          },
        ],
      },
    });
  });
  page.on("request", (r) => {
    if (r.method() !== "GET" && r.url().includes("/ai/")) writes++;
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/api-keys?tab=activity");
  await expect(
    page.getByText("调用结果未知，系统不会自动再次调用。", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "下一页", exact: true }).click();
  await expect(page.getByText("第 2 页", { exact: true })).toBeVisible();
  await page
    .getByRole("combobox", { name: "调用结果", exact: true })
    .selectOption("unknown");
  await expect(page.getByText("第 1 页", { exact: true })).toBeVisible();
  await expect.poll(() => queries.at(-1)).toContain("status=unknown");
  await page.getByRole("button", { name: "重新查询", exact: true }).click();
  expect(writes).toBe(0);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});
