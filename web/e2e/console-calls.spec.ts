import { expect, type Page, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";

// The console's call list, summary, sessions tab, export limit and detail pages against the mocked API.

test.use({ timezoneId: "Asia/Shanghai" });

const main = (page: Page) => page.getByRole("main");
const callRows = (page: Page) => page.getByRole("region", { name: "调用记录" }).locator('.lgh, [data-a="lrow"]');
const summaryRows = (page: Page) => page.getByRole("region", { name: "调用汇总" }).locator('.lgh, [data-a="agg"]');

const firstCall = (state: Awaited<ReturnType<typeof mockConsole>>) => {
  const call = state.calls[0];
  if (!call) throw new Error("expected mock call");
  return call;
};

const firstKey = (state: Awaited<ReturnType<typeof mockConsole>>) => {
  const key = state.keys[0];
  if (!key) throw new Error("expected mock key");
  return key;
};

test.describe("calls", () => {
  test("the list pages by cursor, filters live in the address, and a call opens and copies IDs", async ({
    page,
    context,
  }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
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

    await page.getByRole("button", { name: "复制请求 ID" }).click();
    await expect(page.getByRole("status").filter({ hasText: "已复制" })).toBeVisible();

    await page.getByRole("link", { name: "返回列表" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "调用记录" })).toBeVisible();
    await expect(page).toHaveURL(/model=openai%2Fgpt-5/);
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(200);
  });

  test("inline drawer shows retry on GET failure, updates URL rules, supports keyboard, and drops O8/O24", async ({
    page,
    context,
  }) => {
    const state = await mockConsole(page);
    signedInAs(state);

    let detailFails = true;
    await page.route("**/api/console/v1/calls/00000000-0000-4000-8000-*", async (route) => {
      if (detailFails) {
        await route.fulfill({ status: 500, json: { error: { code: "internal_error", message: "boom" } } });
        return;
      }
      await route.fallback();
    });

    await page.goto("/console/calls?fell_back=true");
    const firstDataRow = page.getByRole("region", { name: "调用记录" }).locator('[data-a="lrow"]').first();
    const detailHref = await firstDataRow.locator("a.lgt").getAttribute("href");
    expect(detailHref).toMatch(/^\/console\/calls\/00000000-0000-4000-8000-/);
    await firstDataRow.locator("button.lgm").press("Enter");
    await expect(page).toHaveURL(/call=00000000-0000-4000-8000-/);
    await expect(page.getByText("无分段尝试明细")).toHaveCount(0);
    await expect(page.getByRole("alert").filter({ hasText: "没有加载出来" })).toBeVisible();

    detailFails = false;
    await page.getByRole("button", { name: "重试" }).click();
    await expect(page.getByText("尝试与路由")).toBeVisible();
    await expect(page.getByText(/upstream_overloaded/)).toBeVisible();
    await expect(page.getByLabel("关闭")).toBeVisible();
    await expect(page.getByRole("button", { name: "慢于 5 秒" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "复制 cURL" })).toHaveCount(0);
    await expect(page.locator(".lglive")).toHaveCount(0);

    // Changing group_by keeps drawer open; changing status filter closes stale drawer selection.
    await page.getByRole("button", { name: "按模型", exact: true }).click();
    await expect(page).toHaveURL(/group_by=model/);
    await expect(page).toHaveURL(/call=00000000-0000-4000-8000-/);

    await page.getByRole("button", { name: "成功", exact: true }).click();
    await expect(page).not.toHaveURL(/call=/);

    // Modifier-click on native link opens a new tab instead of navigating the current tab via onOpenCall.
    const popupPromise = context.waitForEvent("page");
    await firstDataRow.locator("a.lgt").click({ modifiers: ["ControlOrMeta"] });
    const popup = await popupPromise;
    await expect(popup).toHaveURL(/\/console\/calls\/00000000-0000-4000-8000-/);
    await popup.close();
    await expect(page).toHaveURL(/\/console\/calls\?/);
  });

  test("summary shows window and loaded metrics, and export classifies limits vs invalid params", async ({ page }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls");
    await expect(page.getByText(/汇总窗口：/)).toBeVisible();
    await expect(page.getByText("已加载失败")).toBeVisible();
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
    const limitAlert = page.getByRole("alert").filter({ hasText: "导出时间范围超出上限" });
    await expect(limitAlert).toBeVisible();
    await expect(limitAlert).toContainText("31 天");
    await page.getByRole("button", { name: "设为近 7 天" }).click();
    await expect(page).toHaveURL(/preset=d7/);
    await expect(limitAlert).toHaveCount(0);

    await page.goto("/console/calls?request_id=not-a-uuid");
    await page.getByRole("button", { name: "导出 CSV" }).click();
    const invalidAlert = page.getByRole("alert").filter({ hasText: "导出筛选参数无效" });
    await expect(invalidAlert).toBeVisible();
    await expect(page.getByRole("button", { name: "设为近 7 天" })).toHaveCount(0);

    await page.goto("/console/calls?tab=sessions&fell_back=true");
    await expect(page.getByText("CSV 导出与汇总忽略「仅换过线路」过滤")).toBeVisible();
  });

  test("sessions tab pages with server bounds, groups by session+currency, skips aggregate, and stays flat", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    signedInAs(state);

    // Multi-currency + tie-break session check.
    const t0 = Date.UTC(2026, 9, 14, 9, 0, 0);
    const seed = firstCall(state);
    state.calls.unshift(
      {
        ...seed,
        id: "00000000-0000-4000-8000-999999999001",
        session_id: "session-tie",
        at: new Date(t0 + 20_000).toISOString(),
        model: "openai/gpt-5",
        client_app: "Cursor",
        price: { amount: "500000000", currency: "USD" },
      },
      {
        ...seed,
        id: "00000000-0000-4000-8000-999999999002",
        session_id: "session-tie",
        at: new Date(t0 + 10_000).toISOString(),
        model: "anthropic/claude-sonnet-4",
        client_app: "Codex",
        price: { amount: "500000000", currency: "USD" },
      },
      {
        ...seed,
        id: "00000000-0000-4000-8000-999999999003",
        session_id: "session-tie",
        at: new Date(t0 + 5_000).toISOString(),
        model: "deepseek/deepseek-v3",
        client_app: "Claude Code",
        price: { amount: "1000000000", currency: "CNY" },
      },
    );

    let aggregateRequests = 0;
    page.on("request", (req) => {
      if (req.url().includes("/api/console/v1/calls/aggregate")) aggregateRequests += 1;
    });

    await page.goto("/console/calls?tab=sessions");
    const sessionRows = page.getByRole("region", { name: "按会话调用" }).locator('[data-a="session"]');
    await expect(sessionRows).toHaveCount(50);
    expect(aggregateRequests).toBe(0);

    // Same session with two currencies yields two distinct rows; USD row picks newest client (Cursor) and lexicographically smaller model tie-breaker (anthropic/claude-sonnet-4).
    const tieRows = sessionRows.filter({ hasText: "session-tie" });
    await expect(tieRows).toHaveCount(2);
    await expect(tieRows.first()).toContainText("anthropic/claude-sonnet-4");
    await expect(tieRows.first()).toContainText("Cursor");

    // Clicking a session row does not mis-drilldown to client_app.
    await sessionRows.first().click();
    await expect(page).toHaveURL(/tab=sessions/);
    await expect(page).not.toHaveURL(/client_app=/);

    const nextReq = page.waitForRequest((req) => req.url().includes("/api/console/v1/calls/sessions"));
    await page.getByRole("button", { name: "加载更多" }).click();
    const nextUrl = new URL((await nextReq).url());
    expect(nextUrl.searchParams.get("cursor")).toBeTruthy();
    expect(nextUrl.searchParams.get("since")).toBeTruthy();
    expect(nextUrl.searchParams.get("until")).toBeTruthy();
    await expect(sessionRows).toHaveCount(62);

    await page.route("**/calls/sessions*", (route) =>
      route.fulfill({ status: 404, json: { error: { code: "not_found" } } }),
    );
    await page.goto("/console/calls?tab=sessions");
    await expect(main(page).getByRole("status").first()).toContainText("按会话查看");
  });

  test("detail uses corrected charge_details in 3 columns and matches failed, cached, free, session calls", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    signedInAs(state);

    // Push a call where immutable call.price (CNY 1) was adjusted via charge_details.charged (CNY 0.5).
    state.calls.push({
      ...firstCall(state),
      id: "00000000-0000-4000-8000-999999999099",
      price: { amount: "1000000000", currency: "CNY" },
      free: false,
      cache_hit: false,
      error_kind: "",
      finish_reason: "stop",
      charge_details: {
        currency: "CNY",
        subtotal: "0.6",
        balance_adjustment: "-0.1",
        charged: "0.5",
        items: [{ item: "input", quantity: 600, unit: "token", unit_price: "0.001", amount: "0.6" }],
      },
    });
    await page.goto("/console/calls/00000000-0000-4000-8000-999999999099");
    await expect(page.getByRole("heading", { name: "怎么计费" })).toBeVisible();
    await expect(page.getByText("600 × 0.001 / token")).toBeVisible();
    await expect(page.getByText("0.50").first()).toBeVisible();

    await page.goto("/console/calls/00000000-0000-4000-8000-000000000007");
    await expect(page.getByText("upstream_timeout").first()).toBeVisible();
    await expect(main(page).getByText("失败", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("未扣减可用余额")).toHaveCount(0);

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

  test("calls.view-only user can filter by key_id/workspace_id, Key chip paginates, and account switch isolates back state", async ({
    page,
  }) => {
    const state = await mockConsole(page);
    signedInAs(state);
    const baseKey = firstKey(state);
    for (let i = 10; i < 65; i += 1) {
      state.keys.push({
        ...baseKey,
        id: `key_${String(i).padStart(26, "0")}` as (typeof state.keys)[number]["id"],
        name: `Key ${i}`,
      });
    }
    await page.goto("/console/calls");
    await page.getByRole("button", { name: "全部 Key" }).click();
    await page.getByRole("button", { name: "加载更多 Key..." }).click();
    await expect(page.getByRole("button", { name: "Key 10" })).toBeVisible();
    await page.keyboard.press("Escape");

    // Account A saves search on detail nav, then signs out in the same tab; Account B returns without A's filters.
    await page.getByLabel("模型").fill("openai/gpt-5");
    await page.getByRole("button", { name: "筛选", exact: true }).click();
    await expect(page).toHaveURL(/model=openai%2Fgpt-5/);
    await callRows(page).nth(1).getByRole("link").click();
    await expect(page.getByRole("heading", { level: 1, name: "调用详情" })).toBeVisible();

    state.level = "none";
    await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange", { bubbles: true })));
    await expect(page.getByRole("button", { name: "登录" }).first()).toBeVisible();
    state.account = 2;
    state.user.email = "user-b@example.com";
    signedInAs(state);
    await page.goto("/console");
    await page.goto("/console/calls/00000000-0000-4000-8000-000000000007");
    await page.getByRole("link", { name: "返回列表" }).click();
    await expect(page).not.toHaveURL(/model=openai%2Fgpt-5/);

    // Month preset persists explicit preset choice in URL across chip changes.
    await page.getByRole("button", { name: "本月", exact: true }).click();
    await expect(page).toHaveURL(/preset=month/);
    await page.getByRole("button", { name: "全部模型" }).click();
    await page.getByRole("button", { name: "openai/gpt-5", exact: true }).click();
    await expect(page).toHaveURL(/preset=month/);

    // calls.view without api_keys.view avoids /keys 403 error and exposes exact ID inputs.
    state.permissions = ["calls.view", "balance.view"];
    let keysCalls = 0;
    page.on("request", (req) => {
      if (req.url().includes("/api/console/v1/keys")) keysCalls += 1;
    });
    await page.goto("/console/calls");
    await expect(page.getByRole("alert")).toHaveCount(0);
    await page.getByRole("button", { name: "更多筛选" }).click();
    await expect(page.getByLabel("Key ID")).toBeVisible();
    await expect(page.getByLabel("工作区 ID")).toBeVisible();
    await page.getByLabel("Key ID").fill("key_00000000000000000000000001");
    await page.getByRole("button", { name: "筛选", exact: true }).click();
    await expect(page).toHaveURL(/key_id=key_00000000000000000000000001/);
    expect(keysCalls).toBe(0);
  });

  test("390px mobile layout keeps summary buckets, sessions labels, and drawer usable", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    const state = await mockConsole(page);
    signedInAs(state);
    await page.goto("/console/calls?group_by=model");
    const sum = summaryRows(page);
    await expect(sum.nth(1)).toContainText("anthropic/claude-sonnet-4");
    await expect(sum.nth(1)).toContainText("次");

    await page.getByRole("button", { name: "按会话", exact: true }).click();
    const sRow = page.getByRole("region", { name: "按会话调用" }).locator('[data-a="session"]').first();
    await expect(sRow).toContainText("session-");
    await expect(sRow).toContainText("次");

    await page.getByRole("button", { name: "请求明细", exact: true }).click();
    await page.getByRole("region", { name: "调用记录" }).locator('[data-a="lrow"] button.lgm').first().click();
    await expect(page).toHaveURL(/call=/);
    await expect(page.getByText("尝试与路由")).toBeVisible();
  });

  test("no calls, no matches, permission denied, free mark, and overview", async ({ page }) => {
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
    await page.unroute("**/api/console/v1/calls?*");

    const usual = await mockConsole(page);
    signedInAs(usual);
    await page.goto("/console/calls");
    await expect(
      page.getByRole("region", { name: "调用记录" }).locator('[data-a="lrow"]').filter({ hasText: /免单/ }).first(),
    ).toBeVisible();
    await page.getByRole("link", { name: "概览" }).click();
    await expect(page.getByRole("heading", { name: "最近的报错" })).toBeVisible();
  });
});
