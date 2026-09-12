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
async function models(page: Page) {
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
    r.fulfill({ json: { items: [conversation], page: 1 } }),
  );
  await page.route("**/api/v2/agent/conversations/conversation-one", (r) =>
    r.fulfill({ json: conversation }),
  );
}
for (const outcome of ["clarify", "draft", "paper", "retry", "fast"] as const) {
  test(`live ${outcome} completion loads the reply without refreshing`, async ({
    page,
  }) => {
    await setup(page);
    await models(page);
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
  await page.getByRole("button", { name: "AI 全文对话" }).click();
  await expect(page.getByRole("dialog", { name: "AI 论文助手" })).toBeVisible();
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
  await models(page);
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
