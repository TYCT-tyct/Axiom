import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { refundMath } from "./refund-drawer";

const batch = (id: string, amount: string, remaining: string) =>
  ({
    id,
    source: "paid",
    amount: { amount, currency: "USD" },
    remaining: { amount: remaining, currency: "USD" },
    frozen: { amount: "0", currency: "USD" },
    created_at: "2026-11-01T00:00:00Z",
  }) as AdminSchemas["CreditBatch"];

// The refund's order is the TypeID of the UUID its paid batch has.
const refund = { order_id: "ord_01h455vb4pex5vsknk084sn02q" } as AdminSchemas["PaymentRefund"];
const uuid = "01890a5d-ac96-774b-bcce-b302099a8057";

it("works out used and the most that can go back from the order's own batch, exactly", () => {
  const math = refundMath(refund, [batch("x", "1", "1"), batch(uuid, "9007199254740995", "2")]);
  expect(math?.paid.amount).toBe("9007199254740995");
  expect(math?.used.amount).toBe("9007199254740993");
  expect(math?.most.amount).toBe("2");
});

it.each([
  ["no batch of the order", [batch("x", "5", "5")]],
  ["no batches given", undefined],
])("works out nothing for %s", (_, batches) => {
  expect(refundMath(refund, batches)).toBeUndefined();
});
