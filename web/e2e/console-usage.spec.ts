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

    // Empty result during historical backfill keeps the backfill banner and uses the rolled-up copy.
    state.usageBackfillComplete = false;
    await page.reload();
    await expect(page.getByText("尚无已汇总记录")).toBeVisible();
    await expect(page.getByRole("status").filter({ hasText: "回填" })).toBeVisible();
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
    expect((await downloadPromise).suggestedFilename()).toMatch(/^usage-model-USD-2026-10-14\.csv$/);

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

  test("handles unreported tokens, missing payload fields, multi-currency switcher, and key pagination", async ({
    page,
  }) => {
    const errors = collectErrors(page);
    const state = await mockConsole(page);
    const baseCall = state.calls[0];
    expect(baseCall).toBeDefined();
    if (!baseCall) return;
    state.keys = Array.from({ length: 51 }, (_, i) => aKey(i + 1));
    state.calls = [
      {
        ...baseCall,
        at: "2026-10-14T10:00:00Z",
        key_id: aKey(51).id,
        model: "openai/gpt-5",
        usage: {},
        price: { amount: "500000000", currency: "USD" },
        error_kind: "",
      },
      {
        ...baseCall,
        at: "2026-10-14T11:00:00Z",
        key_id: aKey(1).id,
        model: "anthropic/claude-sonnet-4",
        usage: { input: 1000, output: 200 },
        price: { amount: "8000000000", currency: "CNY" },
        error_kind: "",
      },
    ];
    signedInAs(state);

    await page.goto(rangeUrl("2026-10-14T00:00:00Z", "2026-10-15T00:00:00Z", "&granularity=day"));
    // Both USD and CNY appear in the currency switcher; selecting CNY shows ¥ totals and exports scoped CSV.
    const switcher = page.getByRole("group", { name: "币种" });
    await expect(switcher.getByRole("button", { name: "USD" })).toHaveAttribute("aria-pressed", "true");
    await expect(switcher.getByRole("button", { name: "CNY" })).toBeVisible();
    // Unreported tokens in USD stay "—" (never 0) and PPM is "—".
    await expect(page.locator('.usm [data-tw="tk"]')).toHaveText("—");
    await expect(page.locator('.usm [data-tw="pp"]')).toHaveText("—");

    await switcher.getByRole("button", { name: "CNY" }).click();
    await expect(page.locator(".usm > div:nth-child(1) .cy")).toHaveText("¥");
    await expect(page.getByRole("button", { name: "导出 CSV (CNY)" })).toBeEnabled();

    // Key selector loads page 2 via next_cursor and filters by the 51st key.
    await page.locator('[data-a="chip"][data-i="k"]').click();
    await expect(page.getByRole("button", { name: "Key 51" })).toHaveCount(0);
    await page.getByRole("button", { name: "加载更多 Key" }).click();
    await expect(page.getByRole("button", { name: "Key 51" })).toBeVisible();
    const statPromise = page.waitForRequest(
      (req) => req.url().includes("/usage/statistics") && req.url().includes(`key_id=${aKey(51).id}`),
    );
    await page.getByRole("button", { name: "Key 51" }).click();
    await statPromise;

    // Missing required totals/buckets payload enters unavailable state and disables export.
    await page.route("**/api/console/v1/usage/statistics*", (route) =>
      route.fulfill({ status: 200, json: { since: "2026-10-14T00:00:00Z" } }),
    );
    await page.goto(rangeUrl("2026-10-14T00:00:00Z", "2026-10-15T00:00:00Z", "&granularity=day"));
    await expect(page.getByText("当前节点暂不支持用量统计")).toBeVisible();
    await expect(page.getByRole("button", { name: "导出 CSV" })).toBeDisabled();
    expect(errors).toEqual([]);
  });

  test("keeps phone tooltip inside viewport after delayed load and supports keyboard sorting/focus/mover retry", async ({
    page,
  }) => {
    const errors = collectErrors(page);
    const state = await mockConsole(page);
    const prevBase = state.calls[0];
    expect(prevBase).toBeDefined();
    if (prevBase) state.calls = [...state.calls, { ...prevBase, at: "2026-10-05T10:00:00Z" }];
    signedInAs(state);

    // First verify desktop keyboard sorting, group focus updating chartAria, and movers retry on 500.
    let failBreakdown = true;
    await page.route("**/api/console/v1/usage/statistics*", async (route) => {
      const u = new URL(route.request().url());
      if (u.searchParams.get("compare") !== "previous" && failBreakdown) {
        await route.fulfill({ status: 500, json: { error: { code: "internal", message: "boom" } } });
        return;
      }
      await route.fallback();
    });

    await page.goto(rangeUrl("2026-10-08T00:00:00Z", "2026-10-15T00:00:00Z", "&granularity=day"));
    await expect(page.getByText("上期分组变化加载失败")).toBeVisible({ timeout: 10_000 });
    failBreakdown = false;
    await page.locator('[data-a="retrymov"]').click();
    await expect(page.locator(".usd .usrw").first()).toBeVisible();

    const chartSvg = page.locator(".usch svg");
    const initialAria = (await chartSvg.getAttribute("aria-label")) ?? "";
    // Keyboard sort by requests column.
    const reqHeader = page.locator('.ust thead th[data-a="sort"][data-i="1"]');
    await reqHeader.focus();
    await page.keyboard.press("Enter");
    await expect(reqHeader).toHaveClass(/\bon\b/);
    // Keyboard focus first group button updates chart aria-label to that group's spend.
    await page.locator(".ust tbody .usnm").first().focus();
    await page.keyboard.press("Enter");
    const focusedAria = (await chartSvg.getAttribute("aria-label")) ?? "";
    expect(focusedAria).not.toBe(initialAria);

    // Now test 390px phone viewport with delayed first statistics response.
    await page.unroute("**/api/console/v1/usage/statistics*");
    await page.setViewportSize({ width: 390, height: 844 });
    let delayedOnce = false;
    await page.route("**/api/console/v1/usage/statistics*", async (route) => {
      if (!delayedOnce) {
        delayedOnce = true;
        await new Promise((r) => setTimeout(r, 150));
      }
      await route.fallback();
    });
    await page.goto(rangeUrl("2026-10-08T00:00:00Z", "2026-10-15T00:00:00Z", "&granularity=day"));
    await expect(chartSvg).toBeVisible();
    await chartSvg.focus();
    for (let i = 0; i < 7; i += 1) await page.keyboard.press("ArrowRight");
    const tip = page.locator(".ustip.on");
    await expect(tip).toBeVisible();
    const box = await tip.boundingBox();
    expect(box).not.toBeNull();
    expect((box?.x ?? -1) >= 0).toBe(true);
    expect((box?.x ?? 999) + (box?.width ?? 999)).toBeLessThanOrEqual(390);
    expect(errors.filter((e) => !/500/.test(e))).toEqual([]);
  });

  test("treats previous-only currency as empty state and disables CSV export", async ({ page }) => {
    const errors = collectErrors(page);
    const state = await mockConsole(page);
    signedInAs(state);

    // Current range has USD calls while previous only had CNY.
    await page.route("**/api/console/v1/usage/statistics*", async (route) => {
      const url = new URL(route.request().url());
      if (url.searchParams.get("compare") !== "previous") {
        await route.fallback();
        return;
      }
      await route.fulfill({
        status: 200,
        json: {
          totals: [{ spend: { amount: "8200000", currency: "USD" }, requests: "1", tokens: { input: "100" } }],
          previous: [{ spend: { amount: "500000000", currency: "CNY" }, requests: "2", tokens: { input: "200" } }],
          buckets: [
            {
              at: "2026-10-14T00:00:00Z",
              group: "claude-sonnet-4-6",
              metrics: { spend: { amount: "8200000", currency: "USD" }, requests: "1", tokens: { input: "100" } },
            },
          ],
          backfill_complete: true,
        },
      });
    });

    await page.goto(rangeUrl("2026-10-14T00:00:00Z", "2026-10-15T00:00:00Z", "&granularity=day"));
    const usdExport = page.getByRole("button", { name: "导出 CSV (USD)" });
    await expect(usdExport).toBeEnabled();

    await page.getByRole("group", { name: "币种" }).getByRole("button", { name: "CNY" }).click();
    await expect(page.getByText("这段时间没有调用记录")).toBeVisible();
    await expect(page.getByRole("button", { name: "导出 CSV (CNY)" })).toBeDisabled();

    await page.getByRole("group", { name: "币种" }).getByRole("button", { name: "USD" }).click();
    await expect(usdExport).toBeEnabled();
    expect(errors).toEqual([]);
  });
});
