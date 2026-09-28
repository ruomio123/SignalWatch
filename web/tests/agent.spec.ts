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
