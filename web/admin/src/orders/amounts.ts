import type { AdminSchemas } from "@axiom/api";

type Order = AdminSchemas["PaymentOrder"];
type PayAmount = AdminSchemas["PayAmount"];
type Money = AdminSchemas["Money"];

const same = (a: { amount: string; currency: string } | undefined, b: { amount: string; currency: string } | undefined) =>
  a !== undefined && b !== undefined && a.amount === b.amount && a.currency === b.currency;

const unpaid = (state: Order["state"]) =>
  state === "pending" || state === "expired" || state === "cancelled" || state === "failed";

// orderAmounts separates what was quoted from what the channel actually took. paid_total and paid_fee are the paid
// side; total and fee are the quote and only shown beside it when they differ. A state known to be unpaid says so; any
// other missing paid_total or credited is a record this node does not have, not a claim that nothing moved.
export function orderAmounts(order: Order): {
  paid?: PayAmount;
  paidFee?: PayAmount;
  quote?: PayAmount;
  credited?: Money;
  quotedCredits?: Money;
  paidMissing: boolean;
  creditedMissing: boolean;
} {
  const paid = order.paid_total;
  const credited = order.credited;
  return {
    paid,
    paidFee: order.paid_fee,
    quote: order.total && !same(paid, order.total) ? order.total : undefined,
    credited,
    quotedCredits: order.credits && !same(credited, order.credits) ? order.credits : undefined,
    paidMissing: paid === undefined && !unpaid(order.state),
    creditedMissing: credited === undefined && !unpaid(order.state),
  };
}
