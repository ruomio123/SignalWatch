import { test, expect, type Page } from "@playwright/test";
import { setup, profile, paper, subscription } from "./fixtures";

async function noOverflow(page: Page) {
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
}

test("desktop sidebar and account disclosure support keyboard and logout", async ({
  page,
}) => {
  await setup(page);
  await page.goto("/app");
  const navigation = page.getByRole("button", { name: "切换导航" });
  await expect(navigation).toHaveAttribute("aria-expanded", "true");
  await navigation.click();
  await expect(navigation).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByRole("navigation", { name: "主导航" })).toHaveCount(0);
  await navigation.click();
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible();
  const account = page.getByRole("button", { name: "账户菜单" });
  await account.click();
  await expect(account).toHaveAttribute("aria-expanded", "true");
  await page.keyboard.press("Escape");
  await expect(account).toBeFocused();
  await expect(account).toHaveAttribute("aria-expanded", "false");
  await account.click();
  await page.getByRole("heading", { name: "研究概览" }).click();
  await expect(account).toHaveAttribute("aria-expanded", "false");
  await account.click();
  await page
    .locator("#account-dropdown")
    .getByRole("link", { name: "偏好设置" })
    .click();
  await expect(page).toHaveURL(/\/settings$/);
  await account.click();
  await page
    .locator("#account-dropdown")
    .getByRole("button", { name: "退出登录" })
    .click();
  await expect(page).toHaveURL(/\/login/);
});

test("mobile drawer closes by backdrop, Escape, and navigation", async ({
  page,
}) => {
  await setup(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/app");
  const toggle = page.getByRole("button", { name: "切换导航" });
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await toggle.click();
  await expect(page.getByRole("dialog", { name: "导航菜单" })).toBeVisible();
  await page.mouse.click(360, 400);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(toggle).toBeFocused();
  await toggle.click();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await toggle.click();
  await page
    .getByRole("dialog")
    .getByRole("link", { name: "订阅", exact: true })
    .click();
  await expect(page).toHaveURL(/\/subscriptions$/);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const create = page.getByRole("button", { name: "新建订阅" });
  await create.click();
  const dialog = page.getByRole("dialog", { name: "新建订阅" });
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Shift+Tab");
  expect(
    await dialog.evaluate((el) => el.contains(document.activeElement)),
  ).toBe(true);
  await page.keyboard.press("Escape");
  await expect(create).toBeFocused();
});

test("subscription pagination, filters, save and empty results use real controls", async ({
  page,
}) => {
  await setup(page);
  let renamed = false;
  await page.route("**/api/v2/subscriptions?**", (route) => {
    const params = new URL(route.request().url()).searchParams;
    const second = params.get("page") === "2";
    const empty = params.get("enabled") === "false";
    return route.fulfill({
      json: {
        items: empty
          ? []
          : [
              {
                ...subscription,
                id: second ? 21 : 1,
                name: second
                  ? "第二页订阅"
                  : renamed
                    ? "新的研究方向"
                    : subscription.name,
              },
            ],
        total: empty ? 0 : 21,
        page: second ? 2 : 1,
        page_size: 20,
      },
    });
  });
  await page.route("**/api/v2/subscriptions/1", async (route) => {
    expect(route.request().postDataJSON().name).toBe("新的研究方向");
    expect(route.request().headers()["if-match"]).toBe('"3"');
    renamed = true;
    await route.fulfill({ json: { ...subscription, name: "新的研究方向" } });
  });
  await page.goto("/subscriptions");
  await page.getByRole("button", { name: "下一页" }).click();
  await expect(page.getByRole("heading", { name: "第二页订阅" })).toBeVisible();
  await page.getByRole("button", { name: "上一页" }).click();
  await page.getByRole("button", { name: "编辑 研究订阅" }).click();
  await page.getByLabel("订阅名称").fill("新的研究方向");
  await page.getByRole("button", { name: "保存订阅" }).click();
  await expect(page.getByText("订阅已保存。", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "新的研究方向" }),
  ).toBeVisible();
  await page
    .getByRole("combobox", { name: "状态", exact: true })
    .selectOption("false");
  await expect(
    page.getByRole("heading", { name: "没有符合条件的订阅" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "上一页" })).toBeDisabled();
});

test("preferences show failure then save and refresh account data", async ({
  page,
}) => {
  await setup(page);
  let current = { ...profile };
  let fail = true;
  await page.route("**/api/v2/me", async (route) => {
    if (route.request().method() === "PATCH") {
      if (fail) {
        fail = false;
        await route.fulfill({
          status: 503,
          json: { code: "UNAVAILABLE", message: "down" },
        });
        return;
      }
      current = { ...current, ...route.request().postDataJSON() };
    }
    await route.fulfill({ json: current });
  });
  await page.goto("/settings");
  await page.getByLabel("每日邮件时间").fill("09:30");
  await page.getByRole("button", { name: "保存偏好" }).click();
  await expect(page.getByRole("alert")).toContainText("服务暂时不可用");
  await page.getByRole("button", { name: "保存偏好" }).click();
  await expect(
    page.getByText("阅读偏好已保存。", { exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("每日邮件时间")).toHaveValue("09:30");
  await page.getByRole("link", { name: "概览", exact: true }).click();
  await expect(page.getByText("09:30", { exact: true })).toBeVisible();
});

test("API verification and default deletion refresh reading preferences", async ({
  page,
}) => {
  await setup(page);
  let deleted = false;
  const c = {
    id: "generation-A",
    name: "默认 API",
    configured: true,
    usable: true,
    is_default: true,
    provider: "qwen",
    model: "qwen-plus",
    generation: "generation-A",
    version: 8,
    masked_key: "••••1234",
  };
  await page.route("**/api/v2/me", (r) =>
    r.fulfill({ json: { ...profile, ai_enabled: !deleted } }),
  );
  await page.route("**/api/v2/ai/credentials", (r) =>
    r.fulfill({ json: { items: deleted ? [] : [c] } }),
  );
  await page.route("**/api/v2/ai/credentials/generation-A", (r) => {
    deleted = true;
    return r.fulfill({ status: 204 });
  });
  await page.route("**/api/v2/ai/credentials/generation-A/test", (r) =>
    r.fulfill({ json: c }),
  );
  await page.goto("/api-keys");
  await page.getByRole("button", { name: "验证", exact: true }).click();
  await expect(page.getByText("连接验证成功。", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "删除", exact: true }).click();
  await expect(
    page.getByText(/此条是默认 API，删除后会关闭相关 AI 开关/),
  ).toBeVisible();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await expect(page.getByText("还没有 API Key", { exact: true })).toBeVisible();
  await page
    .getByRole("navigation")
    .getByRole("link", { name: "偏好设置", exact: true })
    .click();
  await expect(page.getByLabel("启用论文 AI 解读")).not.toBeChecked();
});

test("login handles rejected credentials and returns to requested page", async ({
  page,
}) => {
  await setup(page);
  await page.route("**/api/v2/auth/login", async (route) => {
    const valid = route.request().postDataJSON().password === "Password123";
    await route.fulfill({
      status: valid ? 200 : 401,
      json: valid
        ? { access_token: "new-token" }
        : { code: "AUTH_INVALID_CREDENTIALS", message: "invalid" },
    });
  });
  await page.goto("/login?next=%2Fpapers%3Fsubscription_id%3D1");
  await page.getByLabel("邮箱", { exact: true }).fill("reader@example.test");
  await page.getByLabel("密码", { exact: true }).fill("wrong");
  await page.getByRole("button", { name: "显示密码" }).click();
  await expect(page.getByLabel("密码", { exact: true })).toHaveAttribute(
    "type",
    "text",
  );
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("邮箱或密码不正确");
  await page.getByLabel("密码", { exact: true }).fill("Password123");
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page).toHaveURL(/\/papers\?subscription_id=1$/);
});

test("registration validates confirmation and preserves return target", async ({
  page,
}) => {
  await setup(page);
  let registrations = 0;
  await page.route("**/api/v2/auth/register", (route) => {
    registrations++;
    return route.fulfill({ status: 201, json: {} });
  });
  await page.route("**/api/v2/auth/login", (route) =>
    route.fulfill({ json: { access_token: "registered-token" } }),
  );
  await page.goto("/login?next=%2Fsubscriptions");
  await page.getByRole("link", { name: "创建新账户" }).click();
  await expect(
    page.getByRole("heading", { name: "创建账户", exact: true }),
  ).toBeVisible();
  await page.getByLabel("邮箱", { exact: true }).fill("reader@example.test");
  await page.getByLabel("密码", { exact: true }).fill("Password123");
  await page.getByLabel("确认密码", { exact: true }).fill("mismatch");
  await page.getByRole("button", { name: "注册并登录" }).click();
  await expect(page.getByRole("alert")).toContainText("两次输入须一致");
  expect(registrations).toBe(0);
  await page.getByLabel("确认密码", { exact: true }).fill("Password123");
  await page.getByRole("button", { name: "注册并登录" }).click();
  await expect(page).toHaveURL(/\/subscriptions$/);
  expect(registrations).toBe(1);
});

for (const width of [1440, 768, 390, 320]) {
  test(`visual review and long content at ${width}px`, async ({
    page,
  }, info) => {
    test.setTimeout(60000);
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(error.message));
    await setup(page);
    await page.setViewportSize({ width, height: 960 });
    const longPaper = {
      ...paper,
      title:
        "Reliable Planning and Long-Horizon Reasoning in Language Model Agents: A Comprehensive Study of Evaluation and Generalization",
      abstract:
        "We investigate how research agents plan and revise their decisions across diverse environments. ".repeat(
          10,
        ),
      authors: ["Ada Lovelace", "Alan Turing", "Grace Hopper"],
      matches: [
        {
          subscription_id: 1,
          subscription_name: "智能体与推理",
          subscription_active: true,
        },
      ],
    };
    await page.route("**/api/v2/me", (route) =>
      route.fulfill({
        json: {
          ...profile,
          email:
            "a-very-long-research-account-name@department.university.example.test",
        },
      }),
    );
    await page.route("**/api/v2/subscriptions?**", (route) =>
      route.fulfill({
        json: {
          items: [
            {
              ...subscription,
              name: "智能体与推理",
              objective: "关注长期规划、多智能体协作和可靠评估",
              rules: {
                category: "cs.AI",
                keywords: [
                  "agent",
                  "reasoning",
                  "long-horizon planning",
                  "multi-agent collaboration",
                ],
              },
            },
            {
              ...subscription,
              id: 2,
              name: "自然语言处理",
              enabled: false,
              rules: {
                category: "cs.CL",
                keywords: ["retrieval augmented generation"],
              },
              backfill: { state: "cancelled", processed: 120, matched: 15 },
            },
            {
              ...subscription,
              id: 3,
              name: "模型评估与安全",
              backfill: { state: "failed", processed: 800, matched: 40 },
            },
          ],
          total: 3,
          page: 1,
          page_size: 20,
        },
      }),
    );
    await page.route("**/api/v2/papers?**", (route) =>
      route.fulfill({
        json: {
          items: [
            longPaper,
            {
              ...paper,
              id: 2,
              title: "Retrieval-Augmented Generation for Scientific Literature",
            },
          ],
          total: 2,
          page: 1,
          page_size: 20,
        },
      }),
    );
    await page.route("**/api/v2/papers/1", (route) =>
      route.fulfill({ json: longPaper }),
    );
    await page.route("**/api/v2/papers/1/ai-summary**", (route) =>
      route.fulfill({
        json: {
          items: [
            {
              state: "ready",
              language: "zh",
              content: {
                summary: "本文研究语言模型智能体的长期规划与推理能力。".repeat(
                  12,
                ),
                contributions: [
                  "建立可复现的评估方法",
                  "分析长期任务中的错误传播",
                ],
                method: "结合多阶段任务与对照实验。".repeat(12),
                applications: [{ text: "科研工作流辅助", inferred: true }],
                limitations: "当前结果限于文中实验设置。",
                evidence: [{ field: "abstract", quote: longPaper.abstract }],
              },
            },
          ],
        },
      }),
    );
    const screenshots = async (name: string) => {
      await noOverflow(page);
      await page.screenshot({
        path: info.outputPath(`${name}-${width}.png`),
        fullPage: true,
        animations: "disabled",
      });
    };
    for (const [path, heading, name] of [
      ["/", "关注研究方向", "home"],
      ["/login", "欢迎回来", "login"],
      ["/register", "创建账户", "register"],
      ["/app", "研究概览", "dashboard"],
      ["/subscriptions", "订阅", "subscriptions"],
      ["/papers", "匹配论文", "papers"],
      ["/settings", "偏好设置", "settings"],
      ["/api-keys", "API 管理", "api-keys"],
    ]) {
      await page.goto(path);
      await expect(
        page.getByRole("heading", { name: new RegExp(heading) }).first(),
      ).toBeVisible();
      await expect(page.getByText("正在读取…", { exact: true })).toHaveCount(0);
      await expect(page.getByRole("alert")).toHaveCount(0);
      await screenshots(name);
    }
    await page.route("**/api/v2/ai/credentials", (route) =>
      route.fulfill({
        json: {
          items: [
            {
              name: "日常研究",
              configured: true,
              usable: true,
              provider: "qwen",
              model: "qwen-plus",
              generation: "review-generation",
              version: 4,
              masked_key: "••••1234",
            },
          ],
        },
      }),
    );
    await page.route("**/api/v2/ai/usage", (route) =>
      route.fulfill({
        json: {
          items: [
            {
              day: "2026-09-11",
              feature: "paper_summary",
              calls: 24,
              succeeded: 23,
              failed: 1,
              input_tokens: 42000,
              output_tokens: 9600,
              usage_missing: 0,
            },
          ],
        },
      }),
    );
    await page.reload();
    await expect(
      page.getByRole("button", { name: "验证", exact: true }),
    ).toBeVisible();
    await screenshots("api-keys-configured");
    await page.goto("/subscriptions");
    await page.getByRole("button", { name: "编辑 智能体与推理" }).click();
    await screenshots("subscription-editor");
    await page.keyboard.press("Escape");
    await page.goto("/papers?paper_id=1");
    await expect(page.getByText("已生成", { exact: true })).toBeVisible();
    const dialog = page.getByRole("dialog", { name: "论文详情" });
    expect(
      await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth),
    ).toBe(true);
    await screenshots("paper-detail");
    await page
      .getByRole("heading", { name: "AI 论文解读" })
      .scrollIntoViewIfNeeded();
    await screenshots("paper-ai");
    expect(pageErrors).toEqual([]);
  });
}
