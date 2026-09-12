import { test, expect } from "@playwright/test";
import { setup, paper } from "./fixtures";
test("late page response cannot replace the active route", async ({ page }) => {
  await setup(page);
  let release!: () => void;
  const wait = new Promise<void>((r) => (release = r));
  await page.route("**/api/v2/papers?**", async (route) => {
    await wait;
    await route.fulfill({ json: { items: [paper], total: 1 } }).catch(() => {});
  });
  await page.goto("/papers");
  await page.getByRole("link", { name: "订阅", exact: true }).first().click();
  await expect(
    page.getByRole("heading", { name: "订阅", exact: true }),
  ).toBeVisible();
  release();
  await expect(
    page.getByRole("heading", { name: "研究订阅", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "匹配论文", exact: true }),
  ).toHaveCount(0);
});
test("AI fields survive editing; stale version offers reload", async ({
  page,
}) => {
  await setup(page);
  await page.route("**/api/v2/subscriptions/1", async (route) => {
    expect(route.request().headers()["if-match"]).toBe('"3"');
    expect(route.request().postDataJSON()).toMatchObject({
      digest_ai_enabled: true,
      digest_ai_language: "en",
      rules: { category: "cs.AI", keywords: ["agent"] },
    });
    await route.fulfill({
      status: 409,
      json: { code: "SUBSCRIPTION_VERSION_CONFLICT", message: "conflict" },
    });
  });
  await page.goto("/subscriptions");
  await page.getByRole("button", { name: "编辑 研究订阅" }).click();
  await expect(page.getByLabel("每日邮件 AI 导读")).toBeChecked();
  await expect(page.getByLabel("AI 导读语言")).toHaveValue("en");
  await page.getByRole("button", { name: "保存订阅" }).click();
  await expect(page.getByRole("alert")).toContainText("订阅已被修改");
  await page.getByRole("button", { name: "重新载入" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});
test("401 expires session and redirects to login", async ({ page }) => {
  await setup(page);
  await page.route("**/api/v2/me", (route) =>
    route.fulfill({
      status: 401,
      json: { code: "UNAUTHORIZED", message: "expired" },
    }),
  );
  await page.goto("/settings");
  await expect(page).toHaveURL(/\/login\?next=/);
  expect(
    await page.evaluate(() => localStorage.getItem("signalwatch.access_token")),
  ).toBeNull();
});
test("503 exposes retry and keeps navigation usable", async ({ page }) => {
  await setup(page);
  await page.route("**/api/v2/subscriptions?**", (route) =>
    route.fulfill({
      status: 503,
      json: { code: "UNAVAILABLE", message: "down" },
    }),
  );
  await page.goto("/subscriptions");
  await expect(page.getByRole("alert")).toContainText("服务暂时不可用");
  await page
    .getByRole("link", { name: "偏好设置", exact: true })
    .first()
    .click();
  await expect(page.getByRole("heading", { name: "阅读偏好" })).toBeVisible();
});
test("paper detail is inline and never requests the legacy summary", async ({
  page,
}) => {
  await setup(page);
  let calls = 0;
  page.on("request", (r) => {
    if (r.url().includes("/ai-summary")) calls++;
  });
  await page.goto("/papers?paper_id=1");
  await expect(page.getByRole("heading", { name: paper.title })).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByText("AI 论文解读", { exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "返回论文列表" }).click();
  await expect(page.getByRole("heading", { name: "匹配论文" })).toBeVisible();
  expect(calls).toBe(0);
});
test("delete API entry uses its lifecycle ETag and clears the list", async ({
  page,
}) => {
  await setup(page);
  let deleted = false;
  const c = {
    id: "lifecycle-A",
    name: "日常 API",
    configured: true,
    usable: true,
    provider: "qwen",
    model: "qwen-plus",
    masked_key: "••••1234",
    generation: "lifecycle-A",
    version: 4,
  };
  await page.route("**/api/v2/ai/credentials", (r) =>
    r.fulfill({ json: { items: deleted ? [] : [c] } }),
  );
  await page.route("**/api/v2/ai/credentials/lifecycle-A", (r) => {
    expect(r.request().headers()["if-match"]).toBe('"lifecycle-A:4"');
    deleted = true;
    return r.fulfill({ status: 204 });
  });
  await page.goto("/api-keys");
  await page.getByRole("button", { name: "删除", exact: true }).click();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await expect(page.getByText("还没有 API Key", { exact: true })).toBeVisible();
  expect(deleted).toBe(true);
});

test("mobile navigation and modal keyboard lifecycle", async ({
  page,
}, info) => {
  await setup(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/subscriptions");
  await expect(
    page.getByRole("heading", { name: "研究订阅", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: info.outputPath("subscriptions-mobile.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "新建订阅" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.getByRole("button", { name: "切换导航" }).click();
  await page
    .getByRole("dialog", { name: "导航菜单" })
    .getByRole("link", { name: "偏好设置" })
    .click();
  await expect(page.getByRole("heading", { name: "阅读偏好" })).toBeVisible();
});
