import { expect, test } from "@playwright/test";
import { collectErrors } from "./collect-errors.ts";
import { mockConsole, signedInAs } from "./console-api.ts";
import { aKey, defaultWorkspace, type Schemas } from "./console-data.ts";

// The console's /usage page: range limits, exact group sums, empty state, filters, states, and CSV export.

test.use({ timezoneId: "UTC" });

const rangeUrl = (since: string, until: string, more = "") =>
  `/console/usage?range=custom&since=${encodeURIComponent(since)}&until=${encodeURIComponent(until)}${more}`;

const usageRequests = (log: string[]) => log.filter((entry) => entry.includes("/usage/statistics")).length;

const parseIntCell = (text: string) => BigInt(text.replaceAll(",", "").trim());

const parseMoneyNano = (text: string) => {
  const cleaned = text.replace(/[^0-9.]/g, "");
  const [whole = "0", frac = ""] = cleaned.split(".");
  return BigInt(whole) * 1_000_000_000n + BigInt(frac.padEnd(9, "0").slice(0, 9));
};

test.describe("usage", () => {
  test("blocks submission and explains limits when the range exceeds API bounds", async ({ page }) => {
    const errors = collectErrors(page);
    const state = await mockConsole(page);
    signedInAs(state);

    // 10 days on hourly granularity (> 7-day limit): page must not query the API and explains the limit.
    await page.goto(rangeUrl("2026-10-05T00:00:00Z", "2026-10-15T00:00:00Z", "&granularity=hour"));
    await expect(page.getByRole("alert")).toContainText("7 天");
    expect(usageRequests(state.log)).toBe(0);

    // Switching to daily lets the 10-day query submit normally.
    await page.getByRole("button", { name: "改为按天查看" }).click();
    await expect(page.locator("table.ust tbody tr").first()).toBeVisible();
    expect(usageRequests(state.log)).toBeGreaterThan(0);

    const beforeOver90 = usageRequests(state.log);
    // 92 days (> 90-day daily/weekly limit): blocked before hitting API.
    await page.goto(rangeUrl("2026-07-01T00:00:00Z", "2026-10-01T00:00:00Z", "&granularity=day"));
    await expect(page.getByRole("alert")).toContainText("90 天");
    expect(usageRequests(state.log)).toBe(beforeOver90);

    // Custom calendar also disables Apply and explains the limit when selecting > 7 days in hourly mode.
    await page.goto(rangeUrl("2026-10-10T00:00:00Z", "2026-10-15T00:00:00Z", "&granularity=hour"));
    await expect(page.locator("table.ust tbody tr").first()).toBeVisible();
    await page.locator('[data-a="r"][data-i="4"]').click();
    await page.locator('[data-a="cd"]').first().click();
    await page.locator('[data-a="cd"]').nth(15).click();
    await expect(page.locator('[data-a="cok"]')).toBeDisabled();
    await expect(page.locator(".uscal")).toContainText("上限为 7 天");
    expect(errors).toEqual([]);
  });

  test("groups by model, key and workspace so group rows sum to totals", async ({ page }) => {
    const errors = collectErrors(page);
    const state = await mockConsole(page);
    signedInAs(state);

    // Add a second workspace and mix workspaces onto a subset of fixture calls to verify non-trivial workspace grouping.
    const secondWorkspace: Schemas["Workspace"] = {
      id: `ws_${"1".repeat(26)}` as Schemas["WorkspaceId"],
      name: "Research",
      is_default: false,
      disabled: false,
    };
    await page.route("**/api/console/v1/workspaces", (route) =>
      route.fulfill({ status: 200, json: { data: [defaultWorkspace, secondWorkspace] } }),
    );
    state.calls = state.calls.map((c, i) => ({
      ...c,
      workspace_id: i % 3 === 0 ? secondWorkspace.id : defaultWorkspace.id,
    }));

    await page.goto(rangeUrl("2026-10-08T00:00:00Z", "2026-10-15T00:00:00Z", "&granularity=day"));
    const rows = page.locator("table.ust tbody tr");
    const footer = page.locator("table.ust tfoot tr td");
    await expect(rows).toHaveCount(4);

    let expectedTotalSpend = 0n;
    let expectedTotalReqs = 0n;
    for (const [groupBtn, expectedCount] of [
      ["按模型", 4],
      ["按 Key", 3],
      ["按工作区", 2],
    ] as const) {
      await page.getByRole("button", { name: groupBtn }).click();
      await expect(rows).toHaveCount(expectedCount);
      const count = await rows.count();
      let sumSpend = 0n;
      let sumReqs = 0n;
      for (let i = 0; i < count; i += 1) {
        const cells = rows.nth(i).locator("td");
        sumSpend += parseMoneyNano((await cells.nth(1).textContent()) ?? "");
        sumReqs += parseIntCell((await cells.nth(3).textContent()) ?? "");
      }
      const footSpend = parseMoneyNano((await footer.nth(1).textContent()) ?? "");
      const footReqs = parseIntCell((await footer.nth(3).textContent()) ?? "");
      expect(sumSpend).toBe(footSpend);
      expect(sumReqs).toBe(footReqs);
      if (expectedTotalReqs === 0n) {
        expectedTotalSpend = footSpend;
        expectedTotalReqs = footReqs;
      } else {
        expect(footSpend).toBe(expectedTotalSpend);
        expect(footReqs).toBe(expectedTotalReqs);
      }
    }
    expect(errors).toEqual([]);
  });

  test("shows an empty state instead of an empty chart when no calls exist in range", async ({ page }) => {
    const errors = collectErrors(page);
    const state = await mockConsole(page, "empty");
    signedInAs(state);

    await page.goto(rangeUrl("2026-10-10T00:00:00Z", "2026-10-17T00:00:00Z", "&granularity=day"));
    await expect(page.getByText("这段时间没有调用记录")).toBeVisible();
    await expect(page.locator(".usch svg")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "导出 CSV" })).toBeDisabled();
    expect(errors).toEqual([]);
  });

  test("supports filtering, drill-down, CSV download, backfill warning, 404, 500 retry and missing permission", async ({
    page,
  }) => {
    const errors = collectErrors(page);
    const state = await mockConsole(page);
    state.usageBackfillComplete = false;
    signedInAs(state);

    await page.goto(rangeUrl("2026-10-14T00:00:00Z", "2026-10-15T00:00:00Z", "&granularity=hour"));
    await expect(page.getByRole("status").filter({ hasText: "回填" })).toBeVisible();

    // Filter by key from chip dropdown.
    await page.locator('[data-a="chip"][data-i="k"]').click();
    await page.locator('[data-a="pickk"]').first().click();
    await expect(page).toHaveURL(/key_id=/);

    // Clear key filter and verify CSV download works.
    await page.locator('[data-a="chip"][data-i="k"]').click();
    await page.locator('[data-a="clrk"]').click();
    await expect(page).not.toHaveURL(/key_id=/);
    const downloadPromise = page.waitForEvent("download");
    await page.getByRole("button", { name: "导出 CSV" }).click();
    expect((await downloadPromise).suggestedFilename()).toMatch(/^usage-model-2026-10-14\.csv$/);

    // Clicking a populated bucket hit rect navigates to /calls with that bucket's time window.
    await page.locator('.usch rect[data-a="day"]').nth(7).click();
    await expect(page).toHaveURL(/\/console\/calls\?.*since=2026-10-14/);

    // 500 across initial retries surfaces Retry button and recovers when re-queried.
    let failStats = true;
    await page.route("**/api/console/v1/usage/statistics*", (route) =>
      failStats
        ? route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } })
        : route.fallback(),
    );
    await page.goto(rangeUrl("2026-10-14T00:00:00Z", "2026-10-15T00:00:00Z", "&granularity=day"));
    const retryBtn = page.getByRole("button", { name: "重试" });
    await expect(retryBtn).toBeVisible({ timeout: 10_000 });
    failStats = false;
    await retryBtn.click();
    await expect(page.locator("table.ust tbody tr").first()).toBeVisible();

    // 404 from an older server shows unavailable message instead of a generic crash.
    await page.route("**/api/console/v1/usage/statistics*", (route) =>
      route.fulfill({ status: 404, json: { error: { code: "not_found", message: "not_found" } } }),
    );
    await page.goto(rangeUrl("2026-10-08T00:00:00Z", "2026-10-15T00:00:00Z", "&granularity=day"));
    await expect(page.getByText("当前节点暂不支持用量统计")).toBeVisible();

    // Missing calls.view permission hides nav entry and renders forbidden state on /usage.
    await page.unroute("**/api/console/v1/usage/statistics*");
    state.permissions = ["api_keys.view"];
    state.keys = [aKey(1)];
    await page.goto("/console/usage");
    await expect(page.getByRole("link", { name: "用量" })).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "没有权限" })).toBeVisible();
    // Account with only view permission has clean nav expectations, no unhandled errors except intentional 403/404/500.
    expect(errors.filter((e) => !/404|500|403/.test(e))).toEqual([]);
  });
});
