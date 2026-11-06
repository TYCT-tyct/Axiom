import { expect, test } from "@playwright/test";
import { uuid } from "./admin-data.ts";
import { approvalsOf } from "./admin-operations.ts";
import { close, dialog, open, send } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { words, zhText } from "./words.ts";

// The operations approvals against the mocked API (admin-operations.ts): the list and its state filter, what an
// approval's page shows of the caps it was weighed against, and what a decision says it did, read from the answer.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

const credit = `/approvals/${uuid(17, 1)}`;
const approvePath = `**/api/admin/v1/operations/approvals/${uuid(17, 1)}/approve`;

test.describe("operations approvals", () => {
  test("lists every approval, narrows by state from the address, and opens one", async ({ page }) => {
    await open(page, "/approvals");
    const list = page.getByRole("table", { name: zhText("approvals.list") });
    await expect(list.getByRole("row")).toHaveCount(9);
    // A target by its name before its ID, and a waiting one by the time it has left.
    const first = list.getByRole("row").nth(1);
    await expect(first).toContainText("Northwind");
    await expect(first.getByText(words("approvals.expiresIn_other"))).toBeVisible();
    await page.getByLabel(zhText("approvals.filters.state")).selectOption("pending");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    expect(new URL(page.url()).searchParams.get("state")).toBe("pending");
    await expect(list.getByRole("row")).toHaveCount(5);
    await list.getByRole("link", { name: zhText("approvals.risk.gift") }).click();
    await expect(page.getByRole("heading", { level: 1, name: zhText("approvals.risk.gift") })).toBeVisible();
  });

  test("an approved credit grant says it was given, and the page offers no second decision", async ({ page }) => {
    const state = await open(page, credit);
    const caps = page.getByRole("table", { name: zhText("approvals.cap.title") });
    await expect(page.locator("p", { hasText: zhText("approvals.cap.amount") })).toContainText("25.00");
    await expect(caps.getByRole("row").filter({ hasText: zhText("approvals.cap.daily") })).toContainText("500.00");
    await expect(page.getByText(approvalsOf(state)[0]?.agent_reason ?? "")).toBeVisible();
    // Why it waited: $25 is over the $10 autonomous cap for one operation, and the row says so in words.
    await expect(page.getByText(words("approvals.why.single"))).toContainText("10.00");
    await expect(caps.getByRole("row").filter({ hasText: zhText("approvals.cap.single") })).toContainText(
      zhText("approvals.cap.over"),
    );
    await expect(page.getByText("ops@northwind.example").first()).toBeVisible();
    // The raw data is there, closed.
    const raw = page.getByText(zhText("approvals.raw.title"));
    await expect(page.getByText(/"policy_rev": 3/)).toBeHidden();
    await raw.click();
    await expect(page.getByText(/"policy_rev": 3/)).toBeVisible();
    await page.getByRole("button", { name: zhText("approvals.approve") }).click();
    await expect(dialog(page)).toContainText("25.00");
    await send(page, "checked the incident and the account");
    await expect(dialog(page).getByText(zhText("approvals.done.gift"))).toBeVisible();
    expect(approvalsOf(state)[0]?.state).toBe("approved");
    expect(approvalsOf(state)[0]?.decision_reason).toBe("checked the incident and the account");
    await close(page);
    await expect(page.getByRole("button", { name: zhText("approvals.approve") })).toHaveCount(0);
    await expect(page.getByText(zhText("approvals.state.approved"), { exact: true })).toBeVisible();
  });

  // An answer whose operation did not run is no success: neither the generic "Done" nor a good tone.
  for (const [name, answer, outcome, text] of [
    [
      "whose target changed",
      { state: "approved", operation_state: "rejected", error_code: "unprocessable" },
      "refused",
      "approvals.refused",
    ],
    ["whose proposer is gone", { state: "canceled", operation_state: "canceled" }, "canceled", "approvals.canceled"],
  ] as const) {
    test(`an approval ${name} says the operation did not run`, async ({ page }) => {
      const state = await open(page, credit);
      await page.route(approvePath, (route) => route.fulfill({ json: { ...approvalsOf(state)[0], ...answer } }), {
        times: 1,
      });
      await page.getByRole("button", { name: zhText("approvals.approve") }).click();
      await send(page);
      await expect(dialog(page).getByText(words(text))).toBeVisible();
      await expect(dialog(page).getByText(zhText(`approvals.outcome.${outcome}`), { exact: true })).toBeVisible();
      await expect(dialog(page).getByText(zhText("change.done"), { exact: true })).toHaveCount(0);
    });
  }

  test("approving a retirement asks for the model's name, as retiring it on its page does", async ({ page }) => {
    const state = await open(page, `/approvals/${uuid(17, 4)}`);
    await page.getByRole("button", { name: zhText("approvals.approve") }).click();
    await send(page);
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("change.confirmMismatch"));
    expect(approvalsOf(state)[3]?.state).toBe("pending");
    await dialog(page)
      .getByLabel(/输入“vendor\/old-model”以确认/)
      .fill("vendor/old-model");
    await dialog(page).locator("button[type=submit]").click();
    await expect(dialog(page).getByText(zhText("approvals.done.delisting"))).toBeVisible();
    expect(approvalsOf(state)[3]?.state).toBe("approved");
  });

  test("on a phone each price is a block with its three values and their currency", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    await open(page, `/approvals/${uuid(17, 3)}`);
    const input = page.getByRole("listitem").filter({ hasText: zhText("models.item.input") });
    await expect(input).toContainText("4 USD");
    await expect(input).toContainText("4.5 USD");
    await expect(input.getByText(zhText("approvals.prices.cost"), { exact: true })).toBeVisible();
    await expect(page.getByRole("table", { name: words("approvals.prices.title") })).toBeHidden();
  });

  test("an approval decided elsewhere meanwhile is refused with why", async ({ page }) => {
    const state = await open(page, credit);
    await page.getByRole("button", { name: zhText("approvals.reject") }).click();
    const approval = approvalsOf(state)[0];
    if (approval) approval.state = "expired";
    await send(page);
    await expect(dialog(page).getByText(zhText("approvals.conflict"))).toBeVisible();
  });

  test("a sale price shows its prices and envelope, and warns of what the envelope could not check", async ({
    page,
  }) => {
    await open(page, `/approvals/${uuid(17, 3)}`);
    await expect(page.getByText("Claude Opus 5.5").first()).toBeVisible();
    const prices = page.getByRole("table", { name: words("approvals.prices.title") });
    // Input moves from 4 to 4.5 per million tokens; cache reads keep their price, so they are not listed.
    await expect(prices.getByRole("row").filter({ hasText: zhText("models.item.input") })).toContainText(/4\D.*4\.5/);
    await expect(prices.getByRole("row").filter({ hasText: zhText("models.item.cache_read") })).toHaveCount(0);
    await expect(page.getByText(words("approvals.why.envelope.unknownCost"))).toBeVisible();
    const table = page.getByRole("table", { name: zhText("approvals.envelope.title") });
    await expect(table.getByRole("row").filter({ hasText: zhText("approvals.envelope.change") })).toContainText(
      "12.5%",
    );
    const margin = table.getByRole("row").filter({ hasText: zhText("approvals.envelope.margin") });
    await expect(margin).toContainText(zhText("approvals.envelope.unknownCost"));
    await expect(margin).toContainText(zhText("approvals.envelope.flag.unknownCost"));
  });
});
