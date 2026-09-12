import { test, expect, type Page } from "@playwright/test";
import { profile, setup, subscription } from "./fixtures";

const conversationID = "layout-conversation";
const url = `/subscriptions?assistant=subscription&conversation=${conversationID}`;
const payload = {
  ...subscription,
  source_id: 1,
  name: "RAG 与智能体研究",
  objective: "关注检索增强生成、工具调用和智能体评测的新论文。",
  max_items_per_digest: 5,
  digest_ai_language: "zh",
  rules: {
    category: "cs.CL",
    keywords: [
      "retrieval augmented generation",
      "tool use",
      "agent evaluation",
    ],
  },
};

async function assistant(
  page: Page,
  options: { expires?: string; latest?: boolean } = {},
) {
  await setup(page);
  let confirmed = false;
  let confirmations = 0;
  let version = 1;
  let value = payload;
  let sent = "";
  let listReads = 0;
  const draft = () => ({
    id: "draft-one",
    version,
    payload: value,
    expires_at: options.expires ?? "2099-01-01T00:00:00Z",
    ...(confirmed ? { subscription_id: 25 } : {}),
  });
  await page.route("**/api/v2/subscriptions?**", (r) => {
    listReads++;
    return r.fulfill({
      json: { items: [subscription], total: 1, page: 1, page_size: 20 },
    });
  });
  await page.route("**/api/v2/ai/credentials", (r) =>
    r.fulfill({
      json: {
        items: [
          {
            id: "key-one",
            provider: "qwen",
            model: "qwen-plus",
            name: "研究 API",
            usable: true,
            is_default: true,
            masked_key: "••••1234",
          },
        ],
      },
    }),
  );
  const conversation = {
    id: conversationID,
    title: "研究方向",
    latest_draft_id: options.latest === false ? "newer-draft" : "draft-one",
  };
  await page.route("**/api/v2/agent/conversations?**", (r) =>
    r.fulfill({ json: { items: [conversation] } }),
  );
  await page.route(`**/api/v2/agent/conversations/${conversationID}`, (r) =>
    r.fulfill({ json: conversation }),
  );
  await page.route(
    `**/api/v2/agent/conversations/${conversationID}/messages`,
    (r) => {
      if (r.request().method() === "POST") {
        sent = r.request().postDataJSON().question;
        return r.fulfill({
          status: 202,
          json: {
            run_id: "run-one",
            run: { id: "run-one", state: "completed", progress: "completed" },
          },
        });
      }
      return r.fulfill({
        json: {
          items: [
            {
              id: 1,
              run_id: "run-one",
              role: "user",
              content:
                "每天推荐 5 篇 RAG 和 AI Agent 方向的新论文，附中文导读。",
              citations: [],
            },
            {
              id: 2,
              run_id: "run-one",
              role: "assistant",
              content:
                "已根据你的要求生成草案。匹配预览：过去 7 天匹配 18 篇，主题集中在检索增强、工具调用与 Agent 评测。",
              provider: "qwen",
              model: "qwen-plus",
              draft_id: "draft-one",
              citations: [],
            },
          ],
          next_before: 0,
        },
      });
    },
  );
  await page.route("**/api/v2/agent/runs/run-one", (r) =>
    r.fulfill({
      json: {
        run: { id: "run-one", state: "completed", progress: "completed" },
      },
    }),
  );
  await page.route("**/api/v2/agent/subscription-drafts/draft-one", (r) => {
    if (r.request().method() === "PATCH") {
      const body = r.request().postDataJSON();
      expect(body.version).toBe(version);
      version++;
      value = body.payload;
    }
    return r.fulfill({ json: draft() });
  });
  await page.route(
    "**/api/v2/agent/subscription-drafts/draft-one/confirm",
    async (r) => {
      confirmations++;
      expect(r.request().postDataJSON()).toEqual({ version });
      await new Promise((resolve) => setTimeout(resolve, 200));
      confirmed = true;
      await r.fulfill({ json: { ...subscription, id: 25 } });
    },
  );
  return {
    confirmations: () => confirmations,
    value: () => value,
    sent: () => sent,
    listReads: () => listReads,
  };
}

for (const width of [1920, 1440, 1024, 768, 390, 320]) {
  test(`assistant layout and editor at ${width}px`, async ({ page }, info) => {
    await assistant(page);
    await page.setViewportSize({ width, height: width >= 1440 ? 1200 : 900 });
    await page.goto(url);
    const panel = page.locator(".subscription-assistant-panel");
    await expect(panel.getByText(payload.name, { exact: true })).toBeVisible();
    await expect(
      page.getByRole("button", { name: "助手设置" }),
    ).toHaveAttribute("aria-expanded", "false");
    await expect(page.getByLabel("历史对话")).toBeHidden();
    await expect(
      page.getByRole("dialog", { name: "订阅助手", exact: true }),
    ).toHaveCount(0);
    if (width < 1440)
      await expect(
        page.getByRole("tablist", { name: "订阅视图" }),
      ).toBeVisible();
    else await expect(page.getByRole("separator")).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    const input = page.getByRole("textbox", { name: "你的问题" });
    const before = (await input.boundingBox())!;
    await page.screenshot({
      path: info.outputPath(`assistant-${width}.png`),
      fullPage: true,
    });
    await page.getByRole("button", { name: "编辑草案", exact: true }).click();
    await page
      .getByLabel("草案名称")
      .fill("编辑中的研究方向" + "长标题".repeat(15));
    await page
      .getByLabel("草案关键词（逗号分隔，OR）")
      .fill("long-keyword".repeat(30));
    await page
      .getByRole("button", { name: "保存草案修改" })
      .scrollIntoViewIfNeeded();
    expect((await input.boundingBox())!.y).toBeCloseTo(before.y, 0);
    await expect(
      page.getByRole("heading", { name: "订阅助手", exact: true }),
    ).toBeInViewport();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath(`assistant-editor-${width}.png`),
    });
  });
}

test("desktop list remains operable and Escape closes the topmost modal first", async ({
  page,
}) => {
  await assistant(page);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/subscriptions");
  const trigger = page.getByRole("button", { name: "AI 创建订阅" });
  await trigger.click();
  await page
    .getByRole("combobox", { name: "状态", exact: true })
    .selectOption("false");
  await page.getByRole("button", { name: "编辑 研究订阅" }).click();
  await expect(
    page.getByRole("dialog", { name: "编辑订阅", exact: true }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("dialog", { name: "编辑订阅", exact: true }),
  ).toHaveCount(0);
  await expect(page.locator(".subscription-assistant-panel")).toBeVisible();
  await page.getByRole("button", { name: "关闭订阅助手" }).click();
  await expect(trigger).toBeFocused();
  await expect(page).not.toHaveURL(/assistant=/);
});

test("resizing and compact tabs preserve unsent text and draft edits", async ({
  page,
}) => {
  await assistant(page);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(url);
  const input = page.getByRole("textbox", { name: "你的问题" });
  await input.fill("尚未发送的调整要求");
  await page.getByRole("button", { name: "编辑草案", exact: true }).click();
  await page.getByLabel("草案名称").fill("尚未保存的草案名称");
  await page.setViewportSize({ width: 390, height: 844 });
  const panel = page.locator(".subscription-assistant-panel");
  await expect(page.getByRole("tablist", { name: "订阅视图" })).toBeVisible();
  await expect(input).toHaveValue("尚未发送的调整要求");
  await expect(page.getByLabel("草案名称")).toHaveValue("尚未保存的草案名称");
  await page.getByRole("tab", { name: "订阅列表" }).click();
  await expect(panel).toBeHidden();
  await expect(
    page.getByRole("combobox", { name: "状态", exact: true }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "AI 助手" }).click();
  await page.setViewportSize({ width: 1920, height: 1000 });
  await expect(panel).not.toHaveAttribute("aria-modal");
  await expect(page.getByLabel("草案名称")).toHaveValue("尚未保存的草案名称");
  await expect(input).toHaveValue("尚未发送的调整要求");
});

test("draft adjustment, save and explicit confirmation use the latest version", async ({
  page,
}) => {
  const state = await assistant(page);
  await page.goto(url);
  await page.getByRole("button", { name: "继续调整" }).click();
  await expect(page.getByRole("textbox", { name: "你的问题" })).toBeFocused();
  expect(state.sent()).toBe("");
  expect(state.confirmations()).toBe(0);
  await page.getByRole("button", { name: "编辑草案", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "确认创建订阅" }),
  ).toBeDisabled();
  await page.getByLabel("草案名称").fill("修改后的研究订阅");
  await page.getByLabel("每封篇数", { exact: true }).fill("8");
  await page.getByRole("button", { name: "保存草案修改" }).click();
  await expect(
    page.getByText("修改后的研究订阅", { exact: true }),
  ).toBeVisible();
  expect(state.value().max_items_per_digest).toBe(8);
  await expect(page.getByText(`每天 · ${profile.digest_time}`)).toBeVisible();
  const reads = state.listReads();
  const confirm = page.getByRole("button", { name: "确认创建订阅" });
  await confirm.click();
  await expect(confirm).toBeDisabled();
  await expect(page.getByText("已创建订阅 #25")).toBeVisible();
  expect(state.confirmations()).toBe(1);
  await expect.poll(state.listReads).toBeGreaterThan(reads);
});

for (const state of ["expired", "superseded"] as const) {
  test(`${state} drafts cannot be confirmed`, async ({ page }) => {
    const result = await assistant(
      page,
      state === "expired"
        ? { expires: "2000-01-01T00:00:00Z" }
        : { latest: false },
    );
    await page.goto(url);
    await expect(
      page.getByRole("button", { name: "确认创建订阅" }),
    ).toBeDisabled();
    await expect(
      page.getByText(
        state === "expired"
          ? "此草案已过期，请重新生成。"
          : "此草案已被新版替代，请查看最新草案。",
        { exact: true },
      ),
    ).toBeVisible();
    expect(result.confirmations()).toBe(0);
  });
}

test("idle drafts expire without a reload", async ({ page }) => {
  await page.clock.install();
  await assistant(page, {
    expires: new Date(Date.now() + 60_000).toISOString(),
  });
  await page.goto(url);
  await expect(
    page.getByRole("button", { name: "确认创建订阅" }),
  ).toBeEnabled();
  await page.clock.fastForward(61_000);
  await expect(
    page.getByRole("button", { name: "确认创建订阅" }),
  ).toBeDisabled();
});

test("Enter sends, Shift+Enter and IME confirmation do not send", async ({
  page,
}) => {
  const state = await assistant(page);
  await page.goto(url);
  const input = page.getByRole("textbox", { name: "你的问题" });
  await input.fill("研究方向");
  await input.press("Shift+Enter");
  await expect(input).toHaveValue("研究方向\n");
  await input.dispatchEvent("compositionstart");
  await input.press("Enter");
  expect(state.sent()).toBe("");
  await input.dispatchEvent("compositionend");
  await input.fill("关注工具调用");
  await input.press("Enter");
  await expect.poll(state.sent).toBe("关注工具调用");
  await expect(input).toHaveValue("");
});

test("missing API stays visible with settings collapsed and prevents keyboard submission", async ({
  page,
}) => {
  await assistant(page);
  await page.route("**/api/v2/ai/credentials", (r) =>
    r.fulfill({ json: { items: [] } }),
  );
  await page.goto(url);
  await expect(page.getByRole("link", { name: "配置 API" })).toBeVisible();
  await expect(page.getByText("未配置可用 API", { exact: true })).toBeVisible();
  await page.getByRole("textbox", { name: "你的问题" }).fill("关注研究");
  await expect(
    page.getByRole("button", { name: "发送", exact: true }),
  ).toBeDisabled();
  await page.getByRole("textbox", { name: "你的问题" }).press("Enter");
  await expect(page.getByRole("textbox", { name: "你的问题" })).toHaveValue(
    "关注研究",
  );
});

test("account failure shows unavailable schedule instead of an invented time", async ({
  page,
}) => {
  await assistant(page);
  await page.route("**/api/v2/me", (r) =>
    r.fulfill({ status: 503, json: { code: "UNAVAILABLE" } }),
  );
  await page.goto(url);
  await expect(
    page.getByRole("definition").filter({ hasText: "发送安排暂不可用" }),
  ).toBeVisible();
});

test("subscription split preserves list and draft state with independent preferences", async ({
  page,
}, info) => {
  const state = await assistant(page);
  const paperPreference = {
    version: 1,
    direction: "horizontal",
    paperFirst: false,
    horizontal: 0.55,
    vertical: 0.5,
  };
  await page.addInitScript(
    (value) =>
      localStorage.setItem(
        "signalwatch.paper-layout.v1",
        JSON.stringify(value),
      ),
    paperPreference,
  );
  await page.route("**/api/v2/subscriptions?**", (r) =>
    r.fulfill({
      json: {
        items: Array.from({ length: 20 }, (_, i) => ({
          ...subscription,
          id: i + 1,
          name: `研究订阅 ${i + 1}`,
        })),
        total: 40,
      },
    }),
  );
  await page.setViewportSize({ width: 1920, height: 1200 });
  await page.goto(url);
  await page
    .getByRole("combobox", { name: "状态", exact: true })
    .selectOption("false");
  await page.getByRole("button", { name: "下一页", exact: true }).click();
  await page.getByRole("textbox", { name: "你的问题" }).fill("保留聊天草稿");
  await page.getByRole("button", { name: "编辑草案", exact: true }).click();
  const editor = page.getByLabel("草案名称");
  await editor.fill("未保存的分栏草案");
  await editor.evaluate((el) => el.setAttribute("data-instance", "original"));
  await page
    .locator(".subscriptions-list-panel")
    .evaluate((el) => (el.scrollTop = 180));
  const divider = page.getByRole("separator");
  await divider.press("ArrowRight");
  await expect(divider).toHaveAttribute("aria-valuenow", "62");
  const box = (await divider.boundingBox())!;
  await page.mouse.move(box.x + 6, box.y + 40);
  await page.mouse.down();
  await page.mouse.move(box.x - 80, box.y + 40);
  await page.mouse.up();
  await page.getByRole("button", { name: "交换位置" }).click();
  await page.getByRole("button", { name: "上下排列" }).click();
  await expect(divider).toHaveAttribute("aria-orientation", "horizontal");
  await expect(editor).toHaveAttribute("data-instance", "original");
  await expect(editor).toHaveValue("未保存的分栏草案");
  await expect(page.getByRole("textbox", { name: "你的问题" })).toHaveValue(
    "保留聊天草稿",
  );
  await expect(
    page.getByRole("combobox", { name: "状态", exact: true }),
  ).toHaveValue("false");
  await expect(
    page.getByRole("button", { name: "上一页", exact: true }),
  ).toBeEnabled();
  expect(
    await page
      .locator(".subscriptions-list-panel")
      .evaluate((el) => el.scrollTop),
  ).toBe(180);
  expect(state.confirmations()).toBe(0);
  expect(state.sent()).toBe("");
  await page.screenshot({
    path: info.outputPath("subscription-vertical-swapped.png"),
    fullPage: true,
  });
  const stored = await page.evaluate(() =>
    localStorage.getItem("signalwatch.subscription-layout.v1"),
  );
  expect(
    await page.evaluate(() =>
      JSON.parse(localStorage.getItem("signalwatch.paper-layout.v1")!),
    ),
  ).toEqual(paperPreference);
  await page.setViewportSize({ width: 390, height: 900 });
  await expect(page.getByRole("tab", { name: "AI 助手" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await page.getByRole("tab", { name: "订阅列表" }).click();
  await expect(
    page.getByRole("combobox", { name: "状态", exact: true }),
  ).toHaveValue("false");
  await page.getByRole("tab", { name: "AI 助手" }).click();
  await expect(editor).toHaveValue("未保存的分栏草案");
  await page.setViewportSize({ width: 1920, height: 1200 });
  await expect(divider).toBeVisible();
  await page.reload();
  await expect(divider).toHaveAttribute("aria-orientation", "horizontal");
  expect(
    await page.evaluate(() =>
      localStorage.getItem("signalwatch.subscription-layout.v1"),
    ),
  ).toBe(stored);
  await page.getByRole("button", { name: "左右排列" }).click();
  await page.screenshot({
    path: info.outputPath("subscription-horizontal-swapped.png"),
    fullPage: true,
  });
  await page.getByRole("textbox", { name: "你的问题" }).press("Escape");
  await expect(page.locator(".subscription-assistant-panel")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "AI 创建订阅" })).toBeFocused();
  await expect(page.locator(".subscriptions-page")).not.toHaveClass(
    /has-assistant/,
  );
});

test("subscription layout changes do not resubmit an active run", async ({
  page,
}) => {
  await assistant(page);
  await page.route(`**/api/v2/agent/conversations/${conversationID}`, (r) =>
    r.fulfill({ json: { id: conversationID, active_run_id: "active-layout" } }),
  );
  await page.route("**/api/v2/agent/runs/active-layout", (r) =>
    r.fulfill({
      json: {
        run: { id: "active-layout", state: "running", progress: "generating" },
        steps: [],
      },
    }),
  );
  let writes = 0;
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().includes("/agent/")) writes++;
  });
  await page.setViewportSize({ width: 1920, height: 1200 });
  await page.goto(url);
  await expect(page.getByRole("button", { name: "停止本轮" })).toBeVisible();
  await page.getByRole("button", { name: "上下排列" }).click();
  await page.getByRole("button", { name: "交换位置" }).click();
  await page.getByRole("separator").press("ArrowDown");
  await page.setViewportSize({ width: 390, height: 900 });
  await expect(page.getByRole("button", { name: "停止本轮" })).toBeVisible();
  expect(writes).toBe(0);
});
