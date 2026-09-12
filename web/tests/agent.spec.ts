import { test, expect, type Page } from "@playwright/test";
import { setup, subscription } from "./fixtures";
const credential = {
  configured: true,
  usable: true,
  is_default: true,
  provider: "glm",
  model: "glm-4.7-flash",
  id: "g1",
  name: "GLM 研究",
  generation: "g1",
  version: 1,
  masked_key: "••••1234",
};
const conversation = {
  id: "conversation-one",
  kind: "subscription",
  title: "订阅助手",
  latest_draft_id: "run-one",
};
async function models(page: Page, kind = "subscription") {
  const fixture = {
    ...conversation,
    kind,
    ...(kind === "paper" ? { paper_id: 1, paper_report_ready: true } : {}),
  };
  await page.route("**/api/v2/ai/credentials", (r) =>
    r.fulfill({
      json: {
        items: [
          credential,
          {
            ...credential,
            provider: "qwen",
            id: "g2",
            generation: "g2",
            name: "Qwen 备用",
            model: "qwen3.8-flash",
            is_default: false,
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
            id: "glm",
            name: "GLM",
            models: [{ id: "glm-4.7-flash", name: "Flash" }],
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
  await page.route("**/api/v2/agent/conversations?**", (r) =>
    r.fulfill({ json: { items: [fixture], page: 1 } }),
  );
  await page.route("**/api/v2/agent/conversations/conversation-one", (r) =>
    r.fulfill({ json: fixture }),
  );
}
for (const outcome of ["clarify", "draft", "paper", "retry", "fast"] as const) {
  test(`live ${outcome} completion loads the reply without refreshing`, async ({
    page,
  }) => {
    await setup(page);
    await models(page, outcome === "paper" ? "paper" : "subscription");
    let completed = false;
    let confirmations = 0;
    let failedReads = 0;
    let writes = 0;
    page.on("request", (r) => {
      if (r.url().includes("/agent/") && r.method() === "POST") writes++;
    });
    const content =
      outcome === "clarify"
        ? "请补充具体研究方向"
        : outcome === "draft"
          ? "已准备好订阅草案"
          : "论文通过独立测试集验证方法 [p2-c1]";
    await page.route("**/api/v2/agent/conversations/conversation-one", (r) =>
      r.fulfill({
        json: {
          ...conversation,
          ...(outcome === "paper"
            ? { kind: "paper", paper_id: 1, paper_report_ready: true }
            : {}),
          ...(completed ? {} : { active_run_id: "run-one" }),
        },
      }),
    );
    await page.route(
      "**/api/v2/agent/conversations/conversation-one/messages",
      async (r) => {
        // A real API round trip must survive the transition to a terminal run.
        if (completed) await new Promise((resolve) => setTimeout(resolve, 150));
        if (completed && outcome === "retry" && failedReads++ === 0) {
          await r.fulfill({
            status: 503,
            json: {
              code: "TEMPORARILY_UNAVAILABLE",
              message: "消息读取暂时失败",
            },
          });
          return;
        }
        await r.fulfill({
          json: {
            items: [
              {
                id: 1,
                run_id: "run-one",
                role: "user",
                content: "研究方法",
                citations: [],
              },
              ...(completed
                ? [
                    {
                      id: 2,
                      run_id: "run-one",
                      role: "assistant",
                      content,
                      citations:
                        outcome === "paper"
                          ? [
                              {
                                id: "p2-c1",
                                quote: "held out evaluation dataset",
                                page: 2,
                                url: "https://arxiv.org/pdf/1706.03762v1#page=2",
                              },
                            ]
                          : [],
                      ...(outcome === "draft" ? { draft_id: "run-one" } : {}),
                    },
                  ]
                : []),
            ],
            next_before: 0,
          },
        });
      },
    );
    await page.route("**/api/v2/agent/runs/run-one", (r) => {
      if (outcome === "fast") completed = true;
      return r.fulfill({
        json: {
          run: {
            id: "run-one",
            state: completed ? "completed" : "running",
            progress: completed ? "completed" : "generating",
          },
          steps: [],
        },
      });
    });
    await page.route("**/api/v2/agent/subscription-drafts/run-one", (r) =>
      r.fulfill({
        json: {
          id: "run-one",
          version: 1,
          payload: { ...subscription, source_id: 1 },
          expires_at: "2099-01-01T00:00:00Z",
          ...(confirmations ? { subscription_id: 25 } : {}),
        },
      }),
    );
    await page.route(
      "**/api/v2/agent/subscription-drafts/run-one/confirm",
      (r) => {
        confirmations++;
        return r.fulfill({ json: { ...subscription, id: 25 } });
      },
    );
    await page.goto(
      outcome === "paper"
        ? "/papers?paper_id=1&assistant=paper&conversation=conversation-one"
        : "/subscriptions?assistant=subscription&conversation=conversation-one",
    );
    if (outcome !== "fast")
      await expect(
        page.getByRole("button", { name: "停止本轮" }),
      ).toBeVisible();
    completed = true;
    if (outcome === "retry")
      await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByText(content, { exact: true })).toBeVisible({
      timeout: 10000,
    });
    await expect(
      page.getByRole("button", { name: "停止本轮" }),
    ).not.toBeVisible();
    await expect(page.getByRole("alert")).not.toBeVisible();
    expect(confirmations).toBe(0);
    expect(writes).toBe(0);
    if (outcome === "draft") {
      await page.getByRole("button", { name: "确认创建订阅" }).click();
      await expect(page.getByText("已创建订阅 #25")).toBeVisible();
      expect(confirmations).toBe(1);
    }
    if (outcome === "paper") {
      await page.getByText("证据 p2-c1 · 第 2 页").click();
      await expect(page.getByText("held out evaluation dataset")).toBeVisible();
    }
  });
}
test("paper assistant guides API setup and retains a return link", async ({
  page,
}) => {
  await setup(page);
  await page.goto("/papers?paper_id=1");
  await page.getByRole("button", { name: "AI 论文助手", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "AI 论文助手" }),
  ).toBeVisible();
  await page.getByRole("link", { name: "配置 API" }).click();
  await expect(page.getByRole("link", { name: "返回原对话" })).toHaveAttribute(
    "href",
    /assistant=paper/,
  );
});
test("persisted draft restores and only explicit confirmation creates a subscription", async ({
  page,
}) => {
  await setup(page);
  await models(page);
  let confirmed = false;
  let confirmations = 0;
  const draft = {
    id: "run-one",
    conversation_id: conversation.id,
    version: 1,
    payload: { ...subscription, source_id: 1 },
    expires_at: "2099-01-01T00:00:00Z",
  };
  await page.route(
    "**/api/v2/agent/conversations/conversation-one/messages",
    (r) =>
      r.fulfill({
        json: {
          items: [
            {
              id: 1,
              run_id: "run-one",
              role: "user",
              content: "关注 Agent",
              citations: [],
            },
            {
              id: 2,
              run_id: "run-one",
              role: "assistant",
              content: "请确认草案",
              citations: [],
              draft_id: draft.id,
              provider: "glm",
              model: "glm-4.7-flash",
            },
          ],
          next_before: 0,
        },
      }),
  );
  await page.route("**/api/v2/agent/runs/run-one", (r) =>
    r.fulfill({
      json: {
        run: { id: "run-one", state: "completed", progress: "completed" },
        steps: [],
      },
    }),
  );
  await page.route("**/api/v2/agent/subscription-drafts/run-one", (r) =>
    r.fulfill({
      json: { ...draft, ...(confirmed ? { subscription_id: 25 } : {}) },
    }),
  );
  await page.route(
    "**/api/v2/agent/subscription-drafts/run-one/confirm",
    (r) => {
      confirmations++;
      expect(r.request().postDataJSON()).toEqual({ version: 1 });
      confirmed = true;
      return r.fulfill({ json: { ...subscription, id: 25 } });
    },
  );
  await page.goto(
    "/subscriptions?assistant=subscription&conversation=conversation-one",
  );
  await expect(
    page.getByRole("button", { name: "确认创建订阅" }),
  ).toBeEnabled();
  expect(confirmations).toBe(0);
  await page.reload();
  await page.getByRole("button", { name: "确认创建订阅" }).click();
  await expect(page.getByText("已创建订阅 #25")).toBeVisible();
  expect(confirmations).toBe(1);
});
for (const selectedProvider of ["qwen", "glm"])
  test(`chat selects a specific ${selectedProvider} credential and supports cancellation`, async ({
    page,
  }) => {
    await setup(page);
    await models(page);
    if (selectedProvider === "glm")
      await page.route("**/api/v2/ai/credentials", (r) =>
        r.fulfill({
          json: {
            items: [
              credential,
              {
                ...credential,
                id: "g2",
                generation: "g2",
                name: "GLM 备用",
                is_default: false,
              },
            ],
          },
        }),
      );
    let active = false;
    let submitted: unknown;
    await page.route("**/api/v2/agent/conversations/conversation-one", (r) =>
      r.fulfill({
        json: {
          ...conversation,
          ...(active ? { active_run_id: "run-two" } : {}),
        },
      }),
    );
    await page.route(
      "**/api/v2/agent/conversations/conversation-one/messages",
      (r) => {
        if (r.request().method() === "POST") {
          active = true;
          submitted = r.request().postDataJSON();
          return r.fulfill({
            status: 202,
            json: {
              run_id: "run-two",
              run: { id: "run-two", state: "pending", progress: "queued" },
            },
          });
        }
        return r.fulfill({
          json: {
            items: active
              ? [
                  {
                    id: 1,
                    run_id: "run-two",
                    role: "user",
                    content: "订阅视觉模型",
                    citations: [],
                  },
                ]
              : [],
            next_before: 0,
          },
        });
      },
    );
    await page.route("**/api/v2/agent/runs/run-two", (r) =>
      r.fulfill({
        json: {
          run: { id: "run-two", state: "running", progress: "generating" },
          steps: [],
        },
      }),
    );
    await page.route("**/api/v2/agent/runs/run-two/cancel", (r) =>
      r.fulfill({ status: 204 }),
    );
    let defaultWrites = 0;
    await page.route("**/api/v2/ai/configuration", (r) => {
      if (r.request().method() !== "GET") defaultWrites++;
      return r.fulfill({ json: credential });
    });
    await page.goto(
      "/subscriptions?assistant=subscription&conversation=conversation-one",
    );
    await page.getByRole("button", { name: "助手设置" }).click();
    await page.getByRole("combobox", { name: "对话 API" }).selectOption("g2");
    await page.getByRole("textbox", { name: "你的问题" }).fill("订阅视觉模型");
    await page.getByRole("button", { name: "发送", exact: true }).click();
    await expect(page.getByRole("button", { name: "停止本轮" })).toBeVisible();
    expect(submitted).toMatchObject({
      provider: selectedProvider,
      credential_id: "g2",
      model: selectedProvider === "glm" ? "glm-4.7-flash" : "qwen3.8-flash",
      question: "订阅视觉模型",
    });
    expect(defaultWrites).toBe(0);
    await page.getByRole("button", { name: "停止本轮" }).click();
    await expect(page.getByText("本轮已停止。")).toBeVisible();
  });
test("paper citations and unknown run outcomes recover on refresh", async ({
  page,
}) => {
  await setup(page);
  await models(page, "paper");
  await page.route(
    "**/api/v2/agent/conversations/conversation-one/messages",
    (r) =>
      r.fulfill({
        json: {
          items: [
            {
              id: 1,
              run_id: "run-one",
              role: "assistant",
              content: "独立数据集评估 [p2-c1]",
              provider: "glm",
              model: "glm-4.7-flash",
              citations: [
                {
                  id: "p2-c1",
                  quote: "held out evaluation dataset",
                  page: 2,
                  url: "https://arxiv.org/pdf/1706.03762v1#page=2",
                },
              ],
            },
            {
              id: 2,
              run_id: "failed",
              role: "user",
              content: "实验还有哪些细节？",
              citations: [],
            },
          ],
          next_before: 0,
        },
      }),
  );
  await page.route("**/api/v2/agent/runs/failed", (r) =>
    r.fulfill({
      json: {
        run: { id: "failed", state: "unknown", failure_code: "result_unknown" },
        steps: [],
      },
    }),
  );
  await page.goto(
    "/papers?paper_id=1&assistant=paper&conversation=conversation-one",
  );
  await page.getByText("证据 p2-c1 · 第 2 页").click();
  await expect(page.getByText("held out evaluation dataset")).toBeVisible();
  await expect(
    page.getByText("模型调用结果未知，未自动重试。你可以手动重新发送。"),
  ).toBeVisible();
  await page.getByRole("button", { name: "重新编辑上次问题" }).click();
  await expect(page.getByRole("textbox", { name: "你的问题" })).toHaveValue(
    "实验还有哪些细节？",
  );
});

test("the first submitted question is visible while its run is pending", async ({
  page,
}) => {
  await setup(page);
  await models(page);
  let submitted = false;
  await page.route("**/api/v2/agent/conversations", (r) =>
    r.fulfill({ status: 201, json: conversation }),
  );
  await page.route("**/api/v2/agent/conversations/conversation-one", (r) =>
    r.fulfill({
      json: {
        ...conversation,
        ...(submitted ? { active_run_id: "first-run" } : {}),
      },
    }),
  );
  await page.route(
    "**/api/v2/agent/conversations/conversation-one/messages",
    async (r) => {
      if (r.request().method() === "POST") {
        submitted = true;
        await r.fulfill({
          status: 202,
          json: {
            run_id: "first-run",
            run: { id: "first-run", state: "pending", progress: "queued" },
          },
        });
        return;
      }
      const items = submitted
        ? [
            {
              id: 1,
              run_id: "first-run",
              role: "user",
              content: "关注模型对齐",
              citations: [],
            },
          ]
        : [];
      if (!submitted) await new Promise((resolve) => setTimeout(resolve, 150));
      await r.fulfill({ json: { items, next_before: 0 } });
    },
  );
  await page.route("**/api/v2/agent/runs/first-run", (r) =>
    r.fulfill({
      json: {
        run: { id: "first-run", state: "running", progress: "generating" },
        steps: [],
      },
    }),
  );
  await page.goto("/subscriptions?assistant=subscription");
  await page.getByRole("textbox", { name: "你的问题" }).fill("关注模型对齐");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(
    page.getByLabel("对话记录").getByText("关注模型对齐"),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "停止本轮" })).toBeVisible();
});

for (const mode of ["fulltext", "abstract"] as const) {
  test(`fixed report ${mode} needs a click and restores structured output`, async ({
    page,
    context,
  }) => {
    await setup(page);
    await models(page, "paper");
    if (mode === "abstract")
      await page.setViewportSize({ width: 390, height: 844 });
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    let writes = 0;
    let generated = false;
    let lastTask = "";
    await page.route("**/api/v2/agent/conversations/conversation-one", (r) =>
      r.fulfill({
        json: {
          id: "conversation-one",
          kind: "paper",
          paper_id: 1,
          title: "Paper",
          paper_report_ready: generated,
        },
      }),
    );
    const report = {
      problem: "解决检索准确性问题 [problem-1-1]",
      method: "使用检索方法",
      experiments: "使用独立测试集",
      results: "当前材料不足以可靠回答",
      limitations: "论文未明确说明",
    };
    await page.route(
      "**/api/v2/agent/conversations/conversation-one/messages",
      (r) => {
        if (r.request().method() === "POST") {
          writes++;
          lastTask = r.request().postDataJSON().task;
          generated = true;
          return r.fulfill({
            status: 202,
            json: {
              run_id: "report-run",
              run: {
                id: "report-run",
                task: lastTask,
                state: "pending",
                progress: "queued",
              },
            },
          });
        }
        return r.fulfill({
          json: {
            items: generated
              ? [
                  {
                    id: 1,
                    run_id: "report-run",
                    role: "assistant",
                    content: "## 论文问题\n\n解决检索准确性问题",
                    provider: "glm",
                    model: "glm-4.7-flash",
                    result: {
                      report,
                      context_mode: mode,
                      ...(mode === "abstract"
                        ? { fallback_reason: "ocr_required" }
                        : {}),
                      coverage:
                        mode === "abstract"
                          ? "abstract_only"
                          : "all_extracted_text",
                      workflow_version: "paper-fixed-v1",
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
                            citation_ids:
                              field === "problem" ? ["problem-1-1"] : [],
                          },
                        ]),
                      ),
                    },
                    citations: [
                      {
                        id: "problem-1-1",
                        page: mode === "abstract" ? 0 : 2,
                        quote: "The retrieval method addresses accuracy.",
                        url: "https://arxiv.org/pdf/1706.03762v1#page=2",
                      },
                    ],
                  },
                ]
              : [],
            next_before: 0,
          },
        });
      },
    );
    await page.route("**/api/v2/agent/runs/report-run", (r) =>
      r.fulfill({
        json: {
          run: {
            id: "report-run",
            task: lastTask,
            state: "completed",
            progress: "completed",
            effective_context_mode: mode,
          },
          steps: [],
        },
      }),
    );
    await page.goto(
      "/papers?paper_id=1&assistant=paper&conversation=conversation-one",
    );
    const generate = page.getByRole("button", {
      name: "生成论文报告",
      exact: true,
    });
    await expect(generate).toBeEnabled();
    expect(writes).toBe(0);
    await expect(
      page.getByRole("textbox", { name: "你的问题" }),
    ).toBeDisabled();
    await expect(
      page.getByRole("button", { name: "发送", exact: true }),
    ).toBeDisabled();
    await generate.click();
    await expect(page.getByRole("button", { name: "复制 JSON" })).toBeVisible();
    await expect(page.getByRole("textbox", { name: "你的问题" })).toBeEnabled();
    expect(lastTask).toBe("paper_report");
    expect(writes).toBe(1);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `/tmp/signalwatch-paper-report-${mode}.png`,
      fullPage: true,
    });
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
    if (mode === "abstract")
      await expect(page.getByText(/扫描或图片页面需要 OCR/)).toBeVisible();
    await page
      .getByText(
        `证据 problem-1-1 · ${mode === "abstract" ? "摘要" : "第 2 页"}`,
      )
      .click();
    await expect(
      page.getByText("The retrieval method addresses accuracy."),
    ).toBeVisible();
    await page.getByRole("button", { name: "复制 JSON" }).click();
    expect(
      JSON.parse(await page.evaluate(() => navigator.clipboard.readText())),
    ).toEqual(report);
    await page.reload();
    await expect(
      page.getByRole("button", { name: "重新生成论文报告" }),
    ).toBeEnabled();
    expect(writes).toBe(1);
    await page.getByRole("button", { name: "助手设置" }).click();
    await page.getByRole("combobox", { name: "对话 API" }).selectOption("g2");
    expect(writes).toBe(1);
    await page
      .getByRole("textbox", { name: "你的问题" })
      .fill("实验有什么条件？");
    await page.getByRole("button", { name: "发送", exact: true }).click();
    await expect.poll(() => lastTask).toBe("paper_followup");
    expect(writes).toBe(2);
  });
}

for (const [code, message] of [
  ["evidence_quote_mismatch", "证据引文无法在本次提供的论文原文中精确定位。"],
  ["output_schema_mismatch", "模型输出的字段、类型或结构不符合约定。"],
]) {
  test(`paper failure exposes stage and diagnosis for ${code}`, async ({
    page,
  }) => {
    await setup(page);
    await models(page, "paper");
    let writes = 0;
    page.on("request", (request) => {
      if (request.url().includes("/agent/") && request.method() === "POST")
        writes++;
    });
    await page.route(
      "**/api/v2/agent/conversations/conversation-one/messages",
      (route) =>
        route.fulfill({
          json: {
            items: [
              {
                id: 1,
                run_id: "failed-report",
                role: "user",
                content: "帮助用户快速了解当前论文",
                citations: [],
              },
            ],
            next_before: 0,
          },
        }),
    );
    await page.route("**/api/v2/agent/runs/failed-report", (route) =>
      route.fulfill({
        json: {
          run: {
            id: "failed-report",
            state: "failed",
            task: "paper_report",
            failure_code: code,
            failure_detail: {
              code,
              path: "$.claims[0].evidence",
              rule: "expected_array",
            },
          },
          steps: [
            {
              tool: "extracting_batch_1",
              failure_code: code,
              call_id: "diagnostic-one",
            },
          ],
        },
      }),
    );
    await page.goto(
      "/papers?paper_id=1&assistant=paper&conversation=conversation-one",
    );
    await expect(page.getByText(message, { exact: true })).toBeVisible();
    await page.getByText("失败详情", { exact: true }).click();
    await expect(page.getByText("失败步骤：第 1 批全文证据提取")).toBeVisible();
    await expect(page.getByText("诊断编号：diagnostic-one")).toBeVisible();
    await expect(
      page.getByText("校验位置：$.claims[0].evidence"),
    ).toBeVisible();
    await expect(page.getByText("校验要求：必须是数组")).toBeVisible();
    await page.reload();
    await expect(page.getByText(message, { exact: true })).toBeVisible();
    expect(writes).toBe(0);
  });
}
