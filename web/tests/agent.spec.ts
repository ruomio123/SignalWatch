import {
  test,
  expect,
  assistantURL,
  conversation,
  conversationPath,
  credential,
  deferred,
  documentPath,
  documentStatus,
  paper,
} from "./agent-fixtures";
import { readFile } from "node:fs/promises";
import type { Page } from "@playwright/test";

async function captureClipboard(page: Page) {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: {
      writeText: async (text: string) => { (window as Window & { copiedMarkdown?: string }).copiedMarkdown = text; },
    } });
  });
}

async function copiedMarkdown(page: Page) {
  return page.evaluate(() => (window as Window & { copiedMarkdown?: string }).copiedMarkdown ?? "");
}

function questionReply(
  round: number,
  mode: "abstract" | "fulltext",
  content: string,
  fallbackReason?: string,
) {
  return {
    id: round * 2,
    run_id: `question-run-${round}`,
    role: "assistant",
    content,
    provider: credential.provider,
    model: credential.model,
    citations: [],
    result: {
      fields: { answer: { status: "supported", citation_ids: [] } },
      context_mode: mode,
      ...(fallbackReason ? { fallback_reason: fallbackReason } : {}),
      coverage: mode === "fulltext" ? "retrieved_passages" : "abstract_only",
      workflow_version: "paper-fixed-v6",
      source_version: "1706.03762v1",
    },
  };
}

const report = {
  problem: "解决检索准确性问题 [problem-1-1]",
  method: "使用检索方法",
  experiments: "使用独立测试集",
  results: "当前材料不足以可靠回答",
  limitations: "论文未明确说明",
};

function reportMessage(mode: "fulltext" | "abstract") {
  return {
    id: 2,
    run_id: "report-run",
    role: "assistant",
    content: "## 论文问题\n\n解决检索准确性问题 [problem-1-1]",
    provider: credential.provider,
    model: credential.model,
    result: {
      report,
      context_mode: mode,
      ...(mode === "abstract" ? { fallback_reason: "ocr_required" } : {}),
      coverage: mode === "abstract" ? "abstract_only" : "all_extracted_text",
      workflow_version: "paper-fixed-v5",
      source_version: "1706.03762v1",
      fields: Object.fromEntries(
        Object.keys(report).map((field) => [
          field,
          {
            status:
              field === "limitations"
                ? "not_stated"
                : field === "results"
                  ? "insufficient_evidence"
                  : "supported",
            citation_ids: field === "problem" ? ["problem-1-1"] : [],
          },
        ]),
      ),
    },
    citations: [
      {
        id: "problem-1-1",
        page: mode === "abstract" ? 0 : 2,
        quote: "The retrieval method addresses accuracy.",
        url:
          mode === "abstract"
            ? "https://arxiv.org/abs/1706.03762v1"
            : "https://arxiv.org/pdf/1706.03762v1#page=2",
        document_id: mode === "abstract" ? "" : "fixture-document",
        content_hash: "fixture-content-hash",
      },
    ],
  };
}

for (const mode of ["fulltext", "abstract"] as const) {
  test(`report is explicit, evidence is inspectable, and ${mode} output survives reload`, async ({
    page,
    api,
  }) => {
    if (mode === "abstract")
      await page.setViewportSize({ width: 390, height: 844 });
    let generated = false;
    if (mode === "fulltext") {
      api.on("GET", documentPath, (route) =>
        route.fulfill({
          json: generated
            ? {
                ...documentStatus,
                state: "ready",
                usable: true,
                document_id: "fixture-document",
                page_count: 12,
              }
            : documentStatus,
        }),
      );
    }
    const writes: Record<string, unknown>[] = [];
    api.on("GET", conversationPath, (route) =>
      route.fulfill({
        json: { ...conversation, paper_report_ready: generated },
      }),
    );
    api.on("GET", `${conversationPath}/messages`, (route) =>
      route.fulfill({
        json: { items: generated ? [reportMessage(mode)] : [], next_before: 0 },
      }),
    );
    api.on("GET", `${conversationPath}/paper-report`, (route) =>
      route.fulfill({
        json: {
          report: generated ? reportMessage(mode) : null,
          matches_current_paper: generated,
        },
      }),
    );
    api.on("POST", `${conversationPath}/messages`, (route) => {
      writes.push(route.request().postDataJSON());
      generated = true;
      return route.fulfill({
        status: 202,
        json: {
          run_id: "report-run",
          run: {
            id: "report-run",
            task: "paper_report",
            state: "pending",
            progress: "queued",
          },
        },
      });
    });
    api.json("GET", "/agent/runs/report-run", {
      run: {
        id: "report-run",
        task: "paper_report",
        state: "completed",
        progress: "completed",
        effective_context_mode: mode,
      },
      steps: [],
    });

    await page.goto(assistantURL);
    const generate = page.getByRole("button", {
      name: "生成论文报告",
      exact: true,
    });
    await expect(generate).toBeEnabled();
    if (mode === "fulltext") {
      await expect(
        page.getByRole("region", { name: "论文全文材料", exact: true }).getByRole("status"),
      ).toHaveText("全文尚未准备");
    }
    expect(writes).toHaveLength(0);
    await expect(
      page.getByRole("textbox", { name: "你的问题" }),
    ).toBeEnabled();
    await generate.click();
    await expect(page.getByRole("button", { name: "复制 JSON" })).toBeVisible();
    if (mode === "fulltext") {
      // The report workflow prepared the document. Its run update must refresh
      // this card without a reload or a separate manual preparation request.
      const material = page.getByRole("region", { name: "论文全文材料", exact: true });
      await expect(material.getByRole("status")).toHaveText("全文已就绪");
      await expect(material.getByText("下次提问可使用全文。", { exact: true })).toBeVisible();
      expect(api.requestsFor("POST", `${documentPath}/prepare`)).toHaveLength(0);
    }
    expect(writes).toHaveLength(1);
    expect(writes[0]).toMatchObject({
      task: "paper_report",
      question: "",
      context_mode: "fulltext",
      provider: credential.provider,
      credential_id: credential.id,
      model: credential.model,
      idempotency_key: expect.any(String),
    });
    expect(String(writes[0].idempotency_key).length).toBeGreaterThan(0);
    for (const label of [
      "论文问题",
      "核心方法",
      "实验验证",
      "主要结果",
      "局限性",
    ])
      await expect(
        page.getByRole("heading", { name: label, exact: true }),
      ).toBeVisible();
    await expect(page.getByRole("textbox", { name: "你的问题" })).toBeEnabled();
    if (mode === "abstract") {
      await expect(page.getByText(/仅基于摘要/)).toBeVisible();
      await expect(page.getByText(/扫描或图片页面需要 OCR/)).toBeVisible();
    }
    await page
      .getByText(
        `证据 problem-1-1 · ${mode === "abstract" ? "摘要" : "第 2 页"}`,
      )
      .click();
    await expect(
      page.getByText("The retrieval method addresses accuracy."),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "查看 arXiv 原文", exact: true }),
    ).toHaveAttribute("href", reportMessage(mode).citations[0].url);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page.reload();
    await expect(
      page.getByRole("button", { name: "重新生成论文报告" }),
    ).toBeEnabled();
    expect(writes).toHaveLength(1);
    expect(api.requestsFor("POST", `${documentPath}/prepare`)).toHaveLength(0);
    await expect(page.getByRole("alert")).not.toBeVisible();
  });
}

test("a running follow-up publishes its reply without refreshing or submitting again", async ({
  page,
  api,
}) => {
  let completed = false;
  api.on("GET", conversationPath, (route) =>
    route.fulfill({
      json: {
        ...conversation,
        paper_report_ready: true,
        ...(completed ? {} : { active_run_id: "followup-run" }),
      },
    }),
  );
  api.on("GET", `${conversationPath}/messages`, (route) =>
    route.fulfill({
      json: {
        items: [
          reportMessage("fulltext"),
          {
            id: 3,
            run_id: "followup-run",
            role: "user",
            content: "实验有什么条件？",
            citations: [],
          },
          ...(completed
            ? [
                {
                  id: 4,
                  run_id: "followup-run",
                  role: "assistant",
                  content: "论文使用独立测试集验证方法。",
                  citations: [],
                },
              ]
            : []),
        ],
        next_before: 0,
      },
    }),
  );
  api.json("GET", `${conversationPath}/paper-report`, {
    report: reportMessage("fulltext"),
    matches_current_paper: true,
  });
  api.on("GET", "/agent/runs/followup-run", (route) =>
    route.fulfill({
      json: {
        run: {
          id: "followup-run",
          task: "paper_followup",
          state: completed ? "completed" : "running",
          progress: completed ? "completed" : "generating",
        },
        steps: [],
      },
    }),
  );

  await page.goto(assistantURL);
  await expect(page.getByRole("button", { name: "停止本轮" })).toBeVisible();
  completed = true;
  await expect(
    page.getByText("论文使用独立测试集验证方法。", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "停止本轮" }),
  ).not.toBeVisible();
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual(
    [],
  );
  await expect(page.getByRole("alert")).not.toBeVisible();
});

function historyMessage(id: number, content = `历史消息 ${id}`) {
  return {
    id,
    conversation_id: conversation.id,
    run_id: `history-run-${id}`,
    role: id % 2 ? "user" : "assistant",
    content,
    citations: [],
    provider: credential.provider,
    model: credential.model,
  };
}

function historyMessages(first: number, count: number) {
  return Array.from({ length: count }, (_, index) =>
    historyMessage(first + index),
  );
}

test("the report remains available when it is outside the newest 50 messages", async ({
  page,
  api,
}) => {
  api.json("GET", conversationPath, {
    ...conversation,
    paper_report_ready: true,
  });
  api.json("GET", `${conversationPath}/messages`, {
    items: historyMessages(51, 50),
    next_before: 51,
  });
  api.json("GET", `${conversationPath}/paper-report`, {
    report: reportMessage("fulltext"),
    matches_current_paper: true,
  });
  api.json("GET", "/agent/runs/history-run-100", {
    run: { id: "history-run-100", task: "paper_followup", state: "completed", progress: "completed" },
    steps: [],
  });

  await page.goto(assistantURL);
  const reportRegion = page.getByLabel("论文报告", { exact: true });
  await expect(reportRegion).toBeVisible();
  await expect(
    reportRegion.getByRole("heading", { name: "论文问题", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "复制 JSON" })).toHaveCount(1);
  await expect(page.locator("[data-message-id]")).toHaveCount(50);
  await expect(page.getByText("历史消息 100", { exact: true })).toBeAttached();
  await expect(page.getByRole("textbox", { name: "你的问题" })).toBeEnabled();
  expect(api.requestsFor("GET", `${conversationPath}/paper-report`).length).toBeGreaterThan(0);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
  await expect(page.getByRole("alert")).not.toBeVisible();
});

test("a stale report is readable without blocking a new question", async ({
  page,
  api,
}) => {
  api.json("GET", `${conversationPath}/paper-report`, {
    report: reportMessage("fulltext"),
    matches_current_paper: false,
  });
  await page.goto(assistantURL);
  await expect(page.getByLabel("论文报告", { exact: true })).toBeVisible();
  await expect(
    page.getByText("这份报告对应旧版论文材料，可按需重新生成。新问题会使用当前材料。", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.getByRole("textbox", { name: "你的问题" })).toBeEnabled();
  await page.getByRole("textbox", { name: "你的问题" }).fill("当前版本的方法有什么变化？");
  await expect(page.getByRole("button", { name: "发送", exact: true })).toBeEnabled();
  await expect(
    page.getByRole("button", { name: "重新生成论文报告" }),
  ).toBeEnabled();
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

test("conversation history loads beyond 20 entries and repeated clicks request one page", async ({
  page,
  api,
}) => {
  const firstPage = [
    conversation,
    ...Array.from({ length: 19 }, (_, index) => ({
      ...conversation,
      id: `history-conversation-${index + 2}`,
      title: `历史对话 ${index + 2}`,
    })),
  ];
  const secondPage = Array.from({ length: 3 }, (_, index) => ({
    ...conversation,
    id: `history-conversation-${index + 21}`,
    title: `历史对话 ${index + 21}`,
  }));
  const started = deferred();
  const release = deferred();
  let secondPageReads = 0;
  api.on("GET", "/agent/conversations", async (route, url) => {
    const currentPage = Number(url.searchParams.get("page") ?? "1");
    expect(currentPage).toBeGreaterThanOrEqual(1);
    expect(currentPage).toBeLessThanOrEqual(2);
    if (currentPage === 2) {
      secondPageReads++;
      started.resolve();
      await release.promise;
    }
    await route.fulfill({
      json: {
        items: currentPage === 1 ? firstPage : secondPage,
        page: currentPage,
        has_more: currentPage === 1,
        next_page: currentPage === 1 ? 2 : 0,
      },
    });
  });

  await page.goto(assistantURL);
  await page.getByRole("button", { name: "助手设置" }).click();
  const history = page.getByRole("combobox", { name: "历史对话", exact: true });
  await expect(history.locator("option")).toHaveCount(21);
  const more = page.getByRole("button", { name: "加载更多对话", exact: true });
  try {
    await more.evaluate((button: HTMLButtonElement) => {
      button.click();
      button.click();
    });
    await started.promise;
    await expect(
      page.getByRole("button", { name: "正在加载对话…", exact: true }),
    ).toBeDisabled();
    expect(secondPageReads).toBe(1);
  } finally {
    release.resolve();
  }
  await expect(history.locator("option")).toHaveCount(24);
  await expect(history.locator('option[value="history-conversation-23"]')).toHaveCount(1);
  await expect(more).toHaveCount(0);
  await expect(history).toHaveValue(conversation.id);
  expect(secondPageReads).toBe(1);
  await expect(page.getByRole("alert")).not.toBeVisible();
});

test("a late history response from conversation A cannot alter conversation B", async ({
  page,
  api,
}) => {
  const other = {
    ...conversation,
    id: "conversation-two",
    title: "另一段论文对话",
    paper_report_ready: true,
  };
  const otherPath = `/agent/conversations/${other.id}`;
  const started = deferred();
  const release = deferred();
  const settled = deferred();
  api.json("GET", "/agent/conversations", {
    items: [conversation, other],
    page: 1,
    has_more: false,
    next_page: 0,
  });
  api.on("GET", `${conversationPath}/messages`, async (route, url) => {
    if (!url.searchParams.has("before")) {
      await route.fulfill({
        json: { items: [historyMessage(100, "会话 A 的当前消息")], next_before: 100 },
      });
      return;
    }
    expect(url.searchParams.get("before")).toBe("100");
    started.resolve();
    await release.promise;
    try {
      await route.fulfill({
        json: { items: [historyMessage(50, "会话 A 的迟到历史")], next_before: 50 },
      });
    } finally {
      settled.resolve();
    }
  });
  api.json("GET", otherPath, other);
  api.json("GET", `${otherPath}/messages`, {
    items: [{ ...historyMessage(200, "会话 B 的独立消息"), conversation_id: other.id }],
    next_before: 0,
  });
  api.json("GET", `${otherPath}/paper-report`, {
    report: { ...reportMessage("fulltext"), id: 190, conversation_id: other.id },
    matches_current_paper: true,
  });
  for (const id of [100, 200]) {
    api.json("GET", `/agent/runs/history-run-${id}`, {
      run: { id: `history-run-${id}`, task: "paper_followup", state: "completed", progress: "completed" },
      steps: [],
    });
  }

  await page.goto(assistantURL);
  await expect(page.getByText("会话 A 的当前消息", { exact: true })).toBeAttached();
  try {
    await page.getByRole("button", { name: "加载更早消息", exact: true }).click();
    await started.promise;
    await page.getByRole("button", { name: "助手设置" }).click();
    await page.getByRole("combobox", { name: "历史对话", exact: true }).selectOption(other.id);
    await expect(page.getByText("会话 B 的独立消息", { exact: true })).toBeAttached();
    await expect(page.getByRole("textbox", { name: "你的问题" })).toBeEnabled();
  } finally {
    release.resolve();
  }
  await settled.promise;
  await expect(page.getByRole("combobox", { name: "历史对话", exact: true })).toHaveValue(other.id);
  await expect(page.getByText("会话 A 的当前消息", { exact: true })).toHaveCount(0);
  await expect(page.getByText("会话 A 的迟到历史", { exact: true })).toHaveCount(0);
  await expect(page.getByText("会话 B 的独立消息", { exact: true })).toBeAttached();
  await expect(page.getByRole("button", { name: "加载更早消息", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "正在加载消息…", exact: true })).toHaveCount(0);
  await expect(page.getByRole("alert")).not.toBeVisible();
});

test("poll completion preserves loaded history, its cursor, and the reading anchor", async ({
  page,
  api,
}) => {
  let completed = false;
  const started = deferred();
  const release = deferred();
  const olderCursors: string[] = [];
  api.on("GET", conversationPath, (route) =>
    route.fulfill({
      json: {
        ...conversation,
        paper_report_ready: true,
        ...(completed ? {} : { active_run_id: "history-followup" }),
      },
    }),
  );
  api.json("GET", `${conversationPath}/paper-report`, {
    report: reportMessage("fulltext"),
    matches_current_paper: true,
  });
  api.on("GET", `${conversationPath}/messages`, async (route, url) => {
    const before = url.searchParams.get("before");
    if (before) {
      olderCursors.push(before);
      expect(["102", "52"]).toContain(before);
      if (before === "102") {
        started.resolve();
        await release.promise;
      }
      await route.fulfill({
        json: before === "102"
          ? { items: historyMessages(52, 50), next_before: 52 }
          : { items: historyMessages(3, 49), next_before: 0 },
      });
      return;
    }
    const latest = historyMessages(completed ? 103 : 102, completed ? 48 : 49);
    latest.push({ ...historyMessage(151, "请分析实验条件。"), run_id: "history-followup" });
    if (completed) latest.push({ ...historyMessage(152, "完成后的新增回答。"), run_id: "history-followup" });
    await route.fulfill({
      json: { items: latest, next_before: completed ? 103 : 102 },
    });
  });
  api.on("GET", "/agent/runs/history-followup", (route) =>
    route.fulfill({
      json: {
        run: {
          id: "history-followup",
          task: "paper_followup",
          state: completed ? "completed" : "running",
          progress: completed ? "completed" : "generating",
        },
        steps: [],
      },
    }),
  );

  await page.goto(assistantURL);
  await expect(page.getByRole("button", { name: "停止本轮" })).toBeVisible();
  const earlier = page.getByRole("button", { name: "加载更早消息", exact: true });
  const anchor = page.locator('[data-message-id="102"]');
  let originalTop = 0;
  try {
    // Scroll as a reader would before clicking; no dependency on a CSS class.
    await earlier.scrollIntoViewIfNeeded();
    await earlier.evaluate((button: HTMLButtonElement) => {
      button.click();
      button.click();
    });
    await started.promise;
    await expect(
      page.getByRole("button", { name: "正在加载消息…", exact: true }),
    ).toBeDisabled();
    const box = await anchor.boundingBox();
    expect(box).not.toBeNull();
    originalTop = box!.y;
    expect(olderCursors).toEqual(["102"]);
  } finally {
    release.resolve();
  }
  await expect(page.locator('[data-message-id="52"]')).toBeAttached();
  await expect(page.locator("[data-message-id]")).toHaveCount(100);
  await expect.poll(async () => Math.abs((await anchor.boundingBox())!.y - originalTop)).toBeLessThan(3);

  completed = true;
  await expect(page.getByText("完成后的新增回答。", { exact: true })).toBeAttached();
  await expect(page.getByRole("button", { name: "停止本轮" })).toHaveCount(0);
  await expect(page.locator('[data-message-id="52"]')).toBeAttached();
  await expect(page.locator("[data-message-id]")).toHaveCount(101);
  await expect.poll(async () => Math.abs((await anchor.boundingBox())!.y - originalTop)).toBeLessThan(3);
  await expect(page.getByRole("button", { name: "复制 JSON" })).toHaveCount(1);
  await earlier.click();
  await expect(page.locator('[data-message-id="3"]')).toBeAttached();
  await expect(page.locator("[data-message-id]")).toHaveCount(150);
  expect(olderCursors).toEqual(["102", "52"]);
  await expect(earlier).toHaveCount(0);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
  await expect(page.getByRole("alert")).not.toBeVisible();
});

test("creating a conversation sends its first report once and survives its own navigation", async ({
  page,
  api,
}) => {
  let generated = false;
  const started = deferred();
  const release = deferred();
  api.json("POST", "/agent/conversations", conversation, 201);
  api.on("GET", conversationPath, (route) =>
    route.fulfill({ json: { ...conversation, paper_report_ready: generated } }),
  );
  api.on("GET", `${conversationPath}/paper-report`, (route) =>
    route.fulfill({
      json: {
        report: generated ? reportMessage("fulltext") : null,
        matches_current_paper: generated,
      },
    }),
  );
  api.on("GET", `${conversationPath}/messages`, (route) =>
    route.fulfill({
      json: { items: generated ? [reportMessage("fulltext")] : [], next_before: 0 },
    }),
  );
  api.on("POST", `${conversationPath}/messages`, async (route) => {
    started.resolve();
    await release.promise;
    generated = true;
    await route.fulfill({
      status: 202,
      json: {
        run_id: "report-run",
        run: { id: "report-run", task: "paper_report", state: "pending", progress: "queued" },
      },
    });
  });
  api.json("GET", "/agent/runs/report-run", {
    run: { id: "report-run", task: "paper_report", state: "completed", progress: "completed" },
    steps: [],
  });

  await page.goto("/papers?paper_id=1&assistant=paper");
  const generate = page.getByRole("button", { name: "生成论文报告", exact: true });
  await expect(generate).toBeEnabled();
  try {
    await generate.evaluate((button: HTMLButtonElement) => {
      button.click();
      button.click();
    });
    await started.promise;
    expect(api.requestsFor("POST", "/agent/conversations")).toHaveLength(1);
    expect(api.requestsFor("POST", `${conversationPath}/messages`)).toHaveLength(1);
  } finally {
    release.resolve();
  }
  await expect(page).toHaveURL(/conversation=conversation-one/);
  await expect(page.getByLabel("论文报告", { exact: true })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "你的问题" })).toBeEnabled();
  expect(api.requestsFor("POST", "/agent/conversations")).toHaveLength(1);
  expect(api.requestsFor("POST", `${conversationPath}/messages`)).toHaveLength(1);
  await expect(page.getByRole("alert")).not.toBeVisible();
});

test("opening a paper without a conversation only reads its document status", async ({
  page,
  api,
}) => {
  // Document preparation does not depend on a configured model or a chat.
  api.json("GET", "/ai/credentials", { items: [] });
  api.json("GET", "/agent/conversations", {
    items: [],
    page: 1,
    has_more: false,
    next_page: 0,
  });
  await page.goto("/papers?paper_id=1&assistant=paper");
  const material = page.getByRole("region", { name: "论文全文材料", exact: true });
  await expect(material.getByRole("status")).toHaveText("全文尚未准备");
  await expect(
    material.getByText("仅准备论文材料，不调用 AI 模型。", { exact: true }),
  ).toBeVisible();
  await expect(
    material.getByRole("button", { name: "准备全文", exact: true }),
  ).toBeEnabled();
  expect(api.requestsFor("GET", documentPath).length).toBeGreaterThan(0);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
  expect(
    api.requests.filter((request) =>
      /\/agent\/(runs\/|conversations\/[^/]+\/)/.test(request.path),
    ),
  ).toEqual([]);
  await expect(page).not.toHaveURL(/conversation=/);
  await expect(page.getByRole("alert")).not.toBeVisible();
});

test("explicit document preparation ignores an older read and polls until usable", async ({
  page,
  api,
}) => {
  let phase = "not_prepared";
  const initialRead = deferred();
  const releaseInitial = deferred();
  const initialFinishes: Promise<void>[] = [];
  const snapshot = (state: string) => ({
    ...documentStatus,
    state,
    usable: state === "ready",
    document_id: state === "not_prepared" ? "" : "prepared-document-one",
    page_count: state === "ready" ? 12 : 0,
  });
  api.on("GET", documentPath, async (route) => {
    const stateAtRequest = phase;
    if (stateAtRequest === "not_prepared") {
      const finished = deferred();
      initialFinishes.push(finished.promise);
      initialRead.resolve();
      await releaseInitial.promise;
      try {
        await route.fulfill({ json: snapshot(stateAtRequest) });
      } finally {
        finished.resolve();
      }
      return;
    }
    await route.fulfill({ json: snapshot(stateAtRequest) });
  });
  api.on("POST", `${documentPath}/prepare`, (route) => {
    phase = "pending";
    return route.fulfill({ status: 202, json: snapshot(phase) });
  });

  await page.goto("/papers?paper_id=1&assistant=paper");
  const material = page.getByRole("region", { name: "论文全文材料", exact: true });
  try {
    await initialRead.promise;
    const prepare = material.getByRole("button", { name: "准备全文", exact: true });
    await expect(prepare).toBeEnabled();
    expect(api.requestsFor("POST", `${documentPath}/prepare`)).toHaveLength(0);
    await prepare.evaluate((button: HTMLButtonElement) => {
      button.click();
      button.click();
    });
    await expect(material.getByRole("status")).toHaveText("全文等待解析");
    await expect(
      material.getByRole("button", { name: "正在准备全文…", exact: true }),
    ).toBeDisabled();
  } finally {
    releaseInitial.resolve();
  }
  await Promise.all(initialFinishes);
  await expect(material.getByRole("status")).toHaveText("全文等待解析");
  phase = "processing";
  await expect(material.getByRole("status")).toHaveText("正在解析全文");
  phase = "ready";
  await expect(material.getByRole("status")).toHaveText("全文已就绪");
  await expect(
    material.getByText("下次提问可使用全文。", { exact: true }),
  ).toBeVisible();
  await expect(material.getByRole("button")).toHaveCount(0);
  expect(api.requestsFor("POST", `${documentPath}/prepare`)).toHaveLength(1);
  expect(api.requests.filter((request) => request.method !== "GET").map((request) => request.path)).toEqual([
    `/api/v2${documentPath}/prepare`,
  ]);
  await expect(page).not.toHaveURL(/conversation=/);
  await expect(page.getByRole("alert")).not.toBeVisible();
});

test("failed document preparation only retries after an explicit click", async ({
  page,
  api,
}) => {
  let state = "failed";
  const snapshot = () => ({
    ...documentStatus,
    state,
    usable: state === "ready",
    document_id: "retry-document-one",
    page_count: state === "ready" ? 8 : 0,
    ...(state === "failed" ? { failure_code: "ocr_required" } : {}),
  });
  api.on("GET", documentPath, (route) => route.fulfill({ json: snapshot() }));
  api.on("POST", `${documentPath}/prepare`, (route) => {
    state = "pending";
    return route.fulfill({ status: 202, json: snapshot() });
  });
  await page.goto("/papers?paper_id=1&assistant=paper");
  const material = page.getByRole("region", { name: "论文全文材料", exact: true });
  await expect(material.getByRole("status")).toHaveText("全文准备失败");
  const retry = material.getByRole("button", { name: "重试解析", exact: true });
  await expect(retry).toBeEnabled();
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
  await retry.click();
  await expect(material.getByRole("status")).toHaveText("全文等待解析");
  state = "ready";
  await expect(material.getByRole("status")).toHaveText("全文已就绪");
  await expect(material.getByText("下次提问可使用全文。", { exact: true })).toBeVisible();
  await expect(retry).toHaveCount(0);
  expect(api.requestsFor("POST", `${documentPath}/prepare`)).toHaveLength(1);
  expect(api.requests.filter((request) => request.method !== "GET").map((request) => request.path)).toEqual([
    `/api/v2${documentPath}/prepare`,
  ]);
  await expect(page.getByRole("alert")).not.toBeVisible();
});

test("a late document status for paper A cannot replace paper B's material state", async ({
  page,
  api,
}) => {
  const otherPaper = { ...paper, id: 2, title: "Reliable Retrieval" };
  const started = deferred();
  const release = deferred();
  const finishes: Promise<void>[] = [];
  api.json("GET", "/papers", {
    items: [paper, otherPaper],
    total: 2,
    page: 1,
    page_size: 20,
  });
  api.json("GET", "/papers/2", otherPaper);
  api.json("GET", "/agent/conversations", {
    items: [],
    page: 1,
    has_more: false,
    next_page: 0,
  });
  api.json("GET", "/agent/papers/2/document", documentStatus);
  api.on("GET", documentPath, async (route) => {
    const finished = deferred();
    finishes.push(finished.promise);
    started.resolve();
    await release.promise;
    try {
      await route.fulfill({
        json: {
          ...documentStatus,
          state: "ready",
          usable: true,
          document_id: "late-document-for-paper-one",
          page_count: 99,
        },
      });
    } finally {
      finished.resolve();
    }
  });
  await page.goto("/papers?paper_id=1&assistant=paper");
  const material = page.getByRole("region", { name: "论文全文材料", exact: true });
  try {
    await started.promise;
    await page.getByRole("button", { name: "返回论文列表", exact: true }).click();
    await page.getByRole("button", { name: otherPaper.title, exact: true }).click();
    await page.getByRole("button", { name: "AI 论文助手", exact: true }).click();
    await expect(page).toHaveURL(/paper_id=2/);
    await expect(material.getByRole("status")).toHaveText("全文尚未准备");
  } finally {
    release.resolve();
  }
  await Promise.all(finishes);
  await expect(material.getByRole("status")).toHaveText("全文尚未准备");
  await expect(material.getByRole("button", { name: "准备全文", exact: true })).toBeEnabled();
  await expect(material.getByText("全文已就绪", { exact: true })).toHaveCount(0);
  expect(api.requestsFor("GET", "/agent/papers/2/document").length).toBeGreaterThan(0);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
  await expect(page.getByRole("alert")).not.toBeVisible();
});

for (const fallbackReason of ["document_timeout", "document_download_failed"] as const) {
  test(`a first question needs no report, keeps its ${fallbackReason} answer, and uses ready text on the next question`, async ({ page, api }) => {
    const questions = ["这篇论文解决了什么问题？", "请进一步说明实验条件。"];
    const answers = ["摘要回答：当前材料说明方法用于检索。", "全文回答：实验包含独立验证集。"];
    const submissions: Record<string, unknown>[] = [];
    let materialState = "not_prepared";
    const materialSnapshot = () => ({
      ...documentStatus,
      state: materialState,
      usable: materialState === "ready",
      document_id: materialState === "not_prepared" ? "" : "question-document",
      page_count: materialState === "ready" ? 12 : 0,
      ...(materialState === "failed" ? { failure_code: fallbackReason } : {}),
    });
    api.on("GET", documentPath, (route) => route.fulfill({ json: materialSnapshot() }));
    api.on("POST", `${documentPath}/prepare`, (route) => {
      materialState = "ready";
      return route.fulfill({ status: 200, json: materialSnapshot() });
    });
    api.json("POST", "/agent/conversations", conversation, 201);
    api.on("GET", `${conversationPath}/messages`, (route) => route.fulfill({
      json: {
        items: submissions.flatMap((_, index) => [
          {
            id: index * 2 + 1,
            run_id: `question-run-${index + 1}`,
            role: "user",
            content: questions[index],
            citations: [],
          },
          questionReply(index + 1, index === 0 ? "abstract" : "fulltext", answers[index], index === 0 ? fallbackReason : undefined),
        ]),
        next_before: 0,
      },
    }));
    api.on("POST", `${conversationPath}/messages`, (route) => {
      submissions.push(route.request().postDataJSON());
      expect(submissions.length).toBeLessThanOrEqual(2);
      if (submissions.length === 1)
        materialState = fallbackReason === "document_timeout" ? "pending" : "failed";
      return route.fulfill({
        status: 202,
        json: {
          run_id: `question-run-${submissions.length}`,
          run: { id: `question-run-${submissions.length}`, task: "paper_followup", state: "pending", progress: "queued" },
        },
      });
    });
    for (const round of [1, 2]) {
      api.json("GET", `/agent/runs/question-run-${round}`, {
        run: {
          id: `question-run-${round}`,
          task: "paper_followup",
          state: "completed",
          progress: "completed",
          effective_context_mode: round === 1 ? "abstract" : "fulltext",
          ...(round === 1 ? { fallback_reason: fallbackReason } : {}),
        },
        steps: [],
      });
    }

    await page.goto("/papers?paper_id=1&assistant=paper");
    const input = page.getByRole("textbox", { name: "你的问题" });
    const send = page.getByRole("button", { name: "发送", exact: true });
    const material = page.getByRole("region", { name: "论文全文材料", exact: true });
    await expect(material.getByRole("status")).toHaveText("全文尚未准备");
    await expect(input).toBeEnabled();
    await input.fill(questions[0]);
    await send.click();
    await expect(page).toHaveURL(/conversation=conversation-one/);
    const firstReply = page.locator('[data-message-id="2"]');
    await expect(firstReply.getByText(answers[0], { exact: true })).toBeVisible();
    await expect(firstReply.getByText(/仅基于摘要/)).toBeVisible();
    await expect(firstReply.getByText(fallbackReason === "document_timeout" ? /全文准备超时/ : /PDF 下载失败/)).toBeVisible();
    await expect(page.getByRole("region", { name: "论文报告", exact: true })).toHaveCount(0);
    expect(submissions).toHaveLength(1);
    expect(submissions[0]).toMatchObject({
      task: "paper_followup",
      question: questions[0],
      context_mode: "fulltext",
      credential_id: credential.id,
      idempotency_key: expect.any(String),
    });
    expect(api.requestsFor("POST", "/agent/conversations")).toHaveLength(1);
    if (fallbackReason === "document_timeout") {
      await expect(material.getByRole("status")).toHaveText("全文等待解析");
      materialState = "ready";
    } else {
      await expect(material.getByRole("status")).toHaveText("全文准备失败");
      await material.getByRole("button", { name: "重试解析", exact: true }).click();
    }
    await expect(material.getByRole("status")).toHaveText("全文已就绪");
    expect(submissions).toHaveLength(1);
    await expect(firstReply.getByText(answers[0], { exact: true })).toBeVisible();
    await expect(firstReply.getByText(/仅基于摘要/)).toBeVisible();

    await input.fill(questions[1]);
    await send.click();
    const secondReply = page.locator('[data-message-id="4"]');
    await expect(secondReply.getByText(answers[1], { exact: true })).toBeVisible();
    await expect(secondReply.getByText(/基于论文提取文字/)).toBeVisible();
    await expect(firstReply.getByText(/仅基于摘要/)).toBeAttached();
    expect(submissions).toHaveLength(2);
    expect(submissions[1]).toMatchObject({ task: "paper_followup", question: questions[1], context_mode: "fulltext" });
    expect(submissions[1].idempotency_key).not.toBe(submissions[0].idempotency_key);
    expect(api.requestsFor("POST", "/agent/conversations")).toHaveLength(1);
    expect(api.requestsFor("POST", `${documentPath}/prepare`)).toHaveLength(fallbackReason === "document_timeout" ? 0 : 1);
    await expect(page.getByRole("alert")).not.toBeVisible();
  });
}

test("retrying a first question after a lost response reuses its conversation and idempotency key", async ({ page, api }) => {
  const question = "请解释核心方法。";
  const submissions: Record<string, unknown>[] = [];
  const accepted = new Set<string>();
  api.json("POST", "/agent/conversations", conversation, 201);
  api.on("POST", `${conversationPath}/messages`, async (route) => {
    const body = route.request().postDataJSON();
    submissions.push(body);
    accepted.add(body.idempotency_key);
    if (submissions.length === 1) {
      // The server accepted the task, but its acknowledgement was lost.
      await route.abort("failed");
      return;
    }
    await route.fulfill({
      status: 202,
      json: { run_id: "question-run-1", run: { id: "question-run-1", task: "paper_followup", state: "pending", progress: "queued" } },
    });
  });
  api.json("GET", `${conversationPath}/messages`, {
    items: [
      { id: 1, run_id: "question-run-1", role: "user", content: question, citations: [] },
      questionReply(1, "abstract", "同一个任务返回的摘要回答。", "document_timeout"),
    ],
    next_before: 0,
  });
  api.json("GET", "/agent/runs/question-run-1", {
    run: { id: "question-run-1", task: "paper_followup", state: "completed", progress: "completed", effective_context_mode: "abstract", fallback_reason: "document_timeout" },
    steps: [],
  });

  await page.goto("/papers?paper_id=1&assistant=paper");
  const input = page.getByRole("textbox", { name: "你的问题" });
  const send = page.getByRole("button", { name: "发送", exact: true });
  await input.fill(question);
  await send.click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(input).toHaveValue(question);
  expect(submissions).toHaveLength(1);
  await expect(send).toBeEnabled();
  await send.click();
  await expect(page).toHaveURL(/conversation=conversation-one/);
  await expect(page.getByText("同一个任务返回的摘要回答。", { exact: true })).toBeVisible();
  expect(api.requestsFor("POST", "/agent/conversations")).toHaveLength(1);
  expect(submissions).toHaveLength(2);
  expect(submissions[0]).toMatchObject({ task: "paper_followup", question, idempotency_key: expect.any(String) });
  expect(submissions[0].idempotency_key).not.toBe("");
  expect(submissions[1]).toEqual(submissions[0]);
  expect(accepted.size).toBe(1);
  await expect(page.locator('[data-message-id="1"]')).toHaveCount(1);
  await expect(page.locator('[data-message-id="2"]')).toHaveCount(1);
  await expect(page.getByRole("region", { name: "论文报告", exact: true })).toHaveCount(0);
  await expect(page.getByRole("alert")).not.toBeVisible();
});

for (const diagnostic of [
  {
    name: "claim count",
    detail: { path: "$.claims", rule: "", count: 9, limit: 8, unit: "claims" },
    expected: "结论 9 条，上限 8 条",
  },
  {
    name: "UTF-8 byte count",
    detail: { path: "$.claims[0].text", rule: "", count: 1350, limit: 1200, unit: "utf8_bytes" },
    expected: "文字 1350 UTF-8 字节，上限 1200 字节",
  },
  {
    name: "complete response byte count",
    detail: { path: "$", rule: "", count: 50001, limit: 50000, unit: "utf8_bytes" },
    expected: "完整响应 50001 UTF-8 字节，上限 50000 字节",
  },
  {
    name: "legacy missing counts",
    detail: { path: "$.claims", rule: "" },
    expected: undefined,
  },
]) {
  test(`paper output diagnostics display ${diagnostic.name} without submitting again`, async ({ page, api }) => {
    const runID = "failed-report-run";
    api.json("GET", `${conversationPath}/messages`, {
      items: [{ id: 1, run_id: runID, role: "user", content: "生成论文报告", citations: [] }],
      next_before: 0,
    });
    api.json("GET", `/agent/runs/${runID}`, {
      run: {
        id: runID,
        task: "paper_report",
        state: "failed",
        progress: "analyzing_results",
        failure_code: "output_limit_exceeded",
        failure_detail: { code: "output_limit_exceeded", ...diagnostic.detail },
      },
      steps: [{ tool: "analyzing_results", call_id: "fixture-failed-call", failure_code: "output_limit_exceeded" }],
    });

    await page.goto(assistantURL);
    await expect(page.getByText("模型输出的条目数量或文字长度超出上限。", { exact: true })).toBeVisible();
    await page.getByText("失败详情", { exact: true }).click();
    await expect(page.getByText("失败步骤：分析主要结果", { exact: true })).toBeVisible();
    await expect(page.getByText(`校验位置：${diagnostic.detail.path}`, { exact: true })).toBeVisible();
    if (diagnostic.expected)
      await expect(page.getByText(diagnostic.expected, { exact: true })).toBeVisible();
    else
      await expect(page.getByText(/(?:结论|文字|完整响应) \d+.*上限/)).toHaveCount(0);
    await expect(page.getByText("诊断编号：fixture-failed-call", { exact: true })).toBeVisible();
    await expect(page.getByRole("region", { name: "论文报告", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "生成论文报告", exact: true })).toBeEnabled();
    await page.reload();
    await expect(page.getByText("模型输出的条目数量或文字长度超出上限。", { exact: true })).toBeVisible();
    expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
  });
}

test("paper evidence review shows persisted batch progress and publishes no partial report", async ({ page, api }) => {
  const runID = "reviewing-report-run";
  let completed = 1;
  api.json("GET", conversationPath, { ...conversation, active_run_id: runID });
  api.on("GET", `/agent/runs/${runID}`, (route) =>
    route.fulfill({
      json: {
        run: {
          id: runID,
          task: "paper_report",
          state: "running",
          progress: "validating_paper",
          review_progress: { completed, total: 3 },
        },
        steps: [],
      },
    }),
  );

  await page.goto(assistantURL);
  await expect(page.getByText("正在审核证据 2/3", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "生成论文报告", exact: true })).toBeDisabled();
  await expect(page.getByRole("region", { name: "论文报告", exact: true })).toHaveCount(0);
  await page.reload();
  await expect(page.getByText("正在审核证据 2/3", { exact: true })).toBeVisible();
  completed = 2;
  await expect(page.getByText("正在审核证据 3/3", { exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "论文报告", exact: true })).toHaveCount(0);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

test("one paper repair shows safe pending, calling, review, and completed summaries across reload", async ({ page, api }) => {
  const runID = "repairing-report-run";
  let phase: "pending" | "calling" | "reviewing" | "completed" = "pending";
  const repairedReport = { ...reportMessage("fulltext"), run_id: runID };
  api.on("GET", conversationPath, (route) =>
    route.fulfill({ json: { ...conversation, active_run_id: runID, paper_report_ready: phase === "completed" } }),
  );
  api.on("GET", `${conversationPath}/messages`, (route) =>
    route.fulfill({ json: { items: phase === "completed" ? [repairedReport] : [], next_before: 0 } }),
  );
  api.on("GET", `${conversationPath}/paper-report`, (route) =>
    route.fulfill({ json: { report: phase === "completed" ? repairedReport : null, matches_current_paper: phase === "completed" } }),
  );
  api.on("GET", `/agent/runs/${runID}`, (route) =>
    route.fulfill({
      json: {
        run: {
          id: runID,
          task: "paper_report",
          state: phase === "completed" ? "completed" : "running",
          progress: phase === "pending" ? "waiting_for_model_slot" : phase === "calling" ? "repairing_results" : phase === "reviewing" ? "validating_paper" : "completed",
          repair_summary: { field: "results", state: phase === "reviewing" ? "completed" : phase, attempted: phase !== "pending" },
          ...(phase === "reviewing" ? { review_progress: { completed: 1, total: 3 } } : {}),
        },
        steps: [{ tool: "analyzing_results", failure_code: "output_limit_exceeded", call_id: "original-output" }],
      },
    }),
  );

  await page.goto(assistantURL);
  await expect(page.getByText("正在等待模型调用空闲", { exact: true })).toBeVisible();
  await expect(page.getByText("已安排一次自动整理：主要结果。", { exact: true })).toBeVisible();
  phase = "calling";
  await expect(page.getByText("正在整理主要结果", { exact: true })).toBeVisible();
  await expect(page.getByText("本轮正在进行唯一一次自动整理。", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "生成论文报告", exact: true })).toBeDisabled();
  await expect(page.getByRole("region", { name: "论文报告", exact: true })).toHaveCount(0);
  await page.reload();
  await expect(page.getByText("正在整理主要结果", { exact: true })).toBeVisible();
  phase = "reviewing";
  await expect(page.getByText("正在审核证据 2/3", { exact: true })).toBeVisible();
  await expect(page.getByText("已完成一次自动整理：主要结果。", { exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "论文报告", exact: true })).toHaveCount(0);
  phase = "completed";
  await expect(page.getByRole("region", { name: "论文报告", exact: true })).toBeVisible();
  await expect(page.getByText("本轮已完成", { exact: true })).toBeVisible();
  await expect(page.getByText("已完成一次自动整理：主要结果。", { exact: true })).toBeVisible();
  await expect(page.getByText("失败详情", { exact: true })).toHaveCount(0);
  await expect(page.getByText("模型输出的条目数量或文字长度超出上限。", { exact: true })).toHaveCount(0);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

test("an oversized repair request keeps the original limit diagnostic and explains that repair did not start", async ({ page, api }) => {
  const runID = "repair-budget-run";
  api.json("GET", `${conversationPath}/messages`, {
    items: [{ id: 1, run_id: runID, role: "user", content: "生成论文报告", citations: [] }],
    next_before: 0,
  });
  api.json("GET", `/agent/runs/${runID}`, {
    run: {
      id: runID,
      task: "paper_report",
      state: "failed",
      progress: "analyzing_results",
      failure_code: "output_limit_exceeded",
      failure_detail: { code: "output_limit_exceeded", path: "$.claims", rule: "", count: 9, limit: 8, unit: "claims" },
      repair_summary: { field: "results", state: "budget_exceeded", attempted: false },
    },
    steps: [{ tool: "analyzing_results", failure_code: "output_limit_exceeded", call_id: "original-output" }],
  });

  await page.goto(assistantURL);
  await expect(page.getByText("自动整理输入超出 64 KiB 预算，未发起整理。", { exact: true })).toBeVisible();
  await page.getByText("失败详情", { exact: true }).click();
  await expect(page.getByText("失败步骤：分析主要结果", { exact: true })).toBeVisible();
  await expect(page.getByText("结论 9 条，上限 8 条", { exact: true })).toBeVisible();
  await expect(page.getByText("本轮未发布结果，未进行自动整理。", { exact: true })).toBeVisible();
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

for (const failure of [
  { code: "timeout", attempted: true, message: "模型响应超时，本次调用未重试。" },
  { code: "access_or_configuration_changed", attempted: true, message: "论文访问权限或模型配置已变化。" },
  { code: "access_or_configuration_changed", attempted: false, message: "论文访问权限或模型配置已变化。" },
  { code: "result_unknown", attempted: true, message: "模型调用结果未知，本次调用未再次执行。你可以手动重新发送。" },
]) {
  test(`paper repair ${failure.code} (attempted ${failure.attempted}) shows the latest failure without stale limits`, async ({ page, api }) => {
    const runID = "repair-failed-run";
    api.json("GET", `${conversationPath}/messages`, {
      items: [{ id: 1, run_id: runID, role: "user", content: "生成论文报告", citations: [] }],
      next_before: 0,
    });
    api.json("GET", `/agent/runs/${runID}`, {
      run: {
        id: runID,
        task: "paper_report",
        state: ["timeout", "result_unknown"].includes(failure.code) ? "unknown" : "failed",
        progress: "repairing_results",
        failure_code: failure.code,
        failure_stage: "repairing_results",
        repair_summary: { field: "results", state: "failed", attempted: failure.attempted },
      },
      steps: [
        { tool: "analyzing_results", failure_code: "output_limit_exceeded", call_id: "original-output" },
        { tool: "repairing_results", failure_code: failure.code, call_id: "final-failure" },
      ],
    });

    await page.goto(assistantURL);
    await expect(page.getByText(failure.message, { exact: true })).toBeVisible();
    await expect(page.getByText(failure.attempted ? "本轮已尝试自动整理一次。" : "自动整理未开始。", { exact: true })).toBeVisible();
    await page.getByText("失败详情", { exact: true }).click();
    await expect(page.getByText("失败步骤：整理主要结果", { exact: true })).toBeVisible();
    await expect(page.getByText(`原因代码：${failure.code}`, { exact: true })).toBeVisible();
    await expect(page.getByText("诊断编号：final-failure", { exact: true })).toBeVisible();
    await expect(page.getByText(failure.attempted
      ? "本轮未发布结果；已尝试自动整理一次，系统不会再次自动整理。"
      : "本轮未发布结果，未进行自动整理。", { exact: true })).toBeVisible();
    await expect(page.getByText("模型输出的条目数量或文字长度超出上限。", { exact: true })).toHaveCount(0);
    await expect(page.getByText(/校验位置：|未自动重复调用模型|原始候选|original-output/)).toHaveCount(0);
    await expect(page.getByRole("region", { name: "论文报告", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "生成论文报告", exact: true })).toBeEnabled();
    expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
  });
}

test("unknown repair recovery uses its final failure stage when only the old limit step was persisted", async ({ page, api }) => {
  const runID = "repair-recovery-unknown";
  api.json("GET", `${conversationPath}/messages`, {
    items: [{ id: 1, run_id: runID, role: "user", content: "生成论文报告", citations: [] }],
    next_before: 0,
  });
  api.json("GET", `/agent/runs/${runID}`, {
    run: {
      id: runID,
      task: "paper_report",
      state: "unknown",
      progress: "unknown",
      failure_code: "result_unknown",
      failure_stage: "repairing_results",
      repair_summary: { field: "results", state: "failed", attempted: true },
    },
    steps: [{ tool: "analyzing_results", failure_code: "output_limit_exceeded", call_id: "old-call" }],
  });

  await page.goto(assistantURL);
  await expect(page.getByText("模型调用结果未知，本次调用未再次执行。你可以手动重新发送。", { exact: true })).toBeVisible();
  await page.getByText("失败详情", { exact: true }).click();
  await expect(page.getByText("失败步骤：整理主要结果", { exact: true })).toBeVisible();
  await expect(page.getByText("原因代码：result_unknown", { exact: true })).toBeVisible();
  await expect(page.getByText("本轮未发布结果；已尝试自动整理一次，系统不会再次自动整理。", { exact: true })).toBeVisible();
  await expect(page.getByText(/校验位置：|诊断编号：|失败步骤：分析主要结果|output_limit_exceeded/)).toHaveCount(0);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

test("a later output limit identifies its field while preserving the completed one-repair summary", async ({ page, api }) => {
  const runID = "second-limit-run";
  api.json("GET", `${conversationPath}/messages`, {
    items: [{ id: 1, run_id: runID, role: "user", content: "生成论文报告", citations: [] }],
    next_before: 0,
  });
  api.json("GET", `/agent/runs/${runID}`, {
    run: {
      id: runID,
      task: "paper_report",
      state: "failed",
      progress: "analyzing_limitations",
      failure_code: "output_limit_exceeded",
      failure_detail: { code: "output_limit_exceeded", path: "$.claims", rule: "", count: 7, limit: 6, unit: "claims" },
      repair_summary: { field: "results", state: "completed", attempted: true },
    },
    steps: [
      { tool: "analyzing_results", failure_code: "output_limit_exceeded", call_id: "original-output" },
      { tool: "repairing_results", call_id: "repaired-output" },
      { tool: "analyzing_limitations", failure_code: "output_limit_exceeded", call_id: "later-output" },
    ],
  });

  await page.goto(assistantURL);
  await expect(page.getByText("已完成一次自动整理：主要结果。", { exact: true })).toBeVisible();
  await page.getByText("失败详情", { exact: true }).click();
  await expect(page.getByText("失败步骤：分析局限性", { exact: true })).toBeVisible();
  await expect(page.getByText("结论 7 条，上限 6 条", { exact: true })).toBeVisible();
  await expect(page.getByText("诊断编号：later-output", { exact: true })).toBeVisible();
  await expect(page.getByText("本轮未发布结果；已尝试自动整理一次，系统不会再次自动整理。", { exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "论文报告", exact: true })).toHaveCount(0);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

function citationAnswer(id: number, quote = "Current source passage.") {
  return {
    ...questionReply(id / 2, "fulltext", "隐藏的旧版原始回答，不应覆盖结构化结果。"),
    id,
    run_id: `citation-run-${id}`,
    citations: [{
      id: "shared-source",
      page: 3,
      quote,
      url: "https://arxiv.org/pdf/1706.03762v1#page=3",
      document_id: "fixture-document",
      content_hash: "fixture-content-hash",
    }],
    result: {
      ...questionReply(id / 2, "fulltext", "").result,
      workflow_version: "paper-fixed-v10",
      answer: {
        status: "complete",
        parts: [{
          question_id: "q1",
          question: "这项方法如何工作？",
          status: "supported",
          claims: [{ text: "该方法由本轮论文原文支持。", citation_ids: ["shared-source"] }],
        }],
      },
    },
  };
}

function completedCitationRun(id: number) {
  return { run: { id: `citation-run-${id}`, task: "paper_followup", state: "completed", progress: "completed" }, steps: [] };
}

test("structured simple answers use native citation buttons to expand, focus, and scroll evidence with mouse and keyboard", async ({ page, api }) => {
  const message = citationAnswer(20, "<script>source text remains literal</script>");
  const literal = "<img src=x onerror=alert(1)> 本轮支持的回答。";
  message.result.answer.parts[0].claims = [
    { text: literal, citation_ids: ["shared-source"] },
    ...Array.from({ length: 5 }, () => ({ text: "条件与适用范围。".repeat(35), citation_ids: ["shared-source"] })),
  ];
  api.json("GET", `${conversationPath}/messages`, { items: [message], next_before: 0 });
  api.json("GET", "/agent/runs/citation-run-20", completedCitationRun(20));
  await page.goto(assistantURL);
  const reply = page.locator('[data-message-id="20"]');
  await expect(reply.getByText(literal, { exact: false })).toBeVisible();
  await expect(reply.getByRole("heading")).toHaveCount(0);
  await expect(reply.getByText(message.result.answer.parts[0].question, { exact: true })).toHaveCount(0);
  await expect(reply.getByText(message.content, { exact: true })).toHaveCount(0);
  await expect(reply.locator("img, script")).toHaveCount(0);
  const markers = reply.getByRole("button", { name: "查看证据 shared-source", exact: true });
  const marker = markers.first();
  const details = reply.locator("details");
  const summary = details.locator("summary");
  const target = await marker.getAttribute("aria-controls");
  expect(target).toContain(encodeURIComponent(JSON.stringify(["conversation-one:20", "shared-source"])));
  await expect(details).toHaveAttribute("id", target!);
  await expect(details).not.toHaveAttribute("open", "");
  await marker.click();
  await expect(details).toHaveAttribute("open", "");
  await expect(summary).toBeFocused();
  await expect(summary).toBeInViewport();
  await expect(details.getByText(message.citations[0].quote, { exact: true })).toBeVisible();
  await expect(details.locator("script")).toHaveCount(0);
  await expect(details.getByRole("link", { name: "查看 arXiv 原文" })).toHaveAttribute("href", "https://arxiv.org/pdf/1706.03762v1#page=3");
  for (const key of ["Enter", "Space"]) {
    await summary.click();
    await expect(details).not.toHaveAttribute("open", "");
    await marker.focus();
    await marker.press(key);
    await expect(details).toHaveAttribute("open", "");
    await expect(summary).toBeFocused();
  }
  // The PDF navigation itself is mocked too; this verifies the snapshot's
  // fragment reaches the new tab without allowing a real external request.
  await page.context().route("https://arxiv.org/pdf/1706.03762v1", (route) =>
    route.fulfill({ contentType: "text/plain", body: "Mock paper PDF" }),
  );
  const [pdf] = await Promise.all([
    page.waitForEvent("popup"),
    details.getByRole("link", { name: "查看 arXiv 原文" }).click(),
  ]);
  await expect(pdf).toHaveURL("https://arxiv.org/pdf/1706.03762v1#page=3");
  await pdf.close();
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

test("structured partial answers group questions and show only approved claims and controlled gaps", async ({ page, api }) => {
  const message = citationAnswer(22);
  const result = {
    ...message.result,
    answer: {
      status: "partial",
      parts: [
        { question_id: "q1", question: "核心方法是什么？", status: "supported", claims: [{ text: "受支持的方法结论。", citation_ids: ["shared-source"] }] },
        { question_id: "q2", question: "<b>有哪些实验结果？</b>", status: "partial", claims: [{ text: "仅这项实验结果有充分证据。", citation_ids: ["shared-source"] }], gap: { reason: "review_rejected", text: "禁止展示的被拒结论" } },
        { question_id: "q3", question: "是否适用于未测试场景？", status: "insufficient_evidence", claims: [], gap: { reason: "insufficient_evidence" } },
      ],
    },
  };
  api.json("GET", `${conversationPath}/messages`, {
    items: [{ ...message, result, content: "禁止展示的被拒结论", citations: [...message.citations, { ...message.citations[0], id: "rejected-source", quote: "不得展示的拒绝证据" }] }],
    next_before: 0,
  });
  api.json("GET", "/agent/runs/citation-run-22", completedCitationRun(22));
  await page.goto(assistantURL);
  const reply = page.locator('[data-message-id="22"]');
  await expect(reply.getByText("部分回答", { exact: true })).toBeVisible();
  for (const part of result.answer.parts) await expect(reply.getByRole("heading", { name: part.question, exact: true })).toBeVisible();
  await expect(reply.getByText("受支持的方法结论。", { exact: false })).toBeVisible();
  await expect(reply.getByText("仅这项实验结果有充分证据。", { exact: false })).toBeVisible();
  await expect(reply.getByText("这部分结论未通过证据审核，未予展示。", { exact: true })).toBeVisible();
  await expect(reply.getByText("当前材料不足以可靠回答这部分问题。", { exact: true })).toBeVisible();
  await expect(reply.getByText(/禁止展示|不得展示/)).toHaveCount(0);
  await expect(reply.locator("b")).toHaveCount(0);
  await expect(reply.locator("details")).toHaveCount(1);
  await expect(reply.getByRole("button", { name: "查看证据 shared-source", exact: true })).toHaveCount(2);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

test("insufficient structured answers show a bounded gap and accurate call budgets", async ({ page, api }) => {
  const message = citationAnswer(24);
  api.json("GET", `${conversationPath}/messages`, {
    items: [{ ...message, result: { ...message.result, answer: { status: "insufficient", parts: [{ question_id: "q1", question: "缺失的问题", status: "insufficient_evidence", claims: [], gap: { reason: "insufficient_evidence" } }] } } }],
    next_before: 0,
  });
  api.json("GET", "/agent/runs/citation-run-24", completedCitationRun(24));
  await page.goto(assistantURL);
  const reply = page.locator('[data-message-id="24"]');
  await expect(reply.getByText("证据不足", { exact: true })).toBeVisible();
  await expect(reply.getByText("当前材料不足以可靠回答这部分问题。", { exact: true })).toBeVisible();
  await expect(reply.getByRole("button", { name: /^查看证据 / })).toHaveCount(0);
  await expect(reply.locator("details")).toHaveCount(0);
  await expect(reply.getByText(message.content, { exact: true })).toHaveCount(0);
  await expect(page.getByText(/短论文通常调用 6 次；长论文最多 30 次、15 分钟/)).toBeVisible();
  await expect(page.getByText(/问答通常调用 3 次，最多 6 次、180 秒/)).toBeVisible();
});

test("report and legacy answer markers share evidence controls without crossing message boundaries", async ({ page, api }) => {
  const reportReply = reportMessage("fulltext");
  const answer = citationAnswer(26, "The independent legacy answer source.");
  const legacy = {
    ...answer,
    content: "<b>旧回答保留原文</b> [problem-1-1] [unknown-id] [外部链接](javascript:alert(1))",
    result: { ...questionReply(13, "fulltext", "").result },
    citations: [{ ...answer.citations[0], id: "problem-1-1" }],
  };
  api.json("GET", `${conversationPath}/messages`, { items: [reportReply, legacy], next_before: 0 });
  api.json("GET", `${conversationPath}/paper-report`, { report: reportReply, matches_current_paper: true });
  api.json("GET", "/agent/runs/citation-run-26", completedCitationRun(26));
  await page.goto(assistantURL);
  const pinned = page.getByRole("region", { name: "论文报告", exact: true });
  const reply = page.locator('[data-message-id="26"]');
  const reportMarker = pinned.getByRole("button", { name: "查看证据 problem-1-1", exact: true });
  const answerMarker = reply.getByRole("button", { name: "查看证据 problem-1-1", exact: true });
  expect(await reportMarker.getAttribute("aria-controls")).not.toBe(await answerMarker.getAttribute("aria-controls"));
  await expect(reply.getByText(/<b>旧回答保留原文<\/b>/)).toBeVisible();
  await expect(reply.getByText(/\[unknown-id\]/)).toBeVisible();
  await expect(reply.getByRole("button", { name: /^查看证据 / })).toHaveCount(1);
  await expect(reply.locator("b")).toHaveCount(0);
  await expect(reply.getByRole("link", { name: "外部链接" })).toHaveCount(0);
  await reportMarker.click();
  await expect(pinned.locator("details")).toHaveAttribute("open", "");
  await expect(reply.locator("details")).not.toHaveAttribute("open", "");
  await answerMarker.click();
  await expect(reply.locator("summary")).toBeFocused();
  await expect(reply.getByText(answer.citations[0].quote, { exact: true })).toBeVisible();
  await expect(pinned.getByText(reportReply.citations[0].quote, { exact: true })).toBeAttached();
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

test("evidence targets survive history insertion and are isolated across conversations even for repeated IDs", async ({ page, api }) => {
  const recent = citationAnswer(200, "Current conversation recent source.");
  const older = citationAnswer(100, "Current conversation older source.");
  const other = { ...conversation, id: "conversation-two", title: "另一段论文对话" };
  const otherPath = `/agent/conversations/${other.id}`;
  const replacement = citationAnswer(200, "Other conversation source.");
  api.json("GET", "/agent/conversations", { items: [conversation, other], page: 1, has_more: false, next_page: 0 });
  api.on("GET", `${conversationPath}/messages`, (route, url) => route.fulfill({ json: url.searchParams.has("before") ? { items: [older], next_before: 0 } : { items: [recent], next_before: 200 } }));
  api.json("GET", otherPath, other);
  api.json("GET", `${otherPath}/messages`, { items: [replacement], next_before: 0 });
  api.json("GET", `${otherPath}/paper-report`, { report: null, matches_current_paper: false });
  api.json("GET", "/agent/runs/citation-run-200", completedCitationRun(200));
  await page.goto(assistantURL);
  const recentReply = page.locator('[data-message-id="200"]');
  const recentMarker = recentReply.getByRole("button", { name: "查看证据 shared-source", exact: true });
  const targetBefore = await recentMarker.getAttribute("aria-controls");
  await recentMarker.click();
  await page.getByRole("button", { name: "加载更早消息", exact: true }).click();
  const olderReply = page.locator('[data-message-id="100"]');
  await expect(olderReply).toBeAttached();
  await expect(recentMarker).toHaveAttribute("aria-controls", targetBefore!);
  await expect(recentReply.locator("details")).toHaveAttribute("open", "");
  const olderMarker = olderReply.getByRole("button", { name: "查看证据 shared-source", exact: true });
  expect(await olderMarker.getAttribute("aria-controls")).not.toBe(targetBefore);
  await olderMarker.click();
  await expect(olderReply.locator("summary")).toBeFocused();
  await expect(olderReply.getByText(older.citations[0].quote, { exact: true })).toBeVisible();
  await expect(recentReply.getByText(recent.citations[0].quote, { exact: true })).toBeAttached();
  await page.getByRole("button", { name: "助手设置" }).click();
  await page.getByRole("combobox", { name: "历史对话", exact: true }).selectOption(other.id);
  await expect(page.getByRole("combobox", { name: "历史对话", exact: true })).toHaveValue(other.id);
  await expect(page.locator('[data-message-id="100"]')).toHaveCount(0);
  await expect(recentReply.getByText(replacement.citations[0].quote, { exact: true })).toBeAttached();
  const replacementTarget = await recentMarker.getAttribute("aria-controls");
  expect(replacementTarget).not.toBe(targetBefore);
  await expect(recentReply.locator("details")).not.toHaveAttribute("open", "");
  await page.getByRole("button", { name: "助手设置" }).click();
  await recentMarker.click();
  await expect(recentReply.locator("summary")).toBeFocused();
  await expect(recentReply.getByText(replacement.citations[0].quote, { exact: true })).toBeVisible();
  const ids = await page.locator("details[id]").evaluateAll((nodes) => nodes.map((node) => node.id));
  expect(new Set(ids).size).toBe(ids.length);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

test("replacing a login session remounts evidence targets even when message and citation IDs repeat", async ({ page, api }) => {
  let replacement = false;
  api.on("GET", `${conversationPath}/messages`, (route) =>
    route.fulfill({ json: { items: [citationAnswer(200, replacement ? "New session source." : "Old session source.")], next_before: 0 } }),
  );
  api.json("GET", "/agent/runs/citation-run-200", completedCitationRun(200));
  await page.goto(assistantURL);
  const reply = page.locator('[data-message-id="200"]');
  const marker = reply.getByRole("button", { name: "查看证据 shared-source", exact: true });
  const oldTarget = await marker.getAttribute("aria-controls");
  await marker.click();
  await expect(reply.getByText("Old session source.", { exact: true })).toBeVisible();
  replacement = true;
  await page.evaluate(() => {
    const next = JSON.stringify({ id: "replacement-fixture-session", accessToken: "replacement-fixture-token" });
    localStorage.setItem("signalwatch.session", next);
    window.dispatchEvent(new StorageEvent("storage", { key: "signalwatch.session", newValue: next }));
  });
  await expect(reply.getByText("New session source.", { exact: true })).toBeAttached();
  await expect(reply.getByText("Old session source.", { exact: true })).toHaveCount(0);
  const newTarget = await marker.getAttribute("aria-controls");
  expect(newTarget).not.toBe(oldTarget);
  expect(newTarget).not.toContain("replacement-fixture");
  expect(await page.evaluate((id) => document.getElementById(id!), oldTarget)).toBeNull();
  await expect(reply.locator("details")).not.toHaveAttribute("open", "");
  await marker.focus();
  await marker.press("Enter");
  await expect(reply.locator("summary")).toBeFocused();
  await expect(reply.getByText("New session source.", { exact: true })).toBeVisible();
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

test("one supplemental retrieval exposes safe progress through analysis, repair, review, and reload", async ({ page, api }) => {
  const runID = "supplement-run";
  let phase: "initial" | "pending" | "ready" | "calling" | "repairing" | "reviewing" | "completed" = "initial";
  const reply = { ...citationAnswer(32, "Evidence found during the one supplemental retrieval."), run_id: runID };
  reply.result.workflow_version = "paper-fixed-v11";
  api.on("GET", conversationPath, (route) => route.fulfill({ json: { ...conversation, active_run_id: runID } }));
  api.on("GET", `${conversationPath}/messages`, (route) => route.fulfill({ json: { items: phase === "completed" ? [reply] : [], next_before: 0 } }));
  api.on("GET", `/agent/runs/${runID}`, (route) => {
    const stages = {
      initial: "retrieving_evidence",
      pending: "retrieving_supplement",
      ready: "waiting_for_model_slot",
      calling: "analyzing_answer_supplement",
      repairing: "repairing_answer_supplement",
      reviewing: "validating_paper",
      completed: "completed",
    };
    const state = phase === "initial" ? "initial_ready" : phase === "repairing" ? "calling" : phase === "reviewing" ? "completed" : phase;
    return route.fulfill({ json: {
      run: {
        id: runID,
        task: "paper_followup",
        state: phase === "completed" ? "completed" : "running",
        progress: stages[phase],
        retrieval_summary: { state, selected: 4, added: phase === "initial" || phase === "pending" ? 0 : 3, query: "PRIVATE_QUERY_SENTINEL", candidate: "PRIVATE_DRAFT_SENTINEL", request: "PRIVATE_REQUEST_SENTINEL" },
        ...(phase === "reviewing" ? { review_progress: { completed: 0, total: 1 } } : {}),
      },
      steps: [],
    } });
  });
  await page.goto(assistantURL);
  await expect(page.getByText("正在检索论文证据", { exact: true })).toBeVisible();
  await expect(page.getByText("已选取 4 段论文证据。", { exact: true })).toBeVisible();
  phase = "pending";
  await expect(page.getByText("正在补充检索论文证据", { exact: true })).toBeVisible();
  await expect(page.getByText("正在进行本轮唯一一次补充检索。", { exact: true })).toBeVisible();
  phase = "ready";
  await expect(page.getByText("已找到 3 段补充证据，等待生成补充回答。", { exact: true })).toBeVisible();
  phase = "calling";
  await expect(page.getByText("正在生成补充回答", { exact: true })).toBeVisible();
  await expect(page.getByText("正在结合 3 段补充证据完善回答。", { exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByText("正在生成补充回答", { exact: true })).toBeVisible();
  await expect(page.getByText("正在结合 3 段补充证据完善回答。", { exact: true })).toBeVisible();
  phase = "repairing";
  await expect(page.getByText("正在整理补充回答", { exact: true })).toBeVisible();
  phase = "reviewing";
  await expect(page.getByText("正在审核证据 1/1", { exact: true })).toBeVisible();
  await expect(page.getByText("已完成一次补充检索与回答，新增 3 段证据。", { exact: true })).toBeVisible();
  await expect(page.locator('[data-message-id="32"]')).toHaveCount(0);
  await expect(page.getByText(/PRIVATE_QUERY_SENTINEL|PRIVATE_DRAFT_SENTINEL|PRIVATE_REQUEST_SENTINEL/)).toHaveCount(0);
  phase = "completed";
  await expect(page.getByText("本轮已完成", { exact: true })).toBeVisible();
  const published = page.locator('[data-message-id="32"]');
  await expect(published.getByText("该方法由本轮论文原文支持。", { exact: false })).toBeVisible();
  await published.getByRole("button", { name: "查看证据 shared-source", exact: true }).click();
  await expect(published.getByText(reply.citations[0].quote, { exact: true })).toBeVisible();
  await expect(page.getByText(/PRIVATE_QUERY_SENTINEL|PRIVATE_DRAFT_SENTINEL|PRIVATE_REQUEST_SENTINEL/)).toHaveCount(0);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

for (const skipped of [
  { reason: "no_queries", text: "本轮没有新的检索方向，未进行补充检索。" },
  { reason: "abstract_only", text: "当前仅有摘要材料，未进行补充检索。" },
  { reason: "no_new_evidence", text: "补充检索未找到新的证据。" },
  { reason: "input_budget", text: "补充材料超出本轮输入预算，未生成补充回答。" },
  { reason: "time_budget", text: "本轮剩余时间不足，未生成补充回答。" },
  { reason: "call_budget", text: "本轮调用预算不足，未生成补充回答。" },
  { reason: "PRIVATE_REASON_SENTINEL", text: "本轮未生成补充回答。" },
]) {
  test(`supplemental retrieval skip ${skipped.reason} uses controlled prose and preserves partial answers`, async ({ page, api }) => {
    const message = citationAnswer(34);
    const runID = message.run_id;
    const result = { ...message.result, answer: { status: "partial", parts: [{ ...message.result.answer.parts[0], status: "partial", gap: { reason: "insufficient_evidence" } }] } };
    api.json("GET", `${conversationPath}/messages`, { items: [{ ...message, result }], next_before: 0 });
    api.json("GET", `/agent/runs/${runID}`, {
      run: { id: runID, task: "paper_followup", state: "completed", progress: "completed", retrieval_summary: { state: "skipped", selected: 4, added: 0, reason: skipped.reason, query: "PRIVATE_QUERY_SENTINEL" } },
      steps: [],
    });
    await page.goto(assistantURL);
    await expect(page.getByText(skipped.text, { exact: true })).toBeVisible();
    await expect(page.getByText("部分回答", { exact: true })).toBeVisible();
    await expect(page.getByText("当前材料不足以可靠回答这部分问题。", { exact: true })).toBeVisible();
    await expect(page.getByText(/PRIVATE_REASON_SENTINEL|PRIVATE_QUERY_SENTINEL/)).toHaveCount(0);
    await expect(page.getByText(/正在补充检索|正在生成补充回答/)).toHaveCount(0);
    expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
  });
}

for (const failure of [
  { stage: "retrieving_supplement", state: "pending", code: "access_or_configuration_changed", label: "补充检索论文证据" },
  { stage: "analyzing_answer_supplement", state: "calling", code: "timeout", label: "生成补充回答" },
  { stage: "repairing_answer_supplement", state: "calling", code: "result_unknown", label: "整理补充回答" },
]) {
  test(`failed ${failure.stage} shows the actual stage without stale supplemental activity or an initial draft`, async ({ page, api }) => {
    const runID = "supplement-failed";
    api.json("GET", conversationPath, { ...conversation, active_run_id: runID });
    api.json("GET", `/agent/runs/${runID}`, {
      run: { id: runID, task: "paper_followup", state: failure.code === "result_unknown" ? "unknown" : "failed", progress: failure.stage, failure_code: failure.code, failure_stage: failure.stage, retrieval_summary: { state: failure.state, selected: 4, added: 2 } },
      steps: [{ tool: failure.stage, failure_code: failure.code, call_id: "supplement-final-call" }],
    });
    await page.goto(assistantURL);
    await expect(page.getByText("本轮未完成补充回答。", { exact: true })).toBeVisible();
    await expect(page.getByText(/正在进行本轮唯一一次补充检索|正在结合 \d+ 段补充证据/)).toHaveCount(0);
    await page.getByText("失败详情", { exact: true }).click();
    await expect(page.getByText(`失败步骤：${failure.label}`, { exact: true })).toBeVisible();
    await expect(page.getByText(`原因代码：${failure.code}`, { exact: true })).toBeVisible();
    await expect(page.getByRole("article", { name: "助手回复", exact: true })).toHaveCount(0);
    expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
  });
}

function structuredCitation(id: string, kind: "table" | "formula") {
  return {
    id, page: 0, document_id: "fixture-html", content_hash: "fixture-content-hash",
    source_type: "html", source_version: "1706.03762v1", source_hash: "frozen-source-hash",
    parser_version: "arxiv-html-v1", anchor: kind === "table" ? "S3.T1" : "S2.E1",
    label: kind === "table" ? "Table 1" : "Equation 1", kind,
    url: `https://arxiv.org/html/1706.03762v1#${kind === "table" ? "S3.T1" : "S2.E1"}`,
    quote: kind === "table" ? "Method | Accuracy\nBaseline | 88\nOurs | 93" : "E = mc^2, where m is mass.",
    ...(kind === "table" ? { table: { headers: ["Method", "Accuracy"], rows: [["Baseline", "88"], ["Ours", "93"]], caption: "Comparison of methods", notes: ["All runs use the same test set."] } }
      : { formula: { tex: "E = mc^2", context: "where m is mass and c is the speed of light." } }),
  };
}

test("structured evidence cards preserve tables, formula context, source versions and HTML anchors on mobile", async ({ page, api }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const message = citationAnswer(50);
  const table = structuredCitation("table-source", "table");
  table.table!.rows[0][0] = "<img src=x onerror=alert(1)> Baseline";
  const formula = structuredCitation("formula-source", "formula");
  api.json("GET", `${conversationPath}/messages`, { items: [{ ...message, citations: [table, formula], result: { ...message.result, workflow_version: "paper-fixed-v12", answer: { status: "complete", parts: [{ question_id: "q1", question: "表格与公式说明什么？", status: "supported", claims: [{ text: "表格展示方法比较，公式说明质量与能量关系。", citation_ids: [table.id, formula.id] }] }] } } }], next_before: 0 });
  api.json("GET", "/agent/runs/citation-run-50", completedCitationRun(50));
  await page.goto(assistantURL);
  const reply = page.locator('[data-message-id="50"]');
  await reply.getByRole("button", { name: "查看证据 table-source", exact: true }).press("Enter");
  const tableCard = reply.locator("details").filter({ has: page.getByRole("table") });
  await expect(tableCard.locator("summary")).toBeFocused();
  await expect(tableCard.locator("summary")).toHaveText("证据 table-source · HTML 原文 · Table 1");
  await expect(tableCard.getByRole("columnheader", { name: "Accuracy", exact: true })).toBeVisible();
  await expect(tableCard.getByRole("cell", { name: table.table!.rows[0][0], exact: true })).toBeVisible();
  await expect(tableCard.getByText(table.table!.notes[0], { exact: true })).toBeVisible();
  await expect(tableCard.getByText("来源版本：1706.03762v1", { exact: true })).toBeVisible();
  await expect(tableCard.locator("blockquote")).toHaveText(table.quote);
  await expect(tableCard.locator("img, script")).toHaveCount(0);
  await reply.getByRole("button", { name: "查看证据 formula-source", exact: true }).press("Space");
  const formulaCard = reply.locator("details").filter({ has: page.locator(".paper-evidence-formula") });
  await expect(formulaCard.locator(".katex")).toBeVisible();
  await expect(formulaCard.getByText(formula.formula!.context, { exact: true })).toBeVisible();
  await expect(formulaCard.locator("blockquote")).toHaveText(formula.quote);
  await expect(formulaCard.getByRole("link", { name: "查看 arXiv 原文" })).toHaveAttribute("href", formula.url);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.context().route("https://arxiv.org/html/1706.03762v1", (route) => route.fulfill({ contentType: "text/html", body: '<p id="S2.E1">Mock equation</p>' }));
  const [source] = await Promise.all([page.waitForEvent("popup"), formulaCard.getByRole("link", { name: "查看 arXiv 原文" }).click()]);
  await expect(source).toHaveURL(formula.url);
  await source.close();
});

test("formula rendering bounds expansion and size, isolates macros and never trusts source HTML or URLs", async ({ page, api }) => {
  const message = citationAnswer(52);
  const inputs = [
    "\\gdef\\isolated{PRIVATE_MACRO}x",
    "\\isolated",
    "\\def\\recurse{\\recurse}\\recurse",
    "\\href{javascript:alert(1)}{unsafe-link}",
    "\\includegraphics{https://invalid.example/image.png}",
    "\\htmlClass{injected}{x}",
    "\\rule{1000000em}{1000000em}",
    "x".repeat(16_385),
  ];
  const citations = inputs.map((tex, index) => ({ ...structuredCitation(`formula-${index}`, "formula"), formula: { tex, context: `Formula context ${index}` } }));
  api.json("GET", `${conversationPath}/messages`, { items: [{ ...message, citations, result: { ...message.result, answer: { status: "complete", parts: [{ question_id: "q1", question: "公式证据", status: "supported", claims: [{ text: "来源公式。", citation_ids: citations.map((ref) => ref.id) }] }] } } }], next_before: 0 });
  api.json("GET", "/agent/runs/citation-run-52", completedCitationRun(52));
  await page.goto(assistantURL);
  const reply = page.locator('[data-message-id="52"]');
  for (const ref of citations) await reply.getByRole("button", { name: `查看证据 ${ref.id}`, exact: true }).click();
  const cards = reply.locator("details");
  await expect(cards.nth(0).locator(".katex")).toBeVisible();
  for (const index of [1, 2, 5, 7]) await expect(cards.nth(index).locator(".paper-evidence-formula-fallback")).toHaveText(inputs[index]);
  await expect(reply.locator(".paper-evidence-formula a, .paper-evidence-formula img, .paper-evidence-formula script, .paper-evidence-formula .injected")).toHaveCount(0);
  await expect(cards.nth(1).getByText("PRIVATE_MACRO", { exact: false })).toHaveCount(0);
  const size = await cards.nth(6).locator(".katex .katex-rule").boundingBox();
  expect(size?.height).toBeLessThan(500);
  expect(size?.width).toBeLessThan(500);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

test("single approved answer Markdown copies and downloads tables, TeX and safe gaps without drafts or unused sources", async ({ page, api }) => {
  await captureClipboard(page);
  const message = citationAnswer(54);
  const table = structuredCitation("table-source", "table");
  table.table!.rows[0][0] = "A | B\n<script>literal</script>";
  const formula = structuredCitation("formula-source", "formula");
  formula.formula!.tex = "E = mc^2 % ``` literal fence";
  const citations = [table, formula, { ...table, id: "table-alias" }, { ...table, id: "rejected-source", quote: "PRIVATE_REJECTED_EVIDENCE" }];
  const result = { ...message.result, paper_title: "Frozen title <b>literal</b>", original_question: "请说明论文表格和公式，并讨论未验证场景。", workflow_version: "paper-fixed-v12", structured_gap: "incomplete", checkpoint: { query: "PRIVATE_QUERY", candidate: "PRIVATE_DRAFT" }, answer: { status: "partial", parts: [
    { question_id: "q1", question: "表格和公式？", status: "supported", claims: [{ text: "已审核结论 <script>literal</script>", citation_ids: [table.id, formula.id, table.id, "table-alias"] }] },
    { question_id: "q2", question: "未验证的场景？", status: "insufficient_evidence", claims: [], gap: { reason: "review_rejected", text: "PRIVATE_REJECTED_TEXT" } },
  ] } };
  api.json("GET", `${conversationPath}/messages`, { items: [{ id: 53, role: "user", content: "我的提问 PRIVATE_USER_HISTORY", citations: [] }, { ...message, content: "PRIVATE_RAW_DRAFT", result, citations }], next_before: 0 });
  api.json("GET", "/agent/runs/citation-run-54", completedCitationRun(54));
  await page.goto(assistantURL);
  const reply = page.locator('[data-message-id="54"]');
  await expect(page.locator('[data-message-id="53"]').getByRole("button", { name: /Markdown/ })).toHaveCount(0);
  await reply.getByRole("button", { name: "复制 Markdown", exact: true }).click();
  await expect(reply.getByRole("status")).toHaveText("已复制 Markdown");
  const markdown = await copiedMarkdown(page);
  expect(markdown).toContain("Frozen title &lt;b&gt;literal&lt;/b&gt;");
  expect(markdown).toContain("已审核结论 &lt;script&gt;literal&lt;/script&gt;");
  expect(markdown).toContain(`原问题：${result.original_question}`);
  expect(markdown).toContain("| Method | Accuracy |");
  expect(markdown).toContain("A \\| B<br>&lt;script&gt;literal&lt;/script&gt;");
  expect(markdown).toContain(`\n\`\`\`\`tex\n${formula.formula!.tex}\n\`\`\`\``);
  expect(markdown).toContain(formula.formula!.context.replaceAll(".", "\\."));
  expect(markdown).toContain("https://arxiv.org/html/1706.03762v1#S3.T1");
  expect(markdown).toContain("原文位置：S2\\.E1");
  expect(markdown).toContain("这部分结论未通过证据审核，未予展示。");
  expect(markdown).toContain("本轮仅提取到部分结构化表格和公式");
  expect(markdown).not.toMatch(/PRIVATE_|<script>|<b>|rejected-source/);
  expect(markdown.match(/### 证据 table\\-source/g)).toHaveLength(1);
  expect(markdown).toContain("### 证据 table\\-source、table\\-alias");
  expect(markdown.match(/\| Method \| Accuracy \|/g)).toHaveLength(1);
  const [download] = await Promise.all([page.waitForEvent("download"), reply.getByRole("button", { name: "下载 Markdown", exact: true }).click()]);
  expect(download.suggestedFilename()).toBe("signalwatch-paper-conversation-one-54.md");
  expect(await readFile((await download.path())!, "utf8")).toBe(markdown);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});

test("report Markdown includes the five public fields and source appendix while JSON copy stays compatible", async ({ page, api }) => {
  await captureClipboard(page);
  const message = reportMessage("fulltext");
  const table = structuredCitation("problem-1-1", "table");
  const reportReply = { ...message, content: "PRIVATE_OLD_RENDERED_CONTENT", result: { ...message.result, paper_title: "Frozen report title", structured_gap: "input_budget" }, citations: [table] };
  api.json("GET", `${conversationPath}/messages`, { items: [reportReply], next_before: 0 });
  api.json("GET", `${conversationPath}/paper-report`, { report: reportReply, matches_current_paper: false });
  api.json("GET", "/agent/runs/report-run", { run: { id: "report-run", state: "completed", progress: "completed", task: "paper_report" }, steps: [] });
  await page.goto(assistantURL);
  const pinned = page.getByRole("region", { name: "论文报告", exact: true });
  await pinned.getByRole("button", { name: "复制 Markdown", exact: true }).click();
  await expect(pinned.getByRole("status").filter({ hasText: "已复制 Markdown" })).toBeVisible();
  const markdown = await copiedMarkdown(page);
  for (const title of ["论文问题", "核心方法", "实验验证", "主要结果", "局限性"]) expect(markdown).toContain(`## ${title}\n`);
  expect(markdown).toContain("# Frozen report title — 论文报告");
  expect(markdown).toContain("## 原文证据");
  expect(markdown).toContain("| Baseline | 88 |");
  expect(markdown).toContain("受本轮材料容量限制");
  expect(markdown).not.toContain("PRIVATE_");
  const [download] = await Promise.all([page.waitForEvent("download"), pinned.getByRole("button", { name: "下载 Markdown", exact: true }).click()]);
  expect(await readFile((await download.path())!, "utf8")).toBe(markdown);
  await pinned.getByRole("button", { name: "复制 JSON", exact: true }).click();
  await expect(pinned.getByText("已复制 JSON", { exact: true })).toBeVisible();
  expect(JSON.parse(await copiedMarkdown(page))).toEqual(report);
});

test("legacy Markdown keeps unknown markers and unsafe links literal and exports only sources actually cited", async ({ page, api }) => {
  await captureClipboard(page);
  const message = citationAnswer(56);
  const legacy = { ...message, result: { ...questionReply(28, "abstract", "").result }, content: "旧回答 [shared-source] [unknown] [click](javascript:alert(1)) <script>literal</script>", citations: [...message.citations, { ...message.citations[0], id: "unused", quote: "PRIVATE_UNUSED_SOURCE" }] };
  api.json("GET", `${conversationPath}/messages`, { items: [legacy], next_before: 0 });
  api.json("GET", "/agent/runs/citation-run-56", completedCitationRun(56));
  await page.goto(assistantURL);
  const reply = page.locator('[data-message-id="56"]');
  await reply.getByRole("button", { name: "复制 Markdown", exact: true }).click();
  await expect(reply.getByRole("status")).toHaveText("已复制 Markdown");
  const markdown = await copiedMarkdown(page);
  expect(markdown).toContain("仅基于摘要");
  expect(markdown).toContain("\\[unknown\\]");
  expect(markdown).toContain("\\[click\\]\\(javascript:alert\\(1\\)\\)");
  expect(markdown).toContain("&lt;script&gt;literal&lt;/script&gt;");
  expect(markdown).toContain("https://arxiv.org/pdf/1706.03762v1#page=3");
  expect(markdown).toContain("第 3 页");
  expect(markdown).not.toMatch(/PRIVATE_|### 证据 unused/);
});

test("structured material gaps use fixed text for known reasons and never display internal reason values", async ({ page, api }) => {
  const reasons = ["unavailable", "incomplete", "preparation_budget", "input_budget", "PRIVATE_STRUCTURED_REASON"];
  const messages = reasons.map((reason, index) => ({ ...citationAnswer(60 + index * 2), result: { ...citationAnswer(60 + index * 2).result, structured_gap: reason } }));
  api.json("GET", `${conversationPath}/messages`, { items: messages, next_before: 0 });
  api.json("GET", "/agent/runs/citation-run-68", completedCitationRun(68));
  await page.goto(assistantURL);
  for (const [index, expected] of ["本轮未能取得结构化表格和公式", "本轮仅提取到部分结构化表格和公式", "材料准备时间有限", "受本轮材料容量限制", "本轮结构化材料不完整"].entries()) {
    await expect(page.locator(`[data-message-id="${60 + index * 2}"]`).getByText(expected, { exact: false })).toBeVisible();
  }
  await expect(page.getByText("PRIVATE_STRUCTURED_REASON", { exact: false })).toHaveCount(0);
});

test("Markdown copy failure offers a local download without exporting an unfinished answer", async ({ page, api }) => {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async () => { throw new Error("blocked"); } } });
  });
  const message = citationAnswer(70);
  api.json("GET", conversationPath, { ...conversation, active_run_id: "pending-export-run" });
  api.json("GET", `${conversationPath}/messages`, { items: [message, { id: 71, role: "user", content: "正在等待的新问题", citations: [] }], next_before: 0 });
  api.json("GET", "/agent/runs/pending-export-run", { run: { id: "pending-export-run", task: "paper_followup", state: "running", progress: "analyzing_answer", candidate: "PRIVATE_PENDING_ANSWER" }, steps: [] });
  await page.goto(assistantURL);
  await expect(page.getByRole("article", { name: "助手回复", exact: true })).toHaveCount(1);
  const reply = page.locator('[data-message-id="70"]');
  await reply.getByRole("button", { name: "复制 Markdown", exact: true }).click();
  await expect(reply.getByRole("alert")).toContainText("可点击“下载 Markdown”保存文件");
  await expect(page.getByRole("button", { name: "下载 Markdown", exact: true })).toHaveCount(1);
  const [download] = await Promise.all([page.waitForEvent("download"), reply.getByRole("button", { name: "下载 Markdown", exact: true }).click()]);
  const markdown = await readFile((await download.path())!, "utf8");
  expect(markdown).toContain("该方法由本轮论文原文支持。");
  expect(markdown).not.toMatch(/PRIVATE_|正在等待的新问题/);
  expect(api.requests.filter((request) => request.method !== "GET")).toEqual([]);
});
