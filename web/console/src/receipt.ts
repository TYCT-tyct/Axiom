import type { Schemas } from "@axiom/api";

// A receipt renders the paid order's own facts: when the payment settled, what the channel took, what was credited, and
// the refunds that returned money. Nothing is stored for it, so it can never disagree with the order
// (docs/domains/payments.md「收据与发票」).
type Receipt = {
  paidAt: Date;
  paid: NonNullable<Schemas["Order"]["paid_total"]>;
  credited: NonNullable<Schemas["Order"]["credited"]>;
  refunds: NonNullable<Schemas["Order"]["refunds"]>;
};

// What a receipt page shows. A receipt needs the whole record: a paid order read from a node of the previous release
// has no paid facts, and one read through a response that does not carry the refunds cannot say whether money came back,
// so both are unavailable rather than a receipt with a blank.
export type ReceiptState = ({ kind: "ready" } & Receipt) | { kind: "unpaid" } | { kind: "unavailable" };

// The states that mean the site took money, including a chargeback: the payment happened, whatever became of it after.
const paidStates = new Set<Schemas["OrderState"]>(["paid", "partially_refunded", "refunded", "charged_back"]);

export function receiptState(order: Schemas["Order"]): ReceiptState {
  const { paid_at: paidAt, paid_total: paid, credited, refunds } = order;
  if (paidAt === undefined || paid === undefined || credited === undefined || refunds === undefined) {
    return paidStates.has(order.state) ? { kind: "unavailable" } : { kind: "unpaid" };
  }
  return { kind: "ready", paidAt: new Date(paidAt), paid, credited, refunds };
}
