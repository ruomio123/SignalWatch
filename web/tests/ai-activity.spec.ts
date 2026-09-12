import { test, expect, type Page } from "@playwright/test";
import { setup } from "./fixtures";

const records = ["succeeded", "failed", "unknown", "started"].map(
  (status, i) => ({
    id: `DIAGNOSTIC-${i}-` + "LONG".repeat(20),
    feature: "paper_qa",
    provider: "qwen",
    model: "qwen-" + "long-model-name-".repeat(15),
    status,
    created_at: "2026-09-12T12:00:00Z",
    duration_ms: 5312,
    ...(status === "failed" ? { failure_code: "output_schema_mismatch" } : {}),
  }),
);
async function activity(page: Page) {
  await setup(page);
  await page.route("**/api/v2/ai/usage", (r) =>
    r.fulfill({
      json: {
        today: [
          {
            feature: "config_test",
            calls: 1,
            daily_limit: 0,
            min_interval_seconds: 10,
          },
          {
            feature: "paper_qa",
            calls: 47,
            daily_limit: 60,
            remaining: 13,
            min_interval_seconds: 2,
          },
        ],
        items: [
          {
            day: "2026-09-12",
            feature: "paper_qa",
            calls: 47,
            succeeded: 40,
            failed: 5,
            unknown: 2,
            input_tokens: 123456,
            output_tokens: 1234,
            usage_missing: 2,
          },
        ],
      },
    }),
  );
  const requests: URL[] = [];
  await page.route("**/api/v2/ai/calls?**", (r) => {
    requests.push(new URL(r.request().url()));
    return r.fulfill({ json: { items: records, total: 21 } });
  });
  return requests;
}
for (const width of [1440, 390, 320]) {
  test(`API activity typography and spacing at ${width}px`, async ({
    page,
  }, info) => {
    const requests = await activity(page);
    await page.setViewportSize({ width, height: 1000 });
    await page.goto("/api-keys?tab=activity");
    const summary = page.locator(".ai-usage-summary");
    await expect(summary).toContainText("今日 47 次");
    await expect(summary.locator(".ai-usage-today").first()).toHaveCSS(
      "font-size",
      "13px",
    );
    const filters = page.locator(".ai-call-filters");
    await expect(filters.getByLabel("调用功能")).toBeVisible();
    await expect(summary.locator("p").first()).toHaveCSS("font-size", "12px");
    await expect(filters.locator("label").first()).toHaveCSS(
      "font-size",
      "12px",
    );
    await expect(filters.locator("select").first()).toHaveCSS(
      "font-size",
      "14px",
    );
    await expect(page.locator(".ai-activity-card h2").first()).toHaveCSS(
      "font-size",
      "14px",
    );
    await expect(page.locator(".ai-activity-card td").first()).toHaveCSS(
      "font-size",
      "13px",
    );
    for (const container of [
      summary,
      filters,
      page.locator(".ai-call-pagination"),
    ])
      await expect(container).toHaveCSS(
        "padding-left",
        width < 768 ? "16px" : "20px",
      );
    for (const status of ["成功", "失败", "结果未知", "调用中"])
      await expect(
        page.locator(".ai-activity-card .badge").filter({ hasText: status }),
      ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    if (width < 768) {
      const selects = await filters.locator("select").all();
      const a = (await selects[0].boundingBox())!,
        b = (await selects[1].boundingBox())!;
      expect(b.y).toBeGreaterThan(a.y + a.height);
      expect(b.x).toBeCloseTo(a.x, 0);
    }
    await filters.getByLabel("调用功能").selectOption("paper_qa");
    await filters.getByLabel("调用结果").selectOption("failed");
    await page.getByRole("button", { name: "下一页", exact: true }).click();
    await expect
      .poll(() => requests.at(-1)?.searchParams.get("page"))
      .toBe("2");
    expect(requests.at(-1)?.searchParams.get("feature")).toBe("paper_qa");
    expect(requests.at(-1)?.searchParams.get("status")).toBe("failed");
    await filters.getByLabel("调用结果").selectOption("unknown");
    await expect
      .poll(() => requests.at(-1)?.searchParams.get("page"))
      .toBe("1");
    expect(await page.evaluate(() => scrollX)).toBe(0);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(width);
    const card = (await page
      .locator(".ai-activity-card")
      .first()
      .boundingBox())!;
    expect(card.x).toBe(width < 768 ? 16 : 248);
    await page.screenshot({
      path: info.outputPath(`api-activity-${width}.png`),
      fullPage: true,
      animations: "disabled",
    });
  });
}

test("API activity loading, errors, retry and empty records keep content spacing", async ({
  page,
}) => {
  await setup(page);
  let release!: () => void;
  const hold = new Promise<void>((resolve) => {
    release = resolve;
  });
  let fail = true;
  for (const endpoint of ["usage", "calls?**"]) {
    await page.route(`**/api/v2/ai/${endpoint}`, async (r) => {
      await hold;
      return r.fulfill(
        fail
          ? { status: 503, json: { code: "UNAVAILABLE" } }
          : { json: { items: [], today: [], total: 0 } },
      );
    });
  }
  await page.goto("/api-keys?tab=activity");
  await expect(
    page.locator(".ai-activity-card .ai-activity-body [role='status']").first(),
  ).toBeVisible();
  release();
  await expect(
    page.locator(".ai-activity-card .ai-activity-body [role='alert']"),
  ).toHaveCount(2);
  fail = false;
  const retry = page
    .locator(".ai-activity-card")
    .getByRole("button", { name: "重新载入", exact: true });
  await retry.first().click();
  await expect(retry).toHaveCount(1);
  await retry.first().click();
  await expect(page.getByText("暂无调用记录", { exact: true })).toBeVisible();
  await expect(
    page.getByText("暂无详细调用记录", { exact: true }),
  ).toBeVisible();
});
