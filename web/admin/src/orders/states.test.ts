import { expect, it } from "vitest";
import { orderTone, paymentOrderState, paymentRefundState, refundTone, shortId } from "./states";

it.each([
  ["paid", "paid"],
  ["frozen", undefined],
  ["", undefined],
  ["succeeded", undefined],
])("an order state from the address: %s", (value, want) => {
  expect(paymentOrderState(value)).toBe(want);
});

it.each([
  ["frozen", "frozen"],
  ["pending_review", undefined],
  ["succeeded", "succeeded"],
  [42, undefined],
])("a refund state from the address: %s", (value, want) => {
  expect(paymentRefundState(value)).toBe(want);
});

it("tells a refund waiting for review from one already approved", () => {
  expect(refundTone({ state: "frozen" })).toBe("warn");
  expect(refundTone({ state: "frozen", approved_by: "user_1" })).toBe("neutral");
  expect(refundTone({ state: "succeeded" })).toBe("good");
  expect(refundTone({ state: "failed" })).toBe("bad");
});

it("gives each order state the tone the page shows", () => {
  expect(orderTone("paid")).toBe("good");
  expect(orderTone("review")).toBe("warn");
  expect(orderTone("failed")).toBe("bad");
  expect(orderTone("refunded")).toBe("neutral");
});

it("shortens an ID to what identifies a row without losing the whole", () => {
  expect(shortId("ord_000000000000000000000123456789abcdef")).toBe("89abcdef");
  expect(shortId(undefined)).toBe("—");
});
