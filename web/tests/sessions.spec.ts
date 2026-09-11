import { test, expect } from "@playwright/test";
import { setup, subscription } from "./fixtures";

test("concurrent expired requests share one refresh and stay on the current page", async ({
  page,
}) => {
  await setup(page);
  let refreshes = 0;
  const oldRequests: string[] = [];
  await page.route("**/api/v2/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v2/auth/refresh") {
      refreshes++;
      expect(route.request().headers()["x-signalwatch-session"]).toBe("1");
      await new Promise((resolve) => setTimeout(resolve, 100));
      await route.fulfill({
        json: {
          access_token: "renewed-token",
          token_type: "Bearer",
          expires_in: 900,
        },
      });
    } else if (
      route.request().headers().authorization !== "Bearer renewed-token"
    ) {
      oldRequests.push(path);
      await route.fulfill({ status: 401, json: { code: "UNAUTHORIZED" } });
    } else await route.fallback();
  });
  await page.goto("/subscriptions");
  await expect(
    page.getByRole("heading", { name: "研究订阅", exact: true }),
  ).toBeVisible();
  expect(oldRequests.length).toBeGreaterThanOrEqual(2);
  expect(refreshes).toBe(1);
  await expect(page).toHaveURL(/\/subscriptions$/);
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "研究订阅", exact: true }),
  ).toBeVisible();
  expect(refreshes).toBe(1);
});

test("renewal while a form is open preserves unsaved edits", async ({
  page,
}) => {
  await setup(page);
  let expired = false,
    refreshes = 0;
  await page.route("**/api/v2/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v2/auth/refresh") {
      refreshes++;
      await route.fulfill({ json: { access_token: "fresh-token" } });
      return;
    }
    if (
      expired &&
      route.request().headers().authorization !== "Bearer fresh-token"
    ) {
      await route.fulfill({ status: 401, json: { code: "UNAUTHORIZED" } });
      return;
    }
    if (path === "/api/v2/subscriptions") {
      await route.fulfill({
        json: {
          items: [
            {
              ...subscription,
              backfill: {
                state: refreshes ? "complete" : "processing",
                processed: 5,
                matched: 1,
              },
            },
          ],
          total: 1,
        },
      });
      return;
    }
    await route.fallback();
  });
  await page.goto("/subscriptions");
  await page.getByRole("button", { name: "编辑 研究订阅" }).click();
  await page.getByLabel("订阅名称").fill("尚未保存的修改");
  expired = true;
  await expect.poll(() => refreshes, { timeout: 10000 }).toBe(1);
  await expect(page.getByRole("dialog", { name: "编辑订阅" })).toBeVisible();
  await expect(page.getByLabel("订阅名称")).toHaveValue("尚未保存的修改");
});

test("an expired write is retried once with its original body and ETag", async ({
  page,
}) => {
  await setup(page);
  let writes = 0,
    refreshes = 0;
  await page.route("**/api/v2/auth/refresh", (route) => {
    refreshes++;
    return route.fulfill({ json: { access_token: "write-token" } });
  });
  await page.route("**/api/v2/subscriptions/1", async (route) => {
    writes++;
    expect(route.request().headers()["if-match"]).toBe('"3"');
    expect(route.request().postDataJSON().name).toBe("新的方向");
    if (route.request().headers().authorization !== "Bearer write-token")
      await route.fulfill({ status: 401, json: { code: "UNAUTHORIZED" } });
    else await route.fulfill({ json: subscription });
  });
  await page.goto("/subscriptions");
  await page.getByRole("button", { name: "编辑 研究订阅" }).click();
  await page.getByLabel("订阅名称").fill("新的方向");
  await page.getByRole("button", { name: "保存订阅" }).click();
  await expect(page.getByText("订阅已保存。", { exact: true })).toBeVisible();
  expect(writes).toBe(2);
  expect(refreshes).toBe(1);
});

for (const failure of ["unavailable", "network"] as const) {
  test(`${failure} during renewal keeps the session available for retry`, async ({
    page,
  }) => {
    await setup(page);
    let recovered = false;
    await page.route("**/api/v2/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path === "/api/v2/auth/refresh") {
        if (recovered)
          await route.fulfill({ json: { access_token: "recovered-token" } });
        else if (failure === "network")
          await route.abort("internetdisconnected");
        else
          await route.fulfill({
            status: 503,
            json: { code: "AUTH_UNAVAILABLE" },
          });
      } else if (
        route.request().headers().authorization !== "Bearer recovered-token"
      )
        await route.fulfill({ status: 401, json: { code: "UNAUTHORIZED" } });
      else await route.fallback();
    });
    await page.goto("/subscriptions");
    await expect(page.getByRole("alert").first()).toBeVisible();
    await expect(page).toHaveURL(/\/subscriptions$/);
    await expect(page.getByRole("alert")).toHaveCount(3);
    recovered = true;
    for (let remaining = 3; remaining > 0; remaining--) {
      await page.getByRole("button", { name: "重新载入" }).first().click();
      await expect(page.getByRole("alert")).toHaveCount(remaining - 1);
    }
    await expect(page.getByRole("button", { name: "新建订阅" })).toBeEnabled();
    await expect(
      page.getByRole("heading", { name: "研究订阅", exact: true }),
    ).toBeVisible();
  });
}

test("late renewal cannot sign the user back in after logout", async ({
  page,
}) => {
  await setup(page);
  let release!: () => void,
    started = false;
  const wait = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/v2/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v2/auth/refresh") {
      started = true;
      await wait;
      await route.fulfill({ json: { access_token: "too-late-token" } });
    } else if (path === "/api/v2/auth/logout")
      await route.fulfill({ status: 204 });
    else await route.fulfill({ status: 401, json: { code: "UNAUTHORIZED" } });
  });
  await page.goto("/app");
  await expect.poll(() => started).toBe(true);
  await page.getByRole("button", { name: "退出登录", exact: true }).click();
  await expect(page).toHaveURL(/\/login/);
  const response = page.waitForResponse("**/api/v2/auth/refresh");
  release();
  await response;
  await expect(page.getByRole("heading", { name: "欢迎回来" })).toBeVisible();
  expect(
    await page.evaluate(() => localStorage.getItem("signalwatch.session")),
  ).toBeNull();
  expect(
    await page.evaluate(() => localStorage.getItem("signalwatch.access_token")),
  ).toBeNull();
});

test("logout failure is visible and can be retried without losing the session", async ({
  page,
}) => {
  await setup(page);
  let failed = true;
  await page.route("**/api/v2/auth/logout", (route) =>
    route.fulfill(
      failed
        ? { status: 503, json: { code: "AUTH_UNAVAILABLE" } }
        : { status: 204 },
    ),
  );
  await page.goto("/app");
  await page.getByRole("button", { name: "退出登录", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("服务暂时不可用");
  await expect(page).toHaveURL(/\/app$/);
  failed = false;
  await page.getByRole("button", { name: "重新载入" }).click();
  await expect(page).toHaveURL(/\/login/);
});

test("logout is synchronized to another open tab", async ({
  page,
  context,
}) => {
  await setup(page);
  await page.goto("/app");
  const other = await context.newPage();
  await setup(other);
  await other.goto("/subscriptions");
  await expect(
    other.getByRole("heading", { name: "研究订阅", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "退出登录", exact: true }).click();
  await expect(page).toHaveURL(/\/login/);
  await expect(other).toHaveURL(/\/login/);
});

test("a different account's cookie cannot replay an old account's form", async ({
  page,
}) => {
  await setup(page);
  const token = (sub: string) =>
    `${Buffer.from('{"alg":"HS256"}').toString("base64url")}.${Buffer.from(JSON.stringify({ sub })).toString("base64url")}.test-signature`;
  await page.addInitScript((value) => {
    localStorage.setItem(
      "signalwatch.session",
      JSON.stringify({ id: "account-one", accessToken: value }),
    );
  }, token("1"));
  let writes = 0;
  await page.route("**/api/v2/auth/refresh", (route) =>
    route.fulfill({ json: { access_token: token("2") } }),
  );
  await page.route("**/api/v2/subscriptions/1", (route) => {
    writes++;
    return route.fulfill({ status: 401, json: { code: "UNAUTHORIZED" } });
  });
  await page.goto("/subscriptions");
  await page.getByRole("button", { name: "编辑 研究订阅" }).click();
  await page.getByRole("button", { name: "保存订阅" }).click();
  await expect(page).toHaveURL(/\/login/);
  expect(writes).toBe(1);
});
