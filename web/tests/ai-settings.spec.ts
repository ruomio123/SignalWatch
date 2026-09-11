import { test, expect, type Page } from "@playwright/test";
import { setup } from "./fixtures";
const current = {
  configured: true,
  usable: true,
  provider: "glm",
  model: "glm-5.2",
  generation: "first",
  version: 1,
  masked_key: "••••1234",
};
async function configure(page: Page) {
  await setup(page);
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
  await page.route("**/api/v2/ai/configuration", (r) =>
    r.fulfill({ json: current }),
  );
}
test("model edits validate and save once with the existing key", async ({
  page,
}) => {
  await configure(page);
  let config = current;
  let puts = 0;
  await page.route("**/api/v2/ai/configuration", async (r) => {
    if (r.request().method() === "PUT") {
      puts++;
      expect(r.request().postDataJSON()).toEqual({
        provider: "glm",
        model: "glm-4.7-flash",
      });
      expect(r.request().headers()["if-match"]).toBe('"first:1"');
      config = { ...current, model: "glm-4.7-flash", version: 2 };
    }
    await r.fulfill({ json: config });
  });
  await page.goto("/settings");
  await expect(
    page.getByRole("button", { name: "测试当前配置" }),
  ).toBeVisible();
  await page
    .getByRole("combobox", { name: "模型", exact: true })
    .selectOption("glm-4.7-flash");
  await expect(page.getByRole("button", { name: "测试当前配置" })).toHaveCount(
    0,
  );
  await page.getByRole("button", { name: "验证并保存" }).click();
  await expect(
    page.getByText("AI 配置已验证并保存。", { exact: true }),
  ).toBeVisible();
  expect(puts).toBe(1);
  await expect(
    page.getByText("glm / glm-4.7-flash", { exact: true }),
  ).toBeVisible();
});
test("failed validation preserves draft, refreshes usage and requires manual retry", async ({
  page,
}) => {
  await configure(page);
  let puts = 0,
    reads = 0;
  await page.route("**/api/v2/ai/usage", async (r) => {
    reads++;
    await r.fulfill({
      json: {
        items: [],
        today: [
          {
            feature: "config_test",
            calls: puts,
            daily_limit: 0,
            min_interval_seconds: 10,
          },
        ],
      },
    });
  });
  await page.route("**/api/v2/ai/configuration", async (r) => {
    if (r.request().method() === "PUT") {
      puts++;
      await r.fulfill({
        status: 429,
        json: {
          code: "AI_PROVIDER_RATE_LIMITED",
          request_id: "request-1",
          call_id: "call-1",
          retry_after_seconds: 1,
        },
      });
    } else await r.fulfill({ json: current });
  });
  await page.goto("/settings");
  await page
    .getByRole("combobox", { name: "模型", exact: true })
    .selectOption("glm-4.7-flash");
  await page.getByRole("button", { name: "验证并保存" }).click();
  await expect(
    page.getByText("供应商正在限流，请稍后手动重试。", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("诊断编号：call-1")).toBeVisible();
  await expect(page.getByText("glm / glm-5.2", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("combobox", { name: "模型", exact: true }),
  ).toHaveValue("glm-4.7-flash");
  await expect(page.getByRole("button", { name: "验证并保存" })).toBeEnabled();
  expect(puts).toBe(1);
  expect(reads).toBeGreaterThan(1);
});
test("new provider requires a key and clears it after failed validation", async ({
  page,
}) => {
  await configure(page);
  await page.route("**/api/v2/ai/configuration", async (r) => {
    if (r.request().method() === "PUT")
      await r.fulfill({
        status: 422,
        json: { code: "AI_MODEL_ACCESS_DENIED", request_id: "request-2" },
      });
    else await r.fulfill({ json: current });
  });
  await page.goto("/settings");
  await page
    .getByRole("combobox", { name: "供应商", exact: true })
    .selectOption("qwen");
  await expect(page.getByRole("button", { name: "验证并保存" })).toBeDisabled();
  await page.getByLabel("API Key", { exact: true }).fill("test-secret-1234");
  await page.getByRole("button", { name: "验证并保存" }).click();
  await expect(
    page.getByText("供应商拒绝访问此模型，请检查模型权限。", { exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("API Key", { exact: true })).toHaveValue("");
});
test("a late validation failure does not replace a new route", async ({
  page,
}) => {
  await configure(page);
  let release!: () => void;
  const pending = new Promise<void>((r) => (release = r));
  await page.route("**/api/v2/ai/configuration/test", async (r) => {
    await pending;
    await r
      .fulfill({
        status: 504,
        json: { code: "AI_PROVIDER_TIMEOUT", request_id: "late" },
      })
      .catch(() => {});
  });
  await page.goto("/settings");
  await page.getByRole("button", { name: "测试当前配置" }).click();
  await page.getByRole("link", { name: "匹配论文", exact: true }).click();
  release();
  await expect(
    page.getByRole("heading", { name: "匹配论文", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("模型在 30 秒内未完成响应，调用结果未知。"),
  ).toHaveCount(0);
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
  await page.goto("/settings");
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
test("configuration conflict preserves the draft and offers a query", async ({
  page,
}) => {
  await configure(page);
  await page.route("**/api/v2/ai/configuration", (r) =>
    r.request().method() === "PUT"
      ? r.fulfill({
          status: 409,
          json: {
            code: "AI_CONFIGURATION_VERSION_CONFLICT",
            request_id: "conflict",
          },
        })
      : r.fulfill({ json: current }),
  );
  await page.goto("/settings");
  await page
    .getByRole("combobox", { name: "模型", exact: true })
    .selectOption("glm-4.7-flash");
  await page.getByRole("button", { name: "验证并保存" }).click();
  await expect(
    page.getByText("AI 配置已变化，请重新载入后重试。", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("combobox", { name: "模型", exact: true }),
  ).toHaveValue("glm-4.7-flash");
  await page.getByRole("button", { name: "重新载入", exact: true }).click();
  await expect(
    page.getByRole("combobox", { name: "模型", exact: true }),
  ).toHaveValue("glm-5.2");
});
test("a rejected saved credential is visibly marked for inspection", async ({
  page,
}) => {
  await configure(page);
  await page.route("**/api/v2/ai/configuration/test", (r) =>
    r.fulfill({
      status: 422,
      json: {
        code: "AI_CONFIGURATION_INVALID",
        request_id: "invalid",
        call_id: "credential-check",
      },
    }),
  );
  await page.goto("/settings");
  await page.getByRole("button", { name: "测试当前配置" }).click();
  await expect(page.getByText("需要检查", { exact: true })).toBeVisible();
  await expect(page.getByText("诊断编号：credential-check")).toBeVisible();
});
