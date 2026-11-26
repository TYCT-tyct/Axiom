import { expect, it } from "vitest";
import { batchIdOf, orderTone, paymentOrderState, refundTone, shortId } from "./states";

it.each([
  ["paid", "paid"],
  ["frozen", undefined],
  ["", undefined],
  ["succeeded", undefined],
])("an order state from the address: %s", (value, want) => {
  expect(paymentOrderState(value)).toBe(want);
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

// The TypeID specification's own example: a paid order's batch is found by the UUID its ID spells.
it.each([
  ["ord_01h455vb4pex5vsknk084sn02q", "01890a5d-ac96-774b-bcce-b302099a8057"],
  ["ord_00000000000000000000000000", "00000000-0000-0000-0000-000000000000"],
  ["ord_8zzzzzzzzzzzzzzzzzzzzzzzzz", undefined],
  ["ord_short", undefined],
  [undefined, undefined],
])("reads the batch an order made from %s", (order, batch) => {
  expect(batchIdOf(order)).toBe(batch);
});
