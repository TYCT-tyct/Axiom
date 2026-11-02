import type { Schemas } from "@axiom/api";

// A receipt renders the paid order's own facts: when the payment settled, what the channel took and what was credited.
// Nothing is stored for it, so it can never disagree with the order (docs/domains/payments.md「收据与发票」).
type Receipt = {
  paidAt: Date;
  paid: NonNullable<Schemas["Order"]["paid_total"]>;
  credited: NonNullable<Schemas["Order"]["credited"]>;
};

// What a receipt page shows. An order has one once its payment settled; a charged-back order has none because the money
// was taken back again, and a paid order without the facts was read from a node of the previous release.
export type ReceiptState =
  | ({ kind: "ready" } & Receipt)
  | { kind: "unpaid" }
  | { kind: "reversed" }
  | { kind: "unavailable" };

const paidStates = new Set<Schemas["OrderState"]>(["paid", "partially_refunded", "refunded"]);

export function receiptState(order: Schemas["Order"]): ReceiptState {
  if (order.state === "charged_back") {
    return { kind: "reversed" };
  }
  const { paid_at: paidAt, paid_total: paid, credited } = order;
  if (paidAt === undefined || paid === undefined || credited === undefined) {
    return paidStates.has(order.state) ? { kind: "unavailable" } : { kind: "unpaid" };
  }
  return { kind: "ready", paidAt: new Date(paidAt), paid, credited };
}
