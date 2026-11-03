import { expect, type Page, test } from "@playwright/test";
import { mockAdmin } from "./admin-api.ts";
import { apps } from "./apps.ts";
import { signedInAs } from "./mock-auth.ts";
import { words, zhText } from "./words.ts";

// The support list and the operator's trace and audit pages against the mocked API: the list pages by cursor, a trace
// link only for the permission that opens it, and exact audit filters that keep the recorded detail.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

async function open(page: Page, path: string) {
  const state = await mockAdmin(page, "usual");
  signedInAs(state, "mfa");
  await page.goto(path);
  return state;
}

const h1 = (page: Page, name: string | RegExp) => page.getByRole("heading", { level: 1, name });

test.describe("calls and audit", () => {
  test("the support list pages, and only an operator opens the trace with cost and attempts", async ({ page }) => {
    await open(page, "/calls");
    await expect(page.getByRole("row")).toHaveCount(51);
    await page.getByRole("button", { name: "加载更多" }).click();
    await expect(page.getByRole("row")).toHaveCount(61);
    await page
      .getByRole("link", { name: words("calls.trace") })
      .first()
      .click();
    await expect(h1(page, words("calls.traceTitle"))).toBeVisible();
    await expect(page.getByRole("heading", { name: zhText("calls.costTitle") })).toBeVisible();
    await expect(page.getByRole("table", { name: zhText("calls.attemptsTitle") })).toBeVisible();
    await expect(page.getByText(zhText("calls.outcome.committed")).first()).toBeVisible();
  });

  test("a support session sees no trace link", async ({ page }) => {
    const state = await mockAdmin(page);
    signedInAs(state, "mfa");
    state.permissions = ["calls.search"];
    await page.goto("/calls");
    await expect(page.getByRole("row")).toHaveCount(51);
    await expect(page.getByText(state.calls[0]?.id ?? "")).toBeVisible();
    await expect(page.getByRole("link", { name: words("calls.trace") })).toHaveCount(0);
  });

  test("a call's ids keep both ends and offer a copy, without the account column on a phone", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 900 });
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    const state = await open(page, "/calls");
    const first = state.calls[0];
    if (!first?.id || !first.account_id) {
      throw new Error("the usual call fixture is missing");
    }
    const row = page.getByRole("row").nth(1);
    await expect(row).toContainText(first.id.slice(-8));
    await expect(row.locator(`[title="${first.id}"]`)).toBeVisible();
    await expect(row.locator(`[title="${first.account_id}"]`)).not.toBeVisible();
    expect((await row.getByRole("cell").nth(1).boundingBox())?.height ?? 0).toBeLessThan(80);

    await row
      .getByRole("button", { name: zhText("copy.id") })
      .first()
      .click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(first.id);

    await page.setViewportSize({ width: 768, height: 900 });
    const account = row.getByRole("cell").nth(2);
    await expect(account.locator(`[title="${first.account_id}"]`)).toBeVisible();
    await account.getByRole("button", { name: zhText("copy.id") }).click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(first.account_id);
  });

  for (const mode of ["denied", "missing"] as const) {
    test(`a ${mode} clipboard says so visibly`, async ({ page }) => {
      await page.setViewportSize({ width: 360, height: 900 });
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

  test("audit filters exactly, shows the recorded detail, and links to the call only with calls.search", async ({
    page,
  }) => {
    await open(page, "/audit");
    await expect(page.getByRole("row")).toHaveCount(51);
    await page.getByLabel(zhText("audit.filters.action"), { exact: true }).fill("account.tier_change");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    await expect(page).toHaveURL(/action=account.tier_change/);
    await expect(page.getByRole("row")).toHaveCount(21);
    await page.locator("tbody tr").first().getByRole("link").click();
    await expect(h1(page, "account.tier_change")).toBeVisible();
    await expect(page.getByText(zhText("audit.detailTitle"))).toBeVisible();
    await expect(page.getByText(/"before": "Standard"/)).toBeVisible();
    await page.getByRole("link", { name: zhText("audit.openCalls") }).click();
    await expect(page).toHaveURL(/\/calls\?request_id=.*since=.*until=/);
    await expect(h1(page, words("nav.calls"))).toBeVisible();
  });

  test("the sidebar reaches the end of a page longer than the window", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await open(page, "/calls");
    await expect(page.getByRole("row").nth(10)).toBeVisible();
    const fill = await page.evaluate(() => {
      const nav = document.querySelector("nav");
      const links = nav?.firstElementChild;
      return {
        page: document.documentElement.scrollHeight,
        bottom: (nav?.getBoundingClientRect().bottom ?? 0) + window.scrollY,
        links: links?.getBoundingClientRect().height ?? 0,
        viewport: innerHeight,
      };
    });
    expect(fill.page).toBeGreaterThan(fill.viewport);
    expect(fill.bottom).toBeGreaterThanOrEqual(fill.page - 1);
    expect(fill.links).toBe(fill.viewport);
  });
});
