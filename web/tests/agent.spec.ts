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
    // Updated alongside direct Q&A when that workflow is introduced.
    await expect(
      page.getByRole("textbox", { name: "你的问题" }),
    ).toBeDisabled();
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

test("a stale report is readable while follow-up waits for a current report", async ({
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
    page.getByText("这份报告对应旧版论文材料，请重新生成后再追问。", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.getByRole("textbox", { name: "你的问题" })).toBeDisabled();
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
