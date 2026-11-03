import type { AdminSchemas } from "@axiom/api";

type Refund = AdminSchemas["PaymentRefund"];

// approvalOutcome reads the answer rather than the dialog's snapshot. Two administrators can open the same refund
// before its first approval; the second submission then releases it, and the dialog must say so.
export type RefundApprovalOutcome = "released" | "offline" | "first";

export function approvalOutcome(refund: Refund): RefundApprovalOutcome {
  if (refund.approved_by === undefined) {
    return "first";
  }
  return refund.via === "offline" ? "offline" : "released";
}
