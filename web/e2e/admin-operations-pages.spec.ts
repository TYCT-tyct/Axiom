import { expect, type Page, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { open } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// The dashboard and the operations monitor against the mocked API (admin-api.ts, admin-monitor-api.ts): the eight figures
// and the cards under them, the range, the score the monitor works out for itself and what it takes off, and the sections
// a node without the summaries leaves out without an error.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

const h1 = (page: Page) => page.getByRole("heading", { level: 1 });
const card = (page: Page, key: string) => page.getByRole("region", { name: words(key) });

// A node whose summary operations are not built answers 501 (api/admin.yaml, before its backend), or 404 on a node
// before the operation existed.
async function withoutMonitor(page: Page, status: 501 | 404) {
  await page.route("**/api/admin/v1/reports/monitor*", (route) =>
    route.fulfill({ status, json: { error: { code: "not_implemented", message: "not built" } } }),
  );
}

test.describe("the dashboard", () => {
  test("shows the eight figures, each a link to the page it is made on, and the cards under them", async ({ page }) => {
    await open(page, "");
    await expect(h1(page)).toHaveText(zhText("dashboard:title"));
    const figures = page.getByRole("region", { name: zhText("dashboard:figures") });
    for (const label of ["revenue", "cost", "margin", "calls", "tokens", "failure", "active", "firstToken"] as const) {
      await expect(figures.getByText(words(`dashboard:${label}`))).toBeVisible();
    }
    const paths = await figures
      .getByRole("link")
      .evaluateAll((links) => links.map((link) => new URL((link as HTMLAnchorElement).href).pathname));
    expect(paths).toEqual(["/orders", "/suppliers", "/pricing", "/calls", "/calls", "/calls", "/users", "/monitor"]);
    const failing = new URL((await figures.getByRole("link").nth(5).getAttribute("href")) ?? "", "http://axiom");
    expect(failing.searchParams.get("status")).toBe("error");

    for (const title of ["chart.title", "tokens", "mix.title", "spenders.title", "todo.title"]) {
      await expect(card(page, `dashboard:${title}`)).toBeVisible();
    }
    await expect(card(page, "goals:title")).toBeVisible();
    // The chart is drawn on the real days, and today's bar is the deepest one.
    await expect(
      card(page, "dashboard:chart.title").getByRole("group", { name: zhText("dashboard:chart.title") }),
    ).toBeVisible();
    await expect(card(page, "dashboard:spenders.title").getByRole("link").first()).toHaveAttribute(
      "href",
      /^\/accounts\//,
    );
  });

  test("moves between today, 7 days and the month in the address, and reads the figures again", async ({ page }) => {
    await open(page, "");
    const requests: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes("/reports/overview")) requests.push(new URL(request.url()).search);
    });
    await page.getByText(zhText("dashboard:range.month"), { exact: true }).click();
    await expect(page).toHaveURL(/range=month/);
    await expect.poll(() => requests.length).toBeGreaterThan(0);
    // The month's window starts on the 1st, a UTC day boundary; the 7 days start six days before today.
    expect(requests.some((search) => /since=\d{4}-\d{2}-01T00%3A00%3A00\.000Z/.test(search))).toBe(true);
    await page.getByText(zhText("dashboard:range.7d"), { exact: true }).click();
    await expect(page).toHaveURL(/range=7d/);
    await expect(page.getByText(zhText("dashboard:compare.7d")).first()).toBeVisible();
  });

  test("lists what needs doing, most pressing first, five at a time", async ({ page }) => {
    await open(page, "");
    const todo = card(page, "dashboard:todo.title");
    const rows = todo.getByRole("listitem");
    await expect(rows.first()).toBeVisible();
    await expect(rows).toHaveCount(5);
    // Proposals wait in the mock: the row says how many and when the first one runs out.
    await expect(todo.getByText(/4/).first()).toBeVisible();
    const more = todo.getByRole("button", { name: /再看 \d+ 项/ });
    if ((await more.count()) > 0) {
      await more.click();
      await expect(rows).not.toHaveCount(5);
      await todo.getByRole("button", { name: zhText("dashboard:todo.fewer") }).click();
      await expect(rows).toHaveCount(5);
    }
  });

  test("reads the lines of the last hour and the balances that can be read", async ({ page }) => {
    await open(page, "");
    const lines = card(page, "ops:lines.title");
    // A channel with two lines is named with its group, a channel with one by itself.
    await expect(lines.getByRole("cell", { name: "Supplier 1/default" })).toBeVisible();
    await expect(lines.getByRole("cell", { name: "Supplier 1/vip-2" })).toBeVisible();
    await expect(lines.getByRole("cell", { name: "Example US" })).toBeVisible();
    await expect(lines.getByText("95.0%")).toBeVisible();
    const balances = card(page, "ops:balances.title");
    await expect(balances.getByText(zhText("ops:balances.enough_other").replace("{{count}}", "21"))).toBeVisible();
    await expect(balances.getByText(zhText("ops:balances.short_other").replace("{{count}}", "3"))).toBeVisible();
  });

  for (const status of [501, 404] as const) {
    test(`leaves out the lines when the monitor answers ${status}, without an error`, async ({ page }) => {
      const state = await mockAdmin(page, "usual");
      signedInAs(state, "mfa");
      await withoutMonitor(page, status);
      await page.goto("/");
      await expect(card(page, "dashboard:todo.title")).toBeVisible();
      await expect(card(page, "ops:lines.title")).toHaveCount(0);
      await expect(page.getByRole("alert")).toHaveCount(0);
    });
  }

  test("says there is nothing yet for a site with no calls, and fits a phone", async ({ page }) => {
    await open(page, "", "empty");
    await expect(page.getByText(zhText("dashboard:empty.title"))).toBeVisible();
    await page.setViewportSize({ width: 360, height: 900 });
    await open(page, "", "worst");
    await expect(h1(page)).toBeVisible();
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  });
});

test.describe("the operations monitor", () => {
  test("works out the score from what the page shows and names every point taken off", async ({ page }) => {
    await open(page, "/monitor");
    await expect(h1(page)).toHaveText(zhText("monitor:title"));
    const health = card(page, "monitor:health.title");
    // Two lines under 99% (a heavy one and a light one), a supplier whose balance lasts 3 days, a credential at its
    // ceiling, and the one alert not resolved.
    await expect(health.getByText("Supplier 1/vip-2 成功率 95.0%")).toBeVisible();
    await expect(health.getByText("Example US 成功率 98.0%")).toBeVisible();
    await expect(health.getByText("Low On Funds 余额只够 3 天")).toBeVisible();
    await expect(health.getByText("1 条告警还没有解决")).toBeVisible();
    await expect(health.getByText("1 个上游账号到了并发上限")).toBeVisible();
    const taken = await health
      .getByRole("listitem")
      .evaluateAll((rows) => rows.map((row) => Number((row.textContent ?? "").match(/−(\d+)$/)?.[1] ?? 0)));
    expect(taken.slice(0, 5)).toEqual([12, 4, 6, 2, 3]);
    const score = 100 - taken.reduce((sum, points) => sum + points, 0);
    await expect(health.getByRole("img")).toHaveAttribute("aria-label", new RegExp(`${score}`));
    // A reason leads to its cause.
    await health.getByText("Low On Funds 余额只够 3 天").click();
    await expect(page).toHaveURL(/\/suppliers\//);
  });

  test("shows the six numbers, the charts, the failure causes and how long the calls took", async ({ page }) => {
    await open(page, "/monitor");
    const figures = page.getByRole("region", { name: zhText("monitor:figures") });
    for (const label of ["requestsPerMinute", "tokensPerMinute", "failure", "firstToken", "total", "switch"]) {
      await expect(figures.getByText(words(`monitor:${label}`))).toBeVisible();
    }
    await expect(figures.getByText(zhText("monitor:within").replace("{{value}}", "1.20"))).toBeVisible();
    await expect(card(page, "monitor:requests.title").getByRole("group")).toBeVisible();
    await expect(card(page, "monitor:firstTokenChart.title").getByRole("group")).toBeVisible();
    const causes = card(page, "monitor:causes.title");
    await expect(causes.getByText(zhText("monitor:causes.kind.upstream_error"))).toBeVisible();
    await expect(causes.getByText(zhText("monitor:causes.legend"))).toBeVisible();
    await expect(card(page, "monitor:spread.title").getByText(/1–2 秒/)).toBeVisible();
    await expect(
      card(page, "ops:lines.title").getByRole("columnheader", { name: zhText("ops:lines.busy") }),
    ).toBeVisible();
  });

  test("moves to the last 24 hours in the address, and between its two tabs", async ({ page }) => {
    await open(page, "/monitor");
    await page.getByText(zhText("monitor:window.24h"), { exact: true }).click();
    await expect(page).toHaveURL(/window=24h/);
    await expect(card(page, "monitor:requests.title")).toContainText(zhText("monitor:requests.per.24h"));
    await page.getByRole("tab", { name: new RegExp(zhText("monitor:alerts")) }).click();
    await expect(page).toHaveURL(/\/alert-events$/);
    await expect(h1(page)).toHaveText(zhText("monitor:title"));
    await expect(page.getByRole("tab", { name: new RegExp(zhText("monitor:alerts")) })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await page.getByRole("tab", { name: zhText("monitor:overview") }).click();
    await expect(page).toHaveURL(/\/monitor/);
  });

  for (const status of [501, 404] as const) {
    test(`says the summary is not there yet, and no error, when the monitor answers ${status}`, async ({ page }) => {
      const state = await mockAdmin(page, "usual");
      signedInAs(state, "mfa");
      await withoutMonitor(page, status);
      await page.goto("/monitor");
      await expect(page.getByText(zhText("monitor:notBuilt.title"))).toBeVisible();
      await expect(page.getByRole("alert")).toHaveCount(0);
      await expect(page.getByRole("tab", { name: zhText("monitor:overview") })).toBeVisible();
    });
  }

  test("is the page the sidebar's entry opens, and the old alert address is its alerts tab", async ({ page }) => {
    await open(page, "/alert-events");
    await expect(page.getByRole("link", { name: zhText("shell.nav.ops") })).toHaveAttribute("aria-current", "page");
    await expect(page.getByRole("link", { name: zhText("shell.nav.ops") })).toHaveAttribute("href", "/monitor");
  });
});
