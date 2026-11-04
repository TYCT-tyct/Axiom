import { expect, type Page, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";

// The console's call list, summary and detail pages against the mocked API.

test.use({ timezoneId: "Asia/Shanghai" });

const main = (page: Page) => page.getByRole("main");

test.describe("calls", () => {
  test("the list pages by cursor, filters live in the address, and a call opens", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls");
    const rows = page.getByRole("table", { name: "调用记录" }).getByRole("row");
    await expect(rows).toHaveCount(51);
    // Times show in the viewer's zone and say which it is.
    await expect(page.getByRole("link", { name: /16:29:00/ })).toBeVisible();
    await page.getByRole("button", { name: "加载更多" }).click();
    await expect(rows).toHaveCount(101);

    await page.getByLabel("模型").fill("openai/gpt-5");
    await page.getByRole("button", { name: "筛选" }).click();
    await expect(page).toHaveURL(/model=openai%2Fgpt-5/);
    await expect(rows).toHaveCount(31);
    await page.reload();
    await expect(page.getByLabel("模型")).toHaveValue("openai/gpt-5");
    await expect(rows).toHaveCount(31);

    await page.getByRole("link", { name: "清除" }).click();
    await expect(page.getByLabel("模型")).toHaveValue("");
    await expect(rows).toHaveCount(51);

    await rows.nth(1).getByRole("link").click();
    await expect(page.getByRole("heading", { level: 1, name: "调用详情" })).toBeVisible();
    await expect(page.getByText("00000000-0000-4000-8000-000000000001")).toBeVisible();
    await expect(page.getByText("cache_read")).toBeVisible();
  });

  test("the summary groups the same calls and exports them", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls");
    const summary = page.getByRole("table", { name: "汇总" });
    // Every fixture call is on the same day, so one day group and its header.
    await expect(summary.getByRole("row")).toHaveCount(2);
    await page.getByLabel("分组").selectOption("model");
    await expect(page).toHaveURL(/group_by=model/);
    await expect(summary.getByRole("row")).toHaveCount(5);

    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "导出 CSV" }).click();
    expect((await download).suggestedFilename()).toMatch(/^calls-\d{4}-\d{2}-\d{2}\.csv$/);
  });

  test("a failed call says so, and a call that is not the account's is not found", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls/00000000-0000-4000-8000-000000000007");
    await expect(page.getByText("upstream_timeout")).toBeVisible();
    await expect(main(page).getByText("失败")).toBeVisible();
    await page.goto("/console/calls/00000000-0000-4000-8000-0000000fffff");
    await expect(page.getByRole("heading", { name: "找不到这次调用" })).toBeVisible();
  });

  test("no calls, no matches, and a refused filter each say what to do", async ({ page }) => {
    const state = await mockConsole(page, "empty");
    signedInAs(state);
    await page.goto("/console/calls");
    await expect(page.getByRole("heading", { name: "还没有调用记录" })).toBeVisible();
    await page.goto("/console/calls?model=none");
    await expect(page.getByRole("heading", { name: "没有符合条件的调用" })).toBeVisible();
  });

  test("a free call is marked, and the overview lists the latest failures", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls");
    await expect(
      page.getByRole("table", { name: "调用记录" }).getByRole("row", { name: /免单/ }).first(),
    ).toBeVisible();
    await page.getByRole("link", { name: "概览" }).click();
    await expect(page.getByRole("heading", { name: "最近的报错" })).toBeVisible();
    await expect(page.getByText("upstream_timeout").first()).toBeVisible();
  });
});
