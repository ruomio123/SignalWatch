import {
  test,
  expect,
  assistantURL,
  conversation,
  conversationPath,
  credential,
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
    expect(writes).toHaveLength(0);
    // Updated alongside direct Q&A when that workflow is introduced.
    await expect(
      page.getByRole("textbox", { name: "你的问题" }),
    ).toBeDisabled();
    await generate.click();
    await expect(page.getByRole("button", { name: "复制 JSON" })).toBeVisible();
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
