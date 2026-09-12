import { expect, test, type Page } from "@playwright/test";
import { setup, subscription } from "./fixtures";

async function subscriptions(page: Page, count = 2) {
  await setup(page);
  const state = {
    items: Array.from({ length: count }, (_, i) => ({
      ...subscription,
      id: i + 1,
      name: `研究订阅 ${i + 1}`,
    })),
    writes: [] as {
      id: number;
      method: string;
      etag?: string;
      body?: unknown;
    }[],
    hold: undefined as Promise<void> | undefined,
    failure: undefined as { status: number; code: string } | undefined,
  };
  await page.route("**/api/v2/subscriptions?**", (r) => {
    const params = new URL(r.request().url()).searchParams;
    const pageNumber = Number(params.get("page") ?? 1);
    const enabled = params.get("enabled");
    const items = state.items.filter(
      (s) => enabled === null || s.enabled === (enabled === "true"),
    );
    return r.fulfill({
      json: {
        items: items.slice((pageNumber - 1) * 20, pageNumber * 20),
        total: items.length,
        page: pageNumber,
        page_size: 20,
      },
    });
  });
  await page.route(/\/api\/v2\/subscriptions\/\d+$/, async (r) => {
    const id = Number(new URL(r.request().url()).pathname.split("/").at(-1));
    const method = r.request().method();
    const body = method === "PATCH" ? r.request().postDataJSON() : undefined;
    state.writes.push({
      id,
      method,
      body,
      etag: r.request().headers()["if-match"],
    });
    if (state.hold) await state.hold;
    if (state.failure)
      return r.fulfill({
        status: state.failure.status,
        json: { code: state.failure.code },
      });
    if (method === "DELETE") {
      state.items = state.items.filter((s) => s.id !== id);
      return r.fulfill({ status: 204 });
    }
    state.items = state.items.map((s) =>
      s.id === id ? { ...s, ...body, version: s.version + 1 } : s,
    );
    return r.fulfill({ json: state.items.find((s) => s.id === id) });
  });
  return state;
}
const actions = (page: Page, id = 1) =>
  page.getByRole("group", { name: `研究订阅 ${id}的操作`, exact: true });
const deletion = (page: Page) =>
  page.getByRole("dialog", { name: "删除订阅", exact: true });

test("pause and enable send only enabled and the latest If-Match, locking only their own row", async ({
  page,
}) => {
  const state = await subscriptions(page);
  let release!: () => void;
  state.hold = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.goto("/subscriptions");
  await actions(page)
    .getByRole("button", { name: "暂停 研究订阅 1", exact: true })
    .click();
  await expect.poll(() => state.writes.length).toBe(1);
  for (const label of ["编辑", "暂停", "删除"]) {
    await expect(
      actions(page).getByRole("button", {
        name: `${label} 研究订阅 1`,
        exact: true,
      }),
    ).toBeDisabled();
  }
  await expect(
    actions(page, 2).getByRole("button", {
      name: "暂停 研究订阅 2",
      exact: true,
    }),
  ).toBeEnabled();
  // Even a duplicate DOM click cannot enqueue a second mutation.
  await actions(page)
    .getByRole("button", { name: "暂停 研究订阅 1", exact: true })
    .dispatchEvent("click");
  release();
  await expect(page.getByText("订阅已暂停。", { exact: true })).toBeVisible();
  expect(state.writes).toEqual([
    { id: 1, method: "PATCH", etag: '"3"', body: { enabled: false } },
  ]);
  await actions(page)
    .getByRole("button", { name: "启用 研究订阅 1", exact: true })
    .click();
  await expect(page.getByText("订阅已启用。", { exact: true })).toBeVisible();
  expect(state.writes[1]).toEqual({
    id: 1,
    method: "PATCH",
    etag: '"4"',
    body: { enabled: true },
  });
  expect(state.items[0]).toMatchObject({
    rules: subscription.rules,
    digest_ai_language: subscription.digest_ai_language,
    max_items_per_digest: subscription.max_items_per_digest,
  });
});

test("delete requires confirmation, restores focus on cancel, and prevents duplicate submission", async ({
  page,
}) => {
  const state = await subscriptions(page);
  await page.goto("/subscriptions");
  const trigger = actions(page).getByRole("button", {
    name: "删除 研究订阅 1",
    exact: true,
  });
  await trigger.click();
  await expect(deletion(page)).toContainText("研究订阅 1");
  await expect(deletion(page)).toContainText("已匹配的阅读记录会保留");
  await deletion(page)
    .getByRole("button", { name: "取消", exact: true })
    .click();
  await expect(trigger).toBeFocused();
  await trigger.click();
  await page.keyboard.press("Escape");
  await expect(trigger).toBeFocused();
  expect(state.writes).toEqual([]);
  await trigger.click();
  let release!: () => void;
  state.hold = new Promise<void>((resolve) => {
    release = resolve;
  });
  await deletion(page)
    .getByRole("button", { name: "确认删除", exact: true })
    .click();
  await expect(
    deletion(page).getByRole("button", { name: "正在删除…" }),
  ).toBeDisabled();
  release();
  await expect(deletion(page)).toHaveCount(0);
  await expect(actions(page)).toHaveCount(0);
  await expect(
    page.getByRole("heading", { name: "订阅", exact: true }),
  ).toBeFocused();
  await expect(page.getByText("订阅已删除。", { exact: true })).toBeVisible();
  expect(state.writes).toEqual([
    { id: 1, method: "DELETE", etag: '"3"', body: undefined },
  ]);
});

for (const action of ["暂停", "删除"] as const) {
  for (const failure of [
    {
      status: 409,
      code: "SUBSCRIPTION_VERSION_CONFLICT",
      message: "订阅已被修改",
    },
    { status: 503, code: "UNAVAILABLE", message: "服务暂时不可用" },
  ]) {
    test(`${action} handles ${failure.status} without changing state or retrying the write`, async ({
      page,
    }) => {
      const state = await subscriptions(page);
      state.failure = failure;
      await page.goto("/subscriptions");
      await actions(page)
        .getByRole("button", { name: `${action} 研究订阅 1`, exact: true })
        .click();
      if (action === "删除")
        await deletion(page)
          .getByRole("button", { name: "确认删除", exact: true })
          .click();
      const error =
        action === "删除"
          ? deletion(page).getByRole("alert")
          : page.getByRole("alert");
      await expect(error).toContainText(failure.message);
      expect(state.items[0].enabled).toBe(true);
      expect(state.items.length).toBe(2);
      await error.getByRole("button", { name: "重新载入" }).click();
      await expect(page.getByRole("alert")).toHaveCount(0);
      await expect(
        actions(page).getByRole("button", {
          name: "暂停 研究订阅 1",
          exact: true,
        }),
      ).toBeEnabled();
      expect(state.writes.length).toBe(1);
    });
  }
}

for (const action of ["暂停", "删除"] as const) {
  test(`${action} on the last item of page two keeps the filter and returns to page one`, async ({
    page,
  }) => {
    const state = await subscriptions(page, 21);
    await page.goto("/subscriptions");
    const filter = page.getByRole("combobox", { name: "状态", exact: true });
    await filter.selectOption("true");
    await page.getByRole("button", { name: "下一页" }).click();
    await actions(page, 21)
      .getByRole("button", { name: `${action} 研究订阅 21`, exact: true })
      .click();
    if (action === "删除")
      await deletion(page)
        .getByRole("button", { name: "确认删除", exact: true })
        .click();
    await expect(page.getByRole("navigation", { name: "分页" })).toContainText(
      "第 1 页 · 共 20 条",
    );
    await expect(filter).toHaveValue("true");
    await expect(actions(page)).toBeVisible();
    expect(state.writes.length).toBe(1);
  });
}

test("pausing the sole enabled subscription shows the filtered empty state", async ({
  page,
}) => {
  await subscriptions(page, 1);
  await page.goto("/subscriptions");
  await page
    .getByRole("combobox", { name: "状态", exact: true })
    .selectOption("true");
  await actions(page)
    .getByRole("button", { name: "暂停 研究订阅 1", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "没有符合条件的订阅", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("combobox", { name: "状态", exact: true })
    .selectOption("false");
  await expect(
    actions(page).getByRole("button", { name: "启用 研究订阅 1", exact: true }),
  ).toBeEnabled();
});

for (const enabled of [true, false]) {
  test(`editing an ${enabled ? "enabled" : "paused"} subscription omits lifecycle controls and preserves status`, async ({
    page,
  }) => {
    const state = await subscriptions(page, 1);
    state.items[0].enabled = enabled;
    await page.goto("/subscriptions");
    await actions(page)
      .getByRole("button", { name: "编辑 研究订阅 1", exact: true })
      .click();
    const editor = page.getByRole("dialog", { name: "编辑订阅", exact: true });
    await expect(
      editor.getByRole("checkbox", { name: "启用订阅", exact: true }),
    ).toHaveCount(0);
    await expect(
      editor.getByRole("button", { name: "删除订阅", exact: true }),
    ).toHaveCount(0);
    await editor
      .getByLabel("订阅名称", { exact: true })
      .fill("修改后的研究方向");
    await editor.getByLabel("每日论文上限", { exact: true }).fill("5");
    await editor.getByRole("button", { name: "保存订阅", exact: true }).click();
    await expect(editor).toHaveCount(0);
    await expect(page.getByText("订阅已保存。", { exact: true })).toBeVisible();
    expect(state.writes).toHaveLength(1);
    expect(state.writes[0]).toMatchObject({
      method: "PATCH",
      id: 1,
      etag: '\"3\"',
      body: {
        name: "修改后的研究方向",
        max_items_per_digest: 5,
        digest_ai_enabled: true,
        digest_ai_language: "en",
        rules: subscription.rules,
      },
    });
    expect(state.writes[0].body).not.toHaveProperty("enabled");
    expect(state.writes[0].body).not.toHaveProperty("source_id");
    expect(state.items[0].enabled).toBe(enabled);
    await expect(
      page.getByRole("button", {
        name: `${enabled ? "暂停" : "启用"} 修改后的研究方向`,
        exact: true,
      }),
    ).toBeVisible();
  });
}

for (const mode of ["desktop", "assistant", "mobile", "small"] as const) {
  test(`operation buttons remain readable in ${mode} layout`, async ({
    page,
  }, info) => {
    await subscriptions(page);
    const width = mode === "mobile" ? 390 : mode === "small" ? 320 : 1440;
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(
      mode === "assistant"
        ? "/subscriptions?assistant=subscription"
        : "/subscriptions",
    );
    for (const label of ["编辑", "暂停", "删除"]) {
      await expect(
        actions(page).getByRole("button", {
          name: `${label} 研究订阅 1`,
          exact: true,
        }),
      ).toBeInViewport();
    }
    const boxes = await actions(page)
      .getByRole("button")
      .evaluateAll((buttons) =>
        buttons.map((b) => {
          const r = b.getBoundingClientRect();
          return { x: r.x, y: r.y, right: r.right, bottom: r.bottom };
        }),
      );
    for (let i = 0; i < boxes.length; i++)
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i],
          b = boxes[j];
        expect(
          a.right <= b.x ||
            b.right <= a.x ||
            a.bottom <= b.y ||
            b.bottom <= a.y,
        ).toBe(true);
      }
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath(`subscription-actions-${mode}.png`),
      fullPage: true,
    });
    await actions(page)
      .getByRole("button", { name: "删除 研究订阅 1", exact: true })
      .click();
    await expect(deletion(page)).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(deletion(page)).toHaveCount(0);
    if (mode === "assistant")
      await expect(page.locator(".subscription-assistant-panel")).toBeVisible();
    await actions(page)
      .getByRole("button", { name: "编辑 研究订阅 1", exact: true })
      .click();
    const editor = page.getByRole("dialog", { name: "编辑订阅", exact: true });
    await editor
      .getByRole("button", { name: "保存订阅", exact: true })
      .scrollIntoViewIfNeeded();
    const limit = (await editor
      .getByLabel("每日论文上限", { exact: true })
      .boundingBox())!;
    const ai = (await editor
      .getByLabel("每日邮件 AI 导读", { exact: true })
      .boundingBox())!;
    const language = (await editor
      .getByRole("combobox", { name: "AI 导读语言", exact: true })
      .boundingBox())!;
    expect(limit.y + limit.height).toBeLessThan(ai.y);
    if (width >= 768) {
      expect(ai.x).toBeLessThan(language.x);
      expect(limit.width).toBeGreaterThan(language.width);
    } else {
      expect(ai.y).toBeLessThan(language.y);
    }
    expect(
      await editor.evaluate((el) => el.scrollWidth <= el.clientWidth),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath(`subscription-editor-${mode}.png`),
    });
  });
}
