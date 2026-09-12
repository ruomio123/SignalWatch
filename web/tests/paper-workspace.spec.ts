import { test, expect, type Page } from "@playwright/test";
import { setup, paper } from "./fixtures";

const conversation = {
  id: "paper-one",
  kind: "paper",
  paper_id: 1,
  paper_report_ready: true,
  title: "研究方法",
};
async function assistant(page: Page) {
  await page.route("**/api/v2/ai/credentials", (r) =>
    r.fulfill({
      json: {
        items: [
          {
            id: "key-one",
            generation: "key-one",
            name: "研究 API",
            configured: true,
            usable: true,
            is_default: true,
            provider: "qwen",
            model: "qwen-plus",
            masked_key: "••••1234",
          },
        ],
      },
    }),
  );
  await page.route("**/api/v2/ai/providers", (r) =>
    r.fulfill({
      json: {
        items: [
          {
            id: "qwen",
            name: "Qwen",
            models: [
              { id: "qwen-plus", name: "Plus" },
              { id: "qwen-flash", name: "Flash" },
            ],
          },
        ],
      },
    }),
  );
  await page.route("**/api/v2/agent/conversations?**", (r) =>
    r.fulfill({ json: { items: [conversation] } }),
  );
  await page.route("**/api/v2/agent/conversations/paper-one", (r) =>
    r.fulfill({ json: conversation }),
  );
  await page.route("**/api/v2/agent/conversations/paper-one/messages", (r) =>
    r.fulfill({ json: { items: [], next_before: 0 } }),
  );
}
async function noOverflow(page: Page) {
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  for (const selector of [
    ".paper-content",
    ".paper-assistant-panel",
    ".assistant-scroll",
  ]) {
    const element = page.locator(selector);
    if (await element.isVisible())
      expect(
        await element.evaluate((el) => el.scrollWidth <= el.clientWidth + 1),
      ).toBe(true);
  }
}

test("list state, scroll and focus survive detail navigation and browser history", async ({
  page,
}) => {
  await setup(page);
  const papers = Array.from({ length: 20 }, (_, n) => ({
    ...paper,
    id: n + 1,
    title: `Paper ${n + 1}`,
  }));
  await page.route("**/api/v2/papers?**", (r) =>
    r.fulfill({ json: { items: papers, total: 45 } }),
  );
  await page.route("**/api/v2/papers/10", (r) =>
    r.fulfill({ json: papers[9] }),
  );
  await page.goto("/papers");
  await page.getByPlaceholder("标题或摘要").fill("agent");
  await page.getByRole("button", { name: "搜索", exact: true }).click();
  await page
    .getByRole("combobox", { name: "订阅", exact: true })
    .selectOption("1");
  const loaded = page.waitForResponse((r) =>
    r.url().includes("/papers?page=2"),
  );
  await page.getByRole("button", { name: "下一页" }).click();
  await (await loaded).finished();
  await expect(page.getByText("正在读取…", { exact: true })).toHaveCount(0);
  await expect(page).toHaveURL(/page=2/);
  const trigger = page.getByRole("button", { name: "Paper 10", exact: true });
  await trigger.scrollIntoViewIfNeeded();
  const y = await page.evaluate(() => scrollY);
  await trigger.click();
  await expect(page.getByRole("heading", { name: "Paper 10" })).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "论文详情" })).toBeFocused();
  await page.getByRole("button", { name: "返回论文列表" }).click();
  await expect(trigger).toBeFocused();
  expect(await page.evaluate(() => scrollY)).toBeCloseTo(y, 0);
  await expect(page.getByPlaceholder("标题或摘要")).toHaveValue("agent");
  await expect(
    page.getByRole("combobox", { name: "订阅", exact: true }),
  ).toHaveValue("1");
  await expect(page).toHaveURL(/page=2/);
  await page.goBack();
  await expect(page.getByRole("heading", { name: "Paper 10" })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "Paper 10" })).toBeVisible();
  await page.goForward();
  await expect(page.getByRole("heading", { name: "匹配论文" })).toBeVisible();
  await expect(
    page.getByRole("button", { name: "AI 论文助手", exact: true }),
  ).toHaveCount(0);
});

for (const width of [1920, 1440, 1024, 768, 390, 320]) {
  test(`inline reading and persistent assistant at ${width}px`, async ({
    page,
  }, info) => {
    await setup(page);
    await assistant(page);
    await page.setViewportSize({ width, height: 960 });
    const title =
      "Long-Horizon Planning: 多模态研究与实验评估 " + "LongTitle".repeat(8);
    await page.route("**/api/v2/papers/1", (r) =>
      r.fulfill({
        json: {
          ...paper,
          title,
          abstract: paper.abstract.repeat(150),
          comments: "14 pages, 11 figures",
        },
      }),
    );
    await page.route("**/api/v2/agent/conversations/paper-one/messages", (r) =>
      r.fulfill({
        json: {
          items: [
            { id: 1, role: "user", content: "请分析实验方法。" },
            {
              id: 2,
              role: "assistant",
              content: "论文分析与实验局限。".repeat(100),
              citations: [],
            },
          ],
          next_before: 0,
        },
      }),
    );
    await page.goto("/papers?paper_id=1");
    await expect(page.getByRole("heading", { name: title })).toBeVisible();
    await expect(page.locator(".paper-assistant-panel")).toHaveCount(0);
    await noOverflow(page);
    await page.screenshot({
      path: info.outputPath(`paper-detail-${width}.png`),
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "AI 论文助手", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "AI 论文助手" }),
    ).toBeVisible();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(
      page.getByRole("combobox", { name: "对话模型" }),
    ).not.toBeVisible();
    await page.getByRole("button", { name: "助手设置" }).click();
    await page
      .getByRole("combobox", { name: "历史对话" })
      .selectOption("paper-one");
    await page
      .getByRole("combobox", { name: "对话模型" })
      .selectOption("qwen-flash");
    await page
      .getByRole("combobox", { name: "解读资料" })
      .selectOption("abstract");
    await page.getByRole("button", { name: "助手设置" }).click();
    const input = page.getByRole("textbox", { name: "你的问题" });
    await input.fill("保留未发送的问题");
    if (width >= 1440) {
      await expect(page.getByRole("tablist")).toHaveCount(0);
      const reading = await page.locator(".paper-content").boundingBox();
      const chat = await page.locator(".paper-assistant-panel").boundingBox();
      expect(chat!.x).toBeGreaterThan(reading!.x + reading!.width);
      expect(chat!.width).toBeGreaterThanOrEqual(360);
      expect(reading!.width / (reading!.width + chat!.width)).toBeCloseTo(
        0.6,
        2,
      );
    } else {
      await page.getByRole("tab", { name: "论文内容" }).click();
      await expect(page.getByRole("heading", { name: title })).toBeVisible();
      await page.getByRole("tab", { name: "AI 助手" }).click();
      await expect(input).toHaveValue("保留未发送的问题");
    }
    await noOverflow(page);
    await expect(input).toBeInViewport();
    await expect(
      page.getByRole("heading", { name: "AI 论文助手" }),
    ).toBeInViewport();
    await page.screenshot({
      path: info.outputPath(`paper-assistant-${width}.png`),
      fullPage: true,
    });
    await page.setViewportSize({
      width: width < 1440 ? 1920 : 390,
      height: 960,
    });
    await expect(input).toHaveValue("保留未发送的问题");
    await expect(page.getByText("Qwen · Flash · 配置可用")).toBeVisible();
    await expect(page.getByText("请求资料：仅标题和摘要")).toBeVisible();
    await page.getByRole("button", { name: "关闭AI 论文助手" }).click();
    await expect(page.getByRole("heading", { name: title })).toBeVisible();
    await expect(
      page.getByRole("button", { name: "AI 论文助手", exact: true }),
    ).toBeFocused();
    await expect(page).not.toHaveURL(/assistant=|conversation=/);
  });
}

for (const mode of ["fulltext", "abstract"]) {
  test(`paper ${mode} send respects IME, model and explicit stop`, async ({
    page,
  }) => {
    await setup(page);
    await assistant(page);
    let active = false;
    let writes = 0;
    let stops = 0;
    await page.route("**/api/v2/agent/conversations/paper-one", (r) =>
      r.fulfill({
        json: {
          ...conversation,
          ...(active ? { active_run_id: "run-one" } : {}),
        },
      }),
    );
    await page.route(
      "**/api/v2/agent/conversations/paper-one/messages",
      (r) => {
        if (r.request().method() === "POST") {
          writes++;
          active = true;
          expect(r.request().postDataJSON()).toMatchObject({
            context_mode: mode,
            provider: "qwen",
            credential_id: "key-one",
            model: "qwen-flash",
            question: "分析方法\n和局限",
          });
          return r.fulfill({
            status: 202,
            json: {
              run_id: "run-one",
              run: { id: "run-one", state: "running", progress: "generating" },
            },
          });
        }
        return r.fulfill({ json: { items: [] } });
      },
    );
    await page.route("**/api/v2/agent/runs/run-one", (r) =>
      r.fulfill({
        json: {
          run: {
            id: "run-one",
            state: active ? "running" : "cancelled",
            progress: "generating",
          },
          steps: [],
        },
      }),
    );
    await page.route("**/api/v2/agent/runs/run-one/cancel", (r) => {
      stops++;
      active = false;
      return r.fulfill({ status: 204 });
    });
    await page.goto(
      "/papers?paper_id=1&assistant=paper&conversation=paper-one",
    );
    await page.getByRole("button", { name: "助手设置" }).click();
    await page
      .getByRole("combobox", { name: "对话模型" })
      .selectOption("qwen-flash");
    await page.getByRole("combobox", { name: "解读资料" }).selectOption(mode);
    await page.setViewportSize({ width: 1920, height: 1200 });
    const input = page.getByRole("textbox", { name: "你的问题" });
    await input.fill("分析方法");
    await input.dispatchEvent("compositionstart");
    await input.press("Enter");
    expect(writes).toBe(0);
    await input.dispatchEvent("compositionend");
    await input.fill("分析方法");
    await input.press("Shift+Enter");
    await input.pressSequentially("和局限");
    await input.press("Enter");
    await expect(page.getByRole("button", { name: "停止本轮" })).toBeVisible();
    await expect(
      page.getByRole("button", { name: "发送", exact: true }),
    ).toBeDisabled();
    expect(writes).toBe(1);
    await page.getByRole("button", { name: "交换位置" }).click();
    await page.getByRole("button", { name: "上下排列" }).click();
    await page.getByRole("separator").press("ArrowUp");
    await expect(page.getByRole("button", { name: "停止本轮" })).toBeVisible();
    expect(writes).toBe(1);
    await page.getByRole("button", { name: "停止本轮" }).click();
    await expect(page.getByText("本轮已停止。")).toBeVisible();
    expect(stops).toBe(1);
  });
}

test("closing and returning stop polling without cancelling the server run", async ({
  page,
}) => {
  await setup(page);
  await assistant(page);
  let reads = 0;
  let cancellations = 0;
  await page.route("**/api/v2/agent/conversations/paper-one", (r) =>
    r.fulfill({ json: { ...conversation, active_run_id: "run-one" } }),
  );
  await page.route("**/api/v2/agent/runs/run-one", (r) => {
    reads++;
    return r.fulfill({
      json: {
        run: { id: "run-one", state: "running", progress: "generating" },
      },
    });
  });
  page.on("request", (r) => {
    if (r.url().endsWith("/cancel")) cancellations++;
  });
  await page.goto("/papers?paper_id=1&assistant=paper&conversation=paper-one");
  await expect(page.getByRole("button", { name: "停止本轮" })).toBeVisible();
  await page.getByRole("button", { name: "关闭AI 论文助手" }).click();
  const stopped = reads;
  await page.waitForTimeout(2300);
  expect(reads).toBe(stopped);
  await page.goBack();
  await expect(page.getByRole("button", { name: "停止本轮" })).toBeVisible();
  await page.getByRole("button", { name: "返回论文列表" }).click();
  const returned = reads;
  await page.waitForTimeout(2300);
  expect(reads).toBe(returned);
  expect(cancellations).toBe(0);
});

test("failed detail never starts the assistant and can return or retry", async ({
  page,
}) => {
  await setup(page);
  let reads = 0;
  page.on("request", (r) => {
    if (r.url().includes("/agent/")) reads++;
  });
  await page.route("**/api/v2/papers/1", (r) =>
    r.fulfill({ status: 503, json: { code: "UNAVAILABLE", message: "down" } }),
  );
  await page.goto("/papers?paper_id=1&assistant=paper&conversation=paper-one");
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "AI 论文助手", exact: true }),
  ).toBeDisabled();
  expect(reads).toBe(0);
  await page.getByRole("button", { name: "返回论文列表" }).click();
  await expect(page).toHaveURL(/\/papers$/);
});

test("a conversation belonging to another paper cannot publish replies or accept messages", async ({
  page,
}) => {
  await setup(page);
  await assistant(page);
  await page.route("**/api/v2/papers/2", (r) =>
    r.fulfill({ json: { ...paper, id: 2, title: "Second Paper" } }),
  );
  await page.route("**/api/v2/agent/conversations/paper-one/messages", (r) =>
    r.fulfill({
      json: {
        items: [
          {
            id: 1,
            role: "assistant",
            content: "第一篇的旧回复",
            citations: [],
          },
        ],
      },
    }),
  );
  await page.goto("/papers?paper_id=2&assistant=paper&conversation=paper-one");
  await expect(page.getByRole("alert")).toContainText("该对话不属于当前论文");
  await expect(page.getByText("第一篇的旧回复")).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: "你的问题" })).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "发送", exact: true }),
  ).toBeDisabled();
});

test("late replies are discarded when returning and opening a different paper", async ({
  page,
}) => {
  await setup(page);
  await assistant(page);
  const second = { ...paper, id: 2, title: "Second Paper" };
  await page.route("**/api/v2/papers?**", (r) =>
    r.fulfill({ json: { items: [paper, second], total: 2 } }),
  );
  await page.route("**/api/v2/papers/2", (r) => r.fulfill({ json: second }));
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  let reading = false;
  await page.route(
    "**/api/v2/agent/conversations/paper-one/messages",
    async (r) => {
      reading = true;
      await pending;
      await r
        .fulfill({
          json: {
            items: [
              {
                id: 1,
                role: "assistant",
                content: "第一篇的延迟回复",
                citations: [],
              },
            ],
          },
        })
        .catch(() => {});
    },
  );
  await page.goto("/papers?paper_id=1&assistant=paper&conversation=paper-one");
  await expect.poll(() => reading).toBe(true);
  await page.getByRole("button", { name: "返回论文列表" }).click();
  await page.getByRole("button", { name: "Second Paper", exact: true }).click();
  await expect(page).not.toHaveURL(/assistant=|conversation=/);
  await expect(
    page.getByRole("heading", { name: "Second Paper" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "AI 论文助手", exact: true }).click();
  await expect(page.getByRole("textbox", { name: "你的问题" })).toHaveValue("");
  release();
  await expect(page.getByText("第一篇的延迟回复")).toHaveCount(0);
  await expect(page.getByRole("dialog")).toHaveCount(0);
});

test("late detail reads never replace the next paper", async ({ page }) => {
  await setup(page);
  const second = { ...paper, id: 2, title: "Second Paper" };
  await page.route("**/api/v2/papers?**", (r) =>
    r.fulfill({ json: { items: [paper, second], total: 2 } }),
  );
  await page.route("**/api/v2/papers/2", (r) => r.fulfill({ json: second }));
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  let reading = false;
  await page.route("**/api/v2/papers/1", async (r) => {
    reading = true;
    await pending;
    await r.fulfill({ json: paper }).catch(() => {});
  });
  await page.goto("/papers?paper_id=1");
  await expect.poll(() => reading).toBe(true);
  await page.getByRole("button", { name: "返回论文列表" }).click();
  await page.getByRole("button", { name: "Second Paper", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Second Paper" }),
  ).toBeVisible();
  release();
  await expect(page.getByRole("heading", { name: paper.title })).toHaveCount(0);
  await expect(
    page.getByRole("heading", { name: "Second Paper" }),
  ).toBeVisible();
});

async function openResizableWorkspace(page: Page) {
  await setup(page);
  await assistant(page);
  await page.setViewportSize({ width: 1920, height: 1200 });
  await page.route("**/api/v2/papers/1", (r) =>
    r.fulfill({ json: { ...paper, abstract: paper.abstract.repeat(400) } }),
  );
  await page.route("**/api/v2/agent/conversations/paper-one/messages", (r) =>
    r.fulfill({
      json: {
        items: [
          {
            id: 1,
            role: "assistant",
            content: "已有的论文分析与实验内容。".repeat(500),
            citations: [],
          },
        ],
        next_before: 0,
      },
    }),
  );
  await page.goto("/papers?paper_id=1&assistant=paper&conversation=paper-one");
  await expect(page.getByRole("separator")).toBeVisible();
}
async function paperRatio(page: Page) {
  const p = await page.locator(".paper-content").boundingBox();
  const a = await page.locator(".paper-assistant-panel").boundingBox();
  return p!.width / (p!.width + a!.width);
}

test("panel keyboard, drag, swap and direction preserve draft, DOM and scrolling", async ({
  page,
}, info) => {
  await openResizableWorkspace(page);
  let submissions = 0;
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().includes("/agent/")) submissions++;
  });
  const input = page.getByRole("textbox", { name: "你的问题" });
  await input.fill("调整布局时保留这个草稿");
  await input.evaluate((el) => el.setAttribute("data-instance", "original"));
  await page.locator(".paper-content").evaluate((el) => {
    el.scrollTop = 120;
  });
  await page.locator(".assistant-scroll").evaluate((el) => {
    el.scrollTop = 160;
  });
  const separator = page.getByRole("separator");
  await separator.focus();
  await separator.press("ArrowRight");
  expect(await paperRatio(page)).toBeCloseTo(0.62, 2);
  await separator.press("Shift+ArrowLeft");
  expect(await paperRatio(page)).toBeCloseTo(0.52, 2);
  await separator.press("Home");
  expect(
    (await page.locator(".paper-content").boundingBox())!.width,
  ).toBeCloseTo(320, 0);
  await separator.press("End");
  expect(
    (await page.locator(".paper-assistant-panel").boundingBox())!.width,
  ).toBeCloseTo(360, 0);
  await separator.dblclick();
  expect(await paperRatio(page)).toBeCloseTo(0.5, 2);
  const box = (await separator.boundingBox())!;
  await page.mouse.move(box.x + 6, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + 166, box.y + box.height / 2, { steps: 6 });
  await page.mouse.up();
  expect(await paperRatio(page)).toBeGreaterThan(0.57);
  const beforeSwap = await paperRatio(page);
  await page.getByRole("button", { name: "交换位置" }).click();
  expect(await paperRatio(page)).toBeCloseTo(beforeSwap, 2);
  expect(
    (await page.locator(".paper-assistant-panel").boundingBox())!.x,
  ).toBeLessThan((await page.locator(".paper-content").boundingBox())!.x);
  expect(
    await page.locator(".paper-workspace > :first-child").getAttribute("class"),
  ).toBe("paper-assistant-panel");
  await page.getByRole("button", { name: "上下排列" }).click();
  await expect(separator).toHaveAttribute("aria-orientation", "horizontal");
  expect(
    (await page.locator(".paper-assistant-panel").boundingBox())!.y,
  ).toBeLessThan((await page.locator(".paper-content").boundingBox())!.y);
  await expect(input).toHaveAttribute("data-instance", "original");
  await expect(input).toHaveValue("调整布局时保留这个草稿");
  expect(
    await page.locator(".paper-content").evaluate((el) => el.scrollTop),
  ).toBe(120);
  expect(
    await page.locator(".assistant-scroll").evaluate((el) => el.scrollTop),
  ).toBe(160);
  await noOverflow(page);
  await page.screenshot({
    path: info.outputPath("paper-vertical-swapped.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "左右排列" }).click();
  expect(await paperRatio(page)).toBeCloseTo(beforeSwap, 2);
  await page.screenshot({
    path: info.outputPath("paper-horizontal-swapped.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "恢复默认" }).click();
  expect(await paperRatio(page)).toBeCloseTo(0.6, 2);
  expect(submissions).toBe(0);
});

test("panel preferences survive refresh, mobile and insufficient vertical height", async ({
  page,
}) => {
  await openResizableWorkspace(page);
  await page.getByRole("button", { name: "上下排列" }).click();
  await page.getByRole("separator").press("ArrowUp");
  await page.getByRole("button", { name: "交换位置" }).click();
  const saved = await page.evaluate(() =>
    localStorage.getItem("signalwatch.paper-layout.v1"),
  );
  await page.reload();
  await expect(page.getByRole("separator")).toHaveAttribute(
    "aria-orientation",
    "horizontal",
  );
  expect(
    (await page.locator(".paper-assistant-panel").boundingBox())!.y,
  ).toBeLessThan((await page.locator(".paper-content").boundingBox())!.y);
  await page.setViewportSize({ width: 1920, height: 700 });
  await expect(page.getByRole("tablist")).toBeVisible();
  await expect(page.getByRole("separator")).toHaveCount(0);
  await page.getByRole("button", { name: "左右排列" }).click();
  await expect(page.getByRole("separator")).toBeVisible();
  await page.getByRole("button", { name: "上下排列" }).click();
  await page.setViewportSize({ width: 390, height: 960 });
  await expect(page.getByRole("tablist")).toBeVisible();
  await noOverflow(page);
  await page.setViewportSize({ width: 1920, height: 1200 });
  await expect(page.getByRole("separator")).toBeVisible();
  expect(
    await page.evaluate(() =>
      localStorage.getItem("signalwatch.paper-layout.v1"),
    ),
  ).toBe(saved);
});

for (const storage of ["corrupt", "unavailable"]) {
  test(`layout works with ${storage} storage and pointer cancellation`, async ({
    page,
  }) => {
    await page.addInitScript((mode) => {
      if (mode === "corrupt")
        localStorage.setItem(
          "signalwatch.paper-layout.v1",
          '{"version":1,"horizontal":"oops"}',
        );
      else {
        const get = Storage.prototype.getItem,
          set = Storage.prototype.setItem;
        Storage.prototype.getItem = function (key) {
          if (key === "signalwatch.paper-layout.v1")
            throw new Error("unavailable");
          return get.call(this, key);
        };
        Storage.prototype.setItem = function (key, value) {
          if (key === "signalwatch.paper-layout.v1")
            throw new Error("unavailable");
          return set.call(this, key, value);
        };
      }
    }, storage);
    await openResizableWorkspace(page);
    expect(await paperRatio(page)).toBeCloseTo(0.6, 2);
    const separator = page.getByRole("separator");
    const box = (await separator.boundingBox())!;
    await page.mouse.move(box.x + 6, box.y + 40);
    await page.mouse.down();
    await page.mouse.move(box.x - 100, box.y + 40);
    await separator.dispatchEvent("pointercancel", { pointerId: 1 });
    await page.mouse.up();
    expect(await paperRatio(page)).toBeCloseTo(0.6, 2);
    expect(await page.evaluate(() => document.body.style.userSelect)).toBe("");
    await separator.press("ArrowLeft");
    expect(await paperRatio(page)).toBeCloseTo(0.58, 2);
  });
}

test("touch divider adjusts panels and releases selection capture", async ({
  page,
}) => {
  await openResizableWorkspace(page);
  const box = (await page.getByRole("separator").boundingBox())!;
  const cdp = await page.context().newCDPSession(page);
  const point = { x: box.x + 6, y: box.y + 80 };
  await cdp.send("Input.dispatchTouchEvent", {
    type: "touchStart",
    touchPoints: [point],
  });
  await cdp.send("Input.dispatchTouchEvent", {
    type: "touchMove",
    touchPoints: [{ ...point, x: point.x - 150 }],
  });
  await cdp.send("Input.dispatchTouchEvent", {
    type: "touchEnd",
    touchPoints: [],
  });
  expect(await paperRatio(page)).toBeLessThan(0.53);
  expect(await page.evaluate(() => document.body.style.userSelect)).toBe("");
  await cdp.detach();
});
