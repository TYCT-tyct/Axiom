import { expect, test } from "@playwright/test";
import { close, dialog, done, open, send } from "./admin-page.ts";
import { apps } from "./apps.ts";
import { userId } from "./mock-auth.ts";
import { zhText } from "./words.ts";

// The orders page and its refund queue against the mocked API: what the filters write into the address, what the table
// shows for an order, and who may decide a refund.

test.use({ baseURL: apps[2].url, timezoneId: "Asia/Shanghai" });

test.describe("orders and refunds", () => {
  test("orders are filtered by state from the address, and the filter writes the address", async ({ page }) => {
    await open(page, "/orders");
    const orders = page.getByRole("table", { name: zhText("orders.tabs.orders") });
    await expect(orders.getByRole("row")).toHaveCount(51);
    await page.getByLabel(zhText("orders.filters.state")).selectOption("pending");
    await expect.poll(() => new URL(page.url()).searchParams.get("state")).toBe("pending");
    // Twelve of the sixty usual orders wait for payment.
    await expect(orders.getByRole("row")).toHaveCount(13);
    await page.getByLabel(zhText("orders.filters.state")).selectOption("all");
    await expect(orders.getByRole("row")).toHaveCount(51);
  });

  test("the old addresses of the ledgers and the differences lead to their tabs", async ({ page }) => {
    await open(page, "/orders?cash=1&since=2026-11-01T00%3A00%3A00.000Z");
    await expect.poll(() => new URL(page.url()).pathname).toBe("/orders/cash");
    expect(new URL(page.url()).searchParams.get("since")).toBe("2026-11-01T00:00:00.000Z");
    await page.goto("/orders?paid=batches");
    await expect.poll(() => new URL(page.url()).pathname).toBe("/orders/batches");
    await page.goto("/payment-discrepancies?state=open");
    await expect.poll(() => new URL(page.url()).pathname).toBe("/orders/discrepancies");
    expect(new URL(page.url()).searchParams.get("state")).toBe("open");
  });

  test("a refund is approved by another person with a reason, from its drawer", async ({ page }) => {
    const state = await open(page, "/orders/refunds");
    const queue = page.getByRole("table", { name: zhText("orders.refund.title") });
    await expect(queue.getByRole("row")).toHaveCount(4);
    await queue.getByRole("row").nth(1).click();
    await page.getByRole("button", { name: zhText("orders.refund.approve"), exact: true }).click();
    await dialog(page).locator("button[type=submit]").click();
    await expect(dialog(page).getByRole("alert")).toHaveText(zhText("change.reasonMissing"));
    await send(page, "checked the receipt");
    await expect(done(page)).toBeVisible();
    expect(state.refunds[0]?.state).toBe("succeeded");
    expect(state.refunds[0]?.approved_by).toBeDefined();
    await close(page);
    await expect(queue.getByText(zhText("orders.refund.state.succeeded"))).toHaveCount(1);
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

    const orders = page.getByRole("table", { name: zhText("orders.tabs.orders") });
    const rowOf = (id: string | undefined) => {
      if (!id) throw new Error("the usual order is missing its ID");
      return orders.getByRole("row").filter({ has: page.getByTitle(id) });
    };
    const paidRow = rowOf(paid.id);
    await expect(paidRow.getByText("US$8.00")).toBeVisible();
    await expect(paidRow.getByText("US$10.00")).toBeVisible();
    await expect(paidRow.getByText("US$0.24")).toBeVisible();
    const unpaidRow = rowOf(unpaid.id);
    await expect(unpaidRow.getByText(zhText("orders.notPaid"))).toBeVisible();
    await expect(unpaidRow.getByText("US$10.00")).toBeVisible();
    const missingRow = rowOf(missing.id);
    await expect(missingRow.getByText(zhText("orders.creditedUnavailable"))).toBeVisible();
    await expect(missingRow.getByText(zhText("orders.notCredited"))).toHaveCount(0);
  });

  test("the narrow-screen confirmation shows the refund facts", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    const state = await open(page, "/orders/refunds");
    const refund = state.refunds[0];
    if (!refund) throw new Error("the usual refunds are missing");
    const queue = page.getByRole("table", { name: zhText("orders.refund.title") });
    await queue.getByRole("row").nth(1).click();
    await page.getByRole("button", { name: zhText("orders.refund.approve"), exact: true }).click();
    await expect(dialog(page).getByText(zhText("orders.refund.review"))).toBeVisible();
    await expect(dialog(page).getByText(refund.account_id ?? "")).toBeVisible();
    await expect(dialog(page).getByText(refund.reason ?? "")).toBeVisible();
    await expect(dialog(page).getByText("US$5.00").first()).toBeVisible();
    await expect(dialog(page).getByText(refund.requested_by ?? "")).toBeVisible();
  });

  test("a person who asked for a refund does not approve it themselves", async ({ page }) => {
    const state = await open(page, "/orders/refunds");
    if (state.refunds[0]) {
      state.refunds[0].requested_by = userId(0);
    }
    await page.reload();
    const queue = page.getByRole("table", { name: zhText("orders.refund.title") });
    await queue.getByRole("row").nth(1).click();
    await expect(page.getByText(zhText("orders.refund.secondPerson"))).toBeVisible();
    await expect(page.getByRole("button", { name: zhText("orders.refund.approve"), exact: true })).toHaveCount(0);
    // The reject is still theirs to press, and the next refund offers an approval: J steps to it.
    await expect(page.getByRole("button", { name: zhText("orders.refund.reject") })).toBeVisible();
    await page.keyboard.press("j");
    await expect(page.getByRole("button", { name: zhText("orders.refund.approve"), exact: true })).toBeVisible();
  });
});
