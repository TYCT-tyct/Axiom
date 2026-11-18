import { expect, type Page, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";

// The console's call list, summary, sessions tab, export limit and detail pages against the mocked API.

test.use({ timezoneId: "Asia/Shanghai" });

const main = (page: Page) => page.getByRole("main");
const callRows = (page: Page) => page.getByRole("region", { name: "调用记录" }).locator('.lgh, [data-a="lrow"]');
const summaryRows = (page: Page) => page.getByRole("region", { name: "调用汇总" }).locator('.lgh, [data-a="agg"]');

test.describe("calls", () => {
  test("the list pages by cursor, filters live in the address, and a call opens", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls");
    const rows = callRows(page);
    await expect(rows).toHaveCount(51);
    await expect(page.getByRole("link", { name: /16:29:00/ })).toBeVisible();
    await page.getByRole("button", { name: "加载更多" }).click();
    await expect(rows).toHaveCount(101);

    await page.getByLabel("模型").fill("openai/gpt-5");
    await page.getByRole("button", { name: "筛选", exact: true }).click();
    await expect(page).toHaveURL(/model=openai%2Fgpt-5/);
    await expect(rows).toHaveCount(31);

    await page.getByRole("button", { name: "换过线路", exact: true }).click();
    await expect(page).toHaveURL(/fell_back=true/);
    await page.reload();
    await expect(page.getByLabel("模型")).toHaveValue("openai/gpt-5");
    await expect(page).toHaveURL(/fell_back=true/);

    await page.getByRole("link", { name: "清除", exact: true }).click();
    await expect(page.getByLabel("模型")).toHaveValue("");
    await expect(rows).toHaveCount(51);

    await page.getByLabel("模型").fill("openai/gpt-5");
    await page.getByRole("button", { name: "筛选", exact: true }).click();
    await expect(rows).toHaveCount(31);
    await page.evaluate(() => window.scrollTo(0, 600));
    await rows.nth(15).getByRole("link").click();
    await expect(page.getByRole("heading", { level: 1, name: "调用详情" })).toBeVisible();
    await expect(page.getByText("00000000-0000-4000-8000-000000000057")).toBeVisible();
    await expect(page.getByText("缓存读取")).toBeVisible();
    await expect(page.getByText("4,000")).toBeVisible();
    await expect(page.getByText(/00000000-0000-4000-9000-000000000003/)).toBeVisible();
    await expect(page.getByText("chat", { exact: true })).toBeVisible();

    await page.getByRole("link", { name: "返回列表" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "调用记录" })).toBeVisible();
    await expect(page).toHaveURL(/model=openai%2Fgpt-5/);
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(200);
  });

  test("inline detail drawer shows attempts and route, and O8/O24 items stay out", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls?fell_back=true");
    const firstDataRow = page.getByRole("region", { name: "调用记录" }).locator('[data-a="lrow"]').first();
    await firstDataRow.click({ position: { x: 180, y: 12 } });
    await expect(page).toHaveURL(/call=00000000-0000-4000-8000-/);
    await expect(page.getByText("尝试与路由")).toBeVisible();
    await expect(page.getByText(/upstream_overloaded/)).toBeVisible();
    await expect(page.getByLabel("关闭")).toBeVisible();
    await expect(page.getByRole("button", { name: "慢于 5 秒" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "复制 cURL" })).toHaveCount(0);
    await expect(page.locator(".lglive")).toHaveCount(0);
  });

  test("the summary groups the same calls and export handles limits", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls");
    const summary = summaryRows(page);
    await expect(summary).toHaveCount(2);
    await page.getByRole("button", { name: "按模型", exact: true }).click();
    await expect(page).toHaveURL(/group_by=model/);
    await expect(summary).toHaveCount(5);

    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "导出 CSV" }).click();
    expect((await download).suggestedFilename()).toMatch(/^calls-\d{4}-\d{2}-\d{2}\.csv$/);

    await page.goto("/console/calls?since=2025-01-01T00:00:00.000Z&until=2026-11-01T00:00:00.000Z");
    await page.getByRole("button", { name: "导出 CSV" }).click();
    await expect(page.getByRole("alert")).toContainText("导出超出上限");
    await expect(page.getByRole("alert")).toContainText("31 天");
    await page.getByRole("button", { name: "设为近 7 天" }).click();
    await expect(page).toHaveURL(/since=/);
    await expect(page.getByRole("alert")).toHaveCount(0);
  });

  test("sessions tab groups calls by client and handles 404 nodes", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls");
    await page.getByRole("button", { name: "按会话", exact: true }).click();
    await expect(page).toHaveURL(/tab=sessions/);
    const sessionRows = page.getByRole("region", { name: "按会话调用" }).locator('[data-a="session"]');
    await expect(sessionRows.first()).toBeVisible();
    await sessionRows.first().click();
    await expect(page).not.toHaveURL(/tab=sessions/);
    await expect(page).toHaveURL(/client_app=/);

    await page.route("**/calls/sessions*", (route) =>
      route.fulfill({ status: 404, json: { error: { code: "not_found" } } }),
    );
    await page.goto("/console/calls?tab=sessions");
    await expect(main(page).getByRole("status").first()).toContainText("按会话查看");
  });

  test("a failed call, cached call, and free session call match API fields", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls/00000000-0000-4000-8000-000000000007");
    await expect(page.getByText("upstream_timeout").first()).toBeVisible();
    await expect(main(page).getByText("失败", { exact: true }).first()).toBeVisible();
    await page.goto("/console/calls/00000000-0000-4000-8000-000000000003");
    await expect(page.getByText("命中缓存").first()).toBeVisible();
    await page.goto("/console/calls/00000000-0000-4000-8000-000000000011");
    await expect(page.getByText("是（已免单）")).toBeVisible();
    await page.goto("/console/calls/00000000-0000-4000-8000-000000000002");
    await expect(page.getByText("session-2")).toBeVisible();
    await expect(page.getByText("chat", { exact: true })).toBeVisible();
    await page.goto("/console/calls/00000000-0000-4000-8000-0000000fffff");
    await expect(page.getByRole("status").getByText("找不到这次调用")).toBeVisible();
  });

  test("no calls, no matches, and permission denied each say what to do", async ({ page }) => {
    const state = await mockConsole(page, "empty");
    signedInAs(state);
    await page.goto("/console/calls");
    await expect(page.getByRole("status").filter({ hasText: "还没有调用记录" })).toBeVisible();
    await page.goto("/console/calls?model=none");
    await expect(page.getByRole("status").filter({ hasText: "没有符合条件的调用" })).toBeVisible();

    await page.route("**/api/console/v1/calls?*", (route) =>
      route.fulfill({ status: 403, json: { error: { code: "forbidden", message: "forbidden" } } }),
    );
    await page.goto("/console/calls");
    await expect(page.getByRole("alert").filter({ hasText: "没有权限" })).toBeVisible();
    await page.unroute("**/api/console/v1/calls?*");

    await page.route("**/api/console/v1/calls?*", (route) =>
      route.fulfill({ status: 200, json: { data: [], next_cursor: null } }),
    );
    await page.goto("/console/calls?status=error");
    await expect(page.getByRole("alert").filter({ hasText: "服务端暂不支持该筛选条件" })).toBeVisible();
  });

  test("a free call is marked, and the overview lists the latest failures", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls");
    await expect(
      page.getByRole("region", { name: "调用记录" }).locator('[data-a="lrow"]').filter({ hasText: /免单/ }).first(),
    ).toBeVisible();
    await page.getByRole("link", { name: "概览" }).click();
    await expect(page.getByRole("heading", { name: "最近的报错" })).toBeVisible();
  });
});
