import { test, expect } from "@playwright/test";
import { profile, setup, source, subscription } from "./fixtures";

test("manual creation uses the source catalog array and saves the subscription", async ({
  page,
}) => {
  await setup(page);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  let created = false;
  let body: Record<string, unknown> | undefined;
  await page.route("**/api/v2/subscriptions?**", (r) =>
    r.fulfill({
      json: {
        items: created ? [{ ...subscription, name: "手动研究订阅" }] : [],
        total: created ? 1 : 0,
        page: 1,
        page_size: 20,
      },
    }),
  );
  await page.route("**/api/v2/subscriptions", (r) => {
    expect(r.request().method()).toBe("POST");
    body = r.request().postDataJSON();
    created = true;
    return r.fulfill({
      status: 201,
      json: { ...subscription, name: "手动研究订阅" },
    });
  });
  await page.goto("/subscriptions");
  await page.getByRole("button", { name: "新建订阅", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "新建订阅", exact: true });
  await expect(dialog).toBeVisible();
  await expect(
    dialog.getByRole("combobox", { name: "来源", exact: true }),
  ).toHaveValue(String(source.id));
  await expect(dialog.getByLabel("每日论文上限")).toHaveValue(
    String(profile.max_items_per_digest),
  );
  await expect(
    dialog.getByLabel("每日邮件 AI 导读", { exact: true }),
  ).not.toBeChecked();
  await dialog.getByLabel("订阅名称").fill("手动研究订阅");
  await dialog
    .getByRole("combobox", { name: "分类", exact: true })
    .selectOption("cs.CL");
  await dialog
    .getByLabel("关键词（每行一个，留空匹配整个分类）")
    .fill("reasoning\n language model");
  await dialog.getByRole("button", { name: "保存订阅" }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole("heading", { name: "手动研究订阅", exact: true }),
  ).toBeVisible();
  expect(body).toMatchObject({
    source_id: source.id,
    name: "手动研究订阅",
    enabled: true,
    max_items_per_digest: profile.max_items_per_digest,
    digest_ai_enabled: false,
    rules: { category: "cs.CL", keywords: ["reasoning", "language model"] },
  });
  expect(errors).toEqual([]);
});

for (const width of [1440, 390])
  test(`subscription creation buttons stay together at ${width}px`, async ({
    page,
  }, info) => {
    await setup(page);
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/subscriptions");
    const manual = page.getByRole("button", { name: "新建订阅", exact: true });
    const ai = page.getByRole("button", { name: "AI 创建订阅", exact: true });
    await expect(manual).toBeEnabled();
    const a = (await manual.boundingBox())!;
    const b = (await ai.boundingBox())!;
    expect(Math.abs(a.y - b.y)).toBeLessThan(2);
    expect(b.x - a.x - a.width).toBeGreaterThanOrEqual(0);
    expect(b.x - a.x - a.width).toBeLessThanOrEqual(16);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath(`subscriptions-${width}.png`),
      fullPage: true,
    });
    await ai.click();
    await expect(page.getByRole("dialog", { name: "订阅助手" })).toBeVisible();
  });

test("an empty catalog disables creation with an explanation", async ({
  page,
}) => {
  await setup(page);
  await page.route("**/api/v2/sources", (r) => r.fulfill({ json: [] }));
  await page.goto("/subscriptions");
  await expect(
    page.getByText("暂无可用论文来源，暂时无法手动创建订阅。"),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "新建订阅", exact: true }),
  ).toBeDisabled();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});
