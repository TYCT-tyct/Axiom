import { expect, type Page, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { apps } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// The lists of calls and of audit entries against the mocked API, each with a drawer over it: the list by cursor, the
// lines a request went through and its money only for the permission that inspects, the filters that keep the plugin, and
// exact audit filters whose actions read in words.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

async function open(page: Page, path: string) {
  const state = await mockAdmin(page, "usual");
  signedInAs(state, "mfa");
  await page.goto(path);
  return state;
}

const h1 = (page: Page, name: string | RegExp) => page.getByRole("heading", { level: 1, name });
const drawer = (page: Page) => page.getByRole("dialog");
// The row's first cell is the button that opens it, for the keyboard as well.
const opener = (page: Page, row = 0) => page.locator("tbody tr").nth(row).locator("td").first().getByRole("button");

test.describe("calls and audit", () => {
  test("the list pages, and a row opens the drawer with the lines the request went through and its money", async ({
    page,
  }) => {
    await open(page, "/calls");
    await expect(h1(page, words("shell.nav.calls"))).toBeVisible();
    await expect(page.getByRole("row")).toHaveCount(51);
    await page.getByRole("button", { name: "加载更多" }).click();
    await expect(page.getByRole("row")).toHaveCount(61);

    await opener(page).click();
    await expect(drawer(page).getByRole("heading", { name: zhText("calls.drawer.route") })).toBeVisible();
    await expect(drawer(page).getByText(zhText("calls.step.first"))).toBeVisible();
    await expect(drawer(page).getByText(zhText("calls.step.switch"))).toBeVisible();
    await expect(drawer(page).getByText(zhText("calls.outcome.committed")).first()).toBeVisible();
    await expect(drawer(page).getByRole("heading", { name: zhText("calls.drawer.fee") })).toBeVisible();
    await expect(drawer(page).getByText(zhText("calls.drawer.charged"))).toBeVisible();
    await expect(drawer(page).getByText(zhText("calls.drawer.paid"))).toBeVisible();

    // The drawer walks the list: the second row is next, and the first has no previous.
    await expect(drawer(page).getByText("1 / 60")).toBeVisible();
    await drawer(page)
      .getByRole("button", { name: zhText("drawerSteps.next") })
      .click();
    await expect(drawer(page).getByText("2 / 60")).toBeVisible();
  });

  test("four numbers sit over the list, from the dashboard's own request", async ({ page }) => {
    const state = await open(page, "/calls");
    const strip = page.getByRole("region", { name: zhText("calls.strip.label") });
    await expect(strip.getByText(zhText("calls.strip.requests"))).toBeVisible();
    await expect(strip.getByText(zhText("calls.strip.failRate"))).toBeVisible();
    await expect(strip.getByText(zhText("calls.strip.revenue"))).toBeVisible();
    await expect(page.getByRole("row")).toHaveCount(51);
    expect(state.log.some((entry) => entry.startsWith("GET /reports/overview"))).toBe(true);
  });

  test("a support session reads the summary and never asks for the trace or the dashboard", async ({ page }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");
    state.permissions = ["calls.search"];
    await page.goto("/calls");
    await expect(page.getByRole("row")).toHaveCount(51);
    await expect(page.getByText(state.calls[0]?.id ?? "")).toBeVisible();
    await expect(page.getByRole("region", { name: zhText("calls.strip.label") })).toHaveCount(0);
    await opener(page).click();
    await expect(drawer(page).getByText(zhText("calls.drawer.supportNote"))).toBeVisible();
    await expect(drawer(page).getByRole("heading", { name: zhText("calls.drawer.fee") })).toHaveCount(0);
    expect(state.log.some((entry) => entry.includes("/trace") || entry.startsWith("GET /reports/overview"))).toBe(
      false,
    );
  });

  test("the plugin filter keeps the calls that used the plugin, and the list says which", async ({ page }) => {
    await open(page, "/calls");
    await page.getByLabel(zhText("calls.filters.plugin"), { exact: true }).selectOption("file-parser");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    await expect(page).toHaveURL(/plugin=file-parser/);
    await expect(page.getByRole("row")).toHaveCount(6);
    await expect(page.locator("tbody tr").first()).toContainText(zhText("calls.plugin.file-parser"));
  });

  test("a call's ids keep both ends and offer a copy, the table scrolling inside its box", async ({ page }) => {
    // The narrowest window that has the request's column: a tablet leaves it to the call's drawer.
    await page.setViewportSize({ width: 1024, height: 900 });
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    const state = await open(page, "/calls");
    const first = state.calls[0];
    if (!first?.id || !first.account_id) {
      throw new Error("the usual call fixture is missing");
    }
    const row = page.getByRole("row").nth(1);
    await expect(row).toContainText(first.id.slice(-8));
    await expect(row.locator(`[title="${first.id}"]`)).toBeVisible();
    expect((await row.getByRole("cell").nth(1).boundingBox())?.height ?? 0).toBeLessThan(80);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(1024);

    await row
      .getByRole("button", { name: zhText("copy.id") })
      .first()
      .click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(first.id);

    await page.setViewportSize({ width: 1280, height: 900 });
    const account = row.getByRole("cell").nth(2);
    await expect(account.locator(`[title="${first.account_id}"]`)).toBeVisible();
    await account.getByRole("button", { name: zhText("copy.id") }).click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(first.account_id);
  });

  for (const mode of ["denied", "missing"] as const) {
    test(`a ${mode} clipboard says so visibly`, async ({ page }) => {
      await page.setViewportSize({ width: 1024, height: 900 });
      await page.addInitScript((value) => {
        Object.defineProperty(navigator, "clipboard", {
          configurable: true,
          value:
            value === "missing"
              ? undefined
              : { writeText: () => Promise.reject(new DOMException("denied", "NotAllowedError")) },
        });
      }, mode);
      await open(page, "/calls");
      await page
        .getByRole("button", { name: zhText("copy.id") })
        .first()
        .click();
      await expect(page.getByRole("status").filter({ hasText: zhText("copy.failed") })).toBeVisible();
    });
  }

  test("audit filters exactly, reads actions in words, and the drawer keeps the detail and the link to the call", async ({
    page,
  }) => {
    await open(page, "/audit");
    await expect(h1(page, words("shell.nav.audit"))).toBeVisible();
    await expect(page.getByRole("row")).toHaveCount(51);
    // An action the words do not know reads as a generic label with its own name in mono.
    const unknown = page.locator("tbody tr").filter({ hasText: "credit.grant" }).first();
    await expect(unknown).toContainText(zhText("audit.unknownEvent"));
    await page.getByLabel(zhText("audit.filters.action"), { exact: true }).fill("account.tier_change");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    await expect(page).toHaveURL(/action=account.tier_change/);
    await expect(page.getByRole("row")).toHaveCount(21);
    // A known action reads in words, not as its id.
    await expect(page.locator("tbody tr").first()).toContainText("调整账户等级");
    await expect(page.locator("tbody tr").first()).not.toContainText("account.tier_change");
    await opener(page).click();
    await expect(drawer(page).getByRole("heading", { name: "调整账户等级" })).toBeVisible();
    await expect(drawer(page).getByText("account.tier_change")).toBeVisible();
    await expect(drawer(page).getByText(/"before": "Standard"/)).toBeVisible();
    await drawer(page)
      .getByRole("link", { name: zhText("audit.openCalls") })
      .click();
    await expect(page).toHaveURL(/\/calls\?request_id=.*since=.*until=/);
    await expect(h1(page, words("shell.nav.calls"))).toBeVisible();
  });

  test("a long reason shows in full in a tip", async ({ page }) => {
    await page.setViewportSize({ width: 1600, height: 900 });
    await open(page, "/audit");
    const reason = page.locator("tbody tr").first().locator("td").nth(4);
    await reason.getByText(/Reviewed change/).focus();
    await expect(page.locator('[role="tooltip"]')).toContainText("Reviewed change");
  });

  // The document scrolls, not a box inside the frame, and the sidebar stays in view, as in the console.
  test("the sidebar stays in view on a page longer than the window", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await open(page, "/calls");
    await expect(page.getByRole("row").nth(10)).toBeVisible();
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(0);
    const nav = page.getByRole("navigation", { name: "主导航" });
    await expect(nav.getByRole("link", { name: zhText("shell.nav.calls") })).toBeInViewport();
  });
});
