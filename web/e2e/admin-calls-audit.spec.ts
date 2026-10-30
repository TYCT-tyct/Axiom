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
    await expect(page.getByText(zhText("calls.costTitle"))).toBeVisible();
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

  test("audit filters exactly, shows the recorded detail, and links to the call only with calls.search", async ({
    page,
  }) => {
    await open(page, "/audit");
    await expect(page.getByRole("row")).toHaveCount(51);
    await page.getByLabel(zhText("audit.filters.action")).fill("account.tier_change");
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
});
