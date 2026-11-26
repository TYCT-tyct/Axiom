import type { AdminSchemas } from "@axiom/api";

// The closed lists of the payments contract, in the order the page shows them. An address may name a state a newer
// release added: anything not in the list is not a filter, so an old page still opens.

export const paymentOrderStates = [
  "pending",
  "paid",
  "expired",
  "cancelled",
  "failed",
  "review",
  "partially_refunded",
  "refunded",
  "charged_back",
] as const satisfies readonly AdminSchemas["PaymentOrderState"][];

export function paymentOrderState(value: unknown) {
  return paymentOrderStates.find((state) => state === value);
}

export type Tone = "neutral" | "good" | "warn" | "bad";

const orderTones: Record<AdminSchemas["PaymentOrderState"], Tone> = {
  pending: "warn",
  paid: "good",
  expired: "neutral",
  cancelled: "neutral",
  failed: "bad",
  review: "warn",
  partially_refunded: "warn",
  refunded: "neutral",
  charged_back: "bad",
};

// A frozen refund without an approver is the one waiting for review; once approved it waits for the channel, which is
// neither good nor bad yet.
export function refundTone(refund: Pick<AdminSchemas["PaymentRefund"], "state" | "approved_by">): Tone {
  switch (refund.state) {
    case "succeeded":
      return "good";
    case "failed":
      return "bad";
    case "frozen":
      return refund.approved_by ? "neutral" : "warn";
    case "sending":
    case "pending":
      return "neutral";
    default:
      return "neutral";
  }
}

export const orderTone = (state: AdminSchemas["PaymentOrderState"]) => orderTones[state];

// The last eight characters identify a row in a table, where the whole TypeID would take the width of a column; the
// whole ID stays in the title and in links.
export const shortId = (id: string | undefined) => (id ? id.slice(-8) : "—");

const typeIdDigits = "0123456789abcdefghjkmnpqrstvwxyz";

// batchIdOf is the credit batch a paid order made. The batch has the order's own identity, which the contract writes as a
// UUID where an order's ID is a TypeID of it: a prefix, "_", and the 128 bits in 26 base-32 digits. What is not an order's
// ID gives nothing.
export function batchIdOf(orderId: string | undefined) {
  const digits = orderId?.slice(orderId.lastIndexOf("_") + 1);
  if (digits?.length !== 26) return undefined;
  let value = 0n;
  for (const digit of digits) {
    const at = typeIdDigits.indexOf(digit);
    if (at < 0) return undefined;
    value = value * 32n + BigInt(at);
  }
  const hex = value.toString(16).padStart(32, "0");
  return hex.length === 32
    ? `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
    : undefined;
}
