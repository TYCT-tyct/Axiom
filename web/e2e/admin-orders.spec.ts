import { expect, test } from "@playwright/test";
import { close, dialog, done, open, send } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { userId } from "./mock-auth.ts";
import { zhText } from "./words.ts";

// The orders page and its refund queue against the mocked API: what the filters write into the address, what the table
// shows for an order, and who may decide a refund.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

test.describe("orders and refunds", () => {
  test("orders are filtered by state from the address, and the form writes the address", async ({ page }) => {
    await open(page, "/orders");
    const orders = page.getByRole("table", { name: zhText("nav.orders") });
    await expect(orders.getByRole("row")).toHaveCount(51);
    await page.getByLabel(zhText("orders.filters.state")).selectOption("pending");
    await page.getByRole("button", { name: zhText("filter.apply") }).click();
    expect(new URL(page.url()).searchParams.get("state")).toBe("pending");
    // Twelve of the sixty usual orders wait for payment.
    await expect(orders.getByRole("row")).toHaveCount(13);
    await page.getByRole("button", { name: zhText("filter.reset") }).click();
    await expect(orders.getByRole("row")).toHaveCount(51);
  });

  test("a refund is approved by another person with a reason and leaves the queue", async ({ page }) => {
    const state = await open(page, "/orders");
    const queue = page.getByRole("table", { name: zhText("orders.refund.title") });
    await expect(queue.getByRole("row")).toHaveCount(4);
    await queue
      .getByRole("button", { name: zhText("orders.refund.approve") })
      .first()
      .click();
    await dialog(page).locator("button[type=submit]").click();
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("change.reasonMissing"));
    await send(page, "checked the receipt");
    await expect(done(page)).toBeVisible();
    expect(state.refunds[0]?.state).toBe("succeeded");
    expect(state.refunds[0]?.approved_by).toBeDefined();
    await close(page);
    await expect(queue.getByRole("row")).toHaveCount(3);
  });

  test("shows what the channel took, not the quote, and says when an order is unpaid", async ({ page }) => {
    const state = await open(page, "/orders");
    const paid = state.orders[0];
    const unpaid = state.orders[1];
    const missing = state.orders[2];
    if (!paid || !unpaid || !missing) throw new Error("the usual orders are missing");
    paid.state = "paid";
    paid.total = { amount: "1000", currency: "USD" };
    paid.paid_total = { amount: "800", currency: "USD" };
    paid.paid_fee = { amount: "24", currency: "USD" };
    paid.credits = { amount: "7000000000", currency: "USD" };
    paid.credited = { amount: "6000000000", currency: "USD" };
    unpaid.state = "pending";
    delete unpaid.paid_total;
    delete unpaid.paid_fee;
    delete unpaid.credited;
    unpaid.total = { amount: "1000", currency: "USD" };
    unpaid.credits = { amount: "7000000000", currency: "USD" };
    missing.state = "paid";
    missing.total = { amount: "1000", currency: "USD" };
    missing.paid_total = { amount: "1000", currency: "USD" };
    delete missing.credited;
    missing.credits = { amount: "7000000000", currency: "USD" };
    await page.reload();

    const orders = page.getByRole("table", { name: zhText("nav.orders") });
    const paidRow = orders.getByRole("row").filter({ hasText: paid.id?.slice(-8) ?? "" });
    await expect(paidRow.getByText("US$8.00")).toBeVisible();
    await expect(paidRow.getByText("US$10.00")).toBeVisible();
    await expect(paidRow.getByText("US$0.24")).toBeVisible();
    const unpaidRow = orders.getByRole("row").filter({ hasText: unpaid.id?.slice(-8) ?? "" });
    await expect(unpaidRow.getByText(zhText("orders.notPaid"))).toBeVisible();
    await expect(unpaidRow.getByText("US$10.00")).toBeVisible();
    const missingRow = orders.getByRole("row").filter({ hasText: missing.id?.slice(-8) ?? "" });
    await expect(missingRow.getByText(zhText("orders.creditedUnavailable"))).toBeVisible();
    await expect(missingRow.getByText(zhText("orders.notCredited"))).toHaveCount(0);
  });

  test("the narrow-screen confirmation shows the refund facts", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    const state = await open(page, "/orders");
    const refund = state.refunds[0];
    if (!refund) throw new Error("the usual refunds are missing");
    const queue = page.getByRole("table", { name: zhText("orders.refund.title") });
    await queue
      .getByRole("button", { name: zhText("orders.refund.approve") })
      .first()
      .click();
    await expect(dialog(page).getByText(zhText("orders.refund.review"))).toBeVisible();
    await expect(dialog(page).getByText(refund.account_id ?? "")).toBeVisible();
    await expect(dialog(page).getByText(refund.reason ?? "")).toBeVisible();
    await expect(dialog(page).getByText("US$5.00").first()).toBeVisible();
    await expect(dialog(page).getByText(refund.requested_by ?? "")).toBeVisible();
  });

  test("a person who asked for a refund does not approve it themselves", async ({ page }) => {
    const state = await open(page, "/orders");
    if (state.refunds[0]) {
      state.refunds[0].requested_by = userId(0);
    }
    await page.reload();
    const queue = page.getByRole("table", { name: zhText("orders.refund.title") });
    await expect(queue.getByText(zhText("orders.refund.secondPerson")).first()).toBeVisible();
    // The other two refunds still offer an approval.
    await expect(queue.getByRole("button", { name: zhText("orders.refund.approve") })).toHaveCount(2);
  });
});
