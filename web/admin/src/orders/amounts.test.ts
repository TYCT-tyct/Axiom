import { expect, it } from "vitest";
import { orderAmounts } from "./amounts";

it("shows the paid amount and marks a different quote", () => {
  const amounts = orderAmounts({
    state: "paid",
    credits: { amount: "10000000000", currency: "USD" },
    total: { amount: "1000", currency: "USD" },
    paid_total: { amount: "800", currency: "USD" },
    paid_fee: { amount: "24", currency: "USD" },
    credited: { amount: "8000000000", currency: "USD" },
  });
  expect(amounts.paid).toEqual({ amount: "800", currency: "USD" });
  expect(amounts.paidFee).toEqual({ amount: "24", currency: "USD" });
  expect(amounts.quote).toEqual({ amount: "1000", currency: "USD" });
  expect(amounts.credited).toEqual({ amount: "8000000000", currency: "USD" });
  expect(amounts.quotedCredits).toEqual({ amount: "10000000000", currency: "USD" });
  expect(amounts.paidMissing).toBe(false);
});

it("does not call a quote a payment", () => {
  const amounts = orderAmounts({ state: "pending", total: { amount: "1000", currency: "USD" } });
  expect(amounts.paid).toBeUndefined();
  expect(amounts.paidFee).toBeUndefined();
  expect(amounts.quote).toEqual({ amount: "1000", currency: "USD" });
  expect(amounts.paidMissing).toBe(false);
});

it("marks a paid state with no paid record instead of showing its quote as paid", () => {
  const amounts = orderAmounts({ state: "paid", total: { amount: "1000", currency: "USD" } });
  expect(amounts.paid).toBeUndefined();
  expect(amounts.paidMissing).toBe(true);
  expect(amounts.quote).toEqual({ amount: "1000", currency: "USD" });
});
