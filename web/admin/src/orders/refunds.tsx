import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { ActionButton, Cluster, Facts, Text, useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { Code } from "../parts";
import { approvalOutcome, type RefundApprovalOutcome } from "./refund-approval";
import { shortId } from "./states";

type Refund = AdminSchemas["PaymentRefund"];
export type Decision = { kind: "approve" | "reject"; refund: Refund };

export type RefundActionProps = {
  refund: Refund;
  self: boolean;
  firstBySelf: boolean;
  onDecide: (kind: Decision["kind"]) => void;
};

// A refund frozen without a releasing approver waits for one here. The requester and the operator whose first approval
// is already recorded see why they cannot release it; the approver sees which of the two approvals the click records.
export function RefundActions({ refund, self, firstBySelf, onDecide }: RefundActionProps) {
  const { t } = useTranslation();
  if (refund.state !== "frozen" || refund.approved_by) {
    return null;
  }
  const second = refund.second_required === true && refund.first_approved_by !== undefined;
  return (
    <Cluster justify="end">
      {self || firstBySelf ? (
        <Text as="span" size="sm" tone="faint">
          {t(firstBySelf ? "orders.refund.firstApproved" : "orders.refund.secondPerson")}
        </Text>
      ) : (
        <ActionButton size="sm" onClick={() => onDecide("approve")}>
          {t(second ? "orders.refund.secondApprove" : "orders.refund.approve")}
        </ActionButton>
      )}
      <ActionButton size="sm" variant="gho" onClick={() => onDecide("reject")}>
        {t("orders.refund.reject")}
      </ActionButton>
    </Cluster>
  );
}

export function refundStateText(t: TFunction, refund: Refund): string {
  switch (refund.state) {
    case "frozen":
      if (refund.approved_by) {
        return t("orders.refund.state.approved");
      }
      return t(refund.first_approved_by ? "orders.refund.state.firstApproved" : "orders.refund.state.pendingReview");
    case "sending":
      return t("orders.refund.state.sending");
    case "pending":
      return t("orders.refund.state.pending");
    case "succeeded":
      return t("orders.refund.state.succeeded");
    case "failed":
      return t("orders.refund.state.failed");
    default:
      return "—";
  }
}

export function RefundDecision({ kind, refund, onClose }: Decision & { onClose: () => void }) {
  const { t } = useTranslation();
  const approve = kind === "approve";
  const first = approve && refund.second_required === true && refund.first_approved_by === undefined;
  const intro = confirmation(t, approve, first, refund.via === "offline");
  const id = refund.id ?? "";
  return (
    <ChangeDialog
      title={t(approve ? "orders.refund.approveTitle" : "orders.refund.rejectTitle", { id: shortId(refund.id) })}
      operation={`${kind}-refund:${id}`}
      intro={
        <>
          <p>{intro.text}</p>
          <RefundFacts refund={refund} />
        </>
      }
      action={intro.action}
      danger={approve}
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST(
            approve
              ? "/api/admin/v1/payments/refunds/{refund_id}/approve"
              : "/api/admin/v1/payments/refunds/{refund_id}/reject",
            { params: { path: { refund_id: id }, header: { "Idempotency-Key": key } }, body: { reason } },
          ),
        )
      }
      done={(out) => <p>{outcomeText(t, approve, approvalOutcome(out))}</p>}
      onClose={onClose}
    />
  );
}

// What the click records: a first approval releases nothing, and the operator must know that before confirming.
function confirmation(t: TFunction, approve: boolean, first: boolean, offline: boolean) {
  if (!approve) {
    return { text: t("orders.refund.rejectIntro"), action: t("orders.refund.rejectAction") };
  }
  if (first) {
    return { text: t("orders.refund.firstApprovalIntro"), action: t("orders.refund.firstApprovalAction") };
  }
  return {
    text: t(offline ? "orders.refund.approveIntroOffline" : "orders.refund.approveIntro"),
    action: t("orders.refund.approveAction"),
  };
}

// What the answer recorded, which can differ from what the dialog opened for: another operator may have released the
// refund in between, so the dialog reads the answer instead of its own snapshot.
function outcomeText(t: TFunction, approve: boolean, outcome: RefundApprovalOutcome): string {
  if (!approve) {
    return t("orders.refund.rejected");
  }
  if (outcome === "first") {
    return t("orders.refund.firstApprovedDone");
  }
  return t(outcome === "offline" ? "orders.refund.approvedOffline" : "orders.refund.approved");
}

// RefundFacts keeps the decision's basis in the confirmation: the account, cash, credits, reason and requester are
// before the person approves or rejects.
function RefundFacts({ refund }: { refund: Refund }) {
  const { t } = useTranslation();
  const format = useFormat();
  const rows: [string, ReactNode][] = [
    [
      t("orders.refund.col.account"),
      <Code key="account" title={refund.account_id}>
        {refund.account_id ?? "—"}
      </Code>,
    ],
    [
      t("orders.refund.col.order"),
      <Code key="order" title={refund.order_id}>
        {refund.order_id ?? "—"}
      </Code>,
    ],
    [t("orders.refund.col.credits"), refund.credits ? format.money(refund.credits) : "—"],
    [t("orders.refund.col.amount"), refund.amount ? format.pay(refund.amount) : "—"],
    [t("orders.refund.col.reason"), refund.reason ?? "—"],
    [
      t("orders.refund.col.requested"),
      <Code key="requested" title={refund.requested_by ?? refund.requested_by_agent}>
        {refund.requested_by ?? refund.requested_by_agent ?? "—"}
      </Code>,
    ],
  ];
  if (refund.first_approved_by) {
    rows.push([
      t("orders.refund.col.firstApproved"),
      <Code key="first" title={refund.first_approved_by}>
        {refund.first_approved_by}
      </Code>,
    ]);
  }
  return <Facts title={t("orders.refund.review")} rows={rows} />;
}
