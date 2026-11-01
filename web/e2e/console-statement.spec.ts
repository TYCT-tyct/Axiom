import { expect, test } from "@playwright/test";
import { failOnce, mockConsole, signedInAs } from "./console-api.ts";

// The console's credit statement against the mocked API (console-api.ts): paging, refund holds, export, and the states
// of a page with no rows.

test.use({ timezoneId: "Asia/Shanghai" });

test.describe("statement", () => {
  test("the movements page by cursor, a charge opens its call, and the export is a file", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/statement");
    const rows = page.getByRole("row");
    await expect(rows).toHaveCount(51);
    // A charge says what it charged, and the call it opens is that one.
    await expect(page.getByRole("link", { name: /^调用 00000000/ }).first()).toBeVisible();
    await rows.nth(1).getByRole("link").click();
    await expect(page.getByRole("heading", { level: 1, name: "调用详情" })).toBeVisible();
    await page.goBack();
    await page.getByRole("button", { name: "加载更多" }).click();
    await expect(rows).toHaveCount(101);

    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "导出 CSV" }).click();
    expect((await download).suggestedFilename()).toMatch(/^statement-\d{4}-\d{2}-\d{2}\.csv$/);
    expect(state.log.filter((line) => line === "GET /statement.csv")).toHaveLength(1);
  });

  test("on a phone a charge still opens its call, and an unknown balance says so", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 780 });
    const state = await mockConsole(page);
    state.statement = state.statement.map((row, i) => (i === 0 ? { ...row, balance_after: null } : row));
    signedInAs(state);
    await page.goto("/console/statement");
    const first = page.getByRole("row").nth(1);
    await expect(first).toContainText("不可用");
    const call = first.getByRole("link", { name: /^调用 00000000/ });
    await expect(call).toBeVisible();
    await call.click();
    await expect(page.getByRole("heading", { level: 1, name: "调用详情" })).toBeVisible();
  });

  test("refund holds leave the balance visible and completed refunds reduce it", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/statement");
    const hold = page.getByRole("row").filter({ hasText: "退款冻结" }).first();
    await expect(hold).toContainText("冻结额变动");
    await expect(hold).toContainText("冻结：US$0.0015");
    const refund = page.getByRole("row").filter({ hasText: "退款完成" }).first();
    await expect(refund).toContainText("-US$0.0015");
    await expect(refund).toContainText("冻结：US$0.00");
  });

  test("a denied statement shows no rows and failed exports can be retried", async ({ page }) => {
    const state = await mockConsole(page, "usual", { permissions: [] });
    signedInAs(state);
    await page.goto("/console/statement");
    await expect(page.getByRole("heading", { name: "没有权限" })).toBeVisible();
    await expect(page.getByRole("row")).toHaveCount(0);
    state.permissions = ["balance.view"];
    await page.reload();
    await expect(page.getByRole("row")).toHaveCount(51);
    await failOnce(page, "**/statement.csv");
    await page.getByRole("button", { name: "导出 CSV" }).click();
    await expect(page.getByRole("alert")).toContainText("导出没有成功");
    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "导出 CSV" }).click();
    expect((await download).suggestedFilename()).toMatch(/\.csv$/);
    await expect(page.getByRole("alert")).toHaveCount(0);
  });

  test("an empty segment can continue to older movements", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    let first = true;
    await page.route("**/statement?*", async (route) => {
      if (!first) return route.fallback();
      first = false;
      await route.fulfill({ status: 200, json: { data: [], next_cursor: ":0" } });
    });
    await page.goto("/console/statement");
    await expect(page.getByRole("heading", { name: "这一段没有余额变动" })).toBeVisible();
    await page.getByRole("button", { name: "加载更多" }).click();
    await expect(page.getByRole("row")).toHaveCount(51);
  });

  test("an account with no movements says so", async ({ page }) => {
    const state = await mockConsole(page, "empty");
    signedInAs(state);
    await page.goto("/console/statement");
    await expect(page.getByRole("heading", { name: "还没有额度变动" })).toBeVisible();
    await expect(page.getByRole("row")).toHaveCount(0);
  });
});
