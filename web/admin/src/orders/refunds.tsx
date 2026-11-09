import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { LoadMore, useSession } from "@axiom/app";
import { Button, DataState, EmptyState, Panel, Table, Td, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { Code, Head, State } from "../parts";
import { paymentRefundsQuery } from "../queries";
import { approvalOutcome, type RefundApprovalOutcome } from "./refund-approval";
import { refundTone, shortId } from "./states";

type Refund = AdminSchemas["PaymentRefund"];
type Decision = { kind: "approve" | "reject"; refund: Refund };

// The refunds frozen for a decision, above the orders: a frozen refund holds a payer's money until an operator releases
// or rejects it, and above the site's limit it waits for a second operator.
export function RefundQueue({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const { t } = useTranslation();
  const session = useSession();
  const refunds = useInfiniteQuery(paymentRefundsQuery(siteId, "frozen"));
  const rows = refunds.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const [decision, setDecision] = useState<Decision | null>(null);
  return (
    <Panel title={t("orders.refund.title")}>
      <p className="px-4 pt-4 text-t2">{t("orders.refund.description")}</p>
      <DataState
        query={refunds}
        isEmpty={() => rows.length === 0}
        empty={<EmptyState title={t("orders.refund.emptyTitle")}>{t("orders.refund.emptyBody")}</EmptyState>}
      >
        {() => (
          <Table label={t("orders.refund.title")}>
            <Head
              cols={[
                [t("orders.refund.col.refund")],
                [t("orders.refund.col.account"), "hidden md:table-cell"],
                [t("orders.refund.col.credits"), "hidden text-right sm:table-cell"],
                [t("orders.refund.col.amount"), "hidden text-right sm:table-cell"],
                [t("orders.refund.col.reason"), "hidden lg:table-cell"],
                [t("orders.refund.col.state"), "hidden sm:table-cell"],
              ]}
              action
            />
            <tbody>
              {rows.map((refund, index) => (
                <RefundRow
                  key={refund.id ?? index}
                  refund={refund}
                  self={refund.requested_by !== undefined && refund.requested_by === session.user.id}
                  firstBySelf={refund.first_approved_by !== undefined && refund.first_approved_by === session.user.id}
                  onDecide={(kind) => setDecision({ kind, refund })}
                />
              ))}
            </tbody>
          </Table>
        )}
      </DataState>
      <LoadMore list={refunds} />
      {decision ? <RefundDecision {...decision} onClose={() => setDecision(null)} /> : null}
    </Panel>
  );
}

type RefundRowProps = {
  refund: Refund;
  self: boolean;
  firstBySelf: boolean;
  onDecide: (kind: Decision["kind"]) => void;
};

function RefundRow({ refund, self, firstBySelf, onDecide }: RefundRowProps) {
  const { t } = useTranslation();
  const format = useFormat();
  const credits = refund.credits ? format.money(refund.credits) : "—";
  return (
    <tr className="align-top hover:bg-s2">
      <Td>
        <Code title={refund.id}>{shortId(refund.id)}</Code>
        <span className="block text-label text-t3" title={refund.created_at}>
          {refund.created_at ? format.dateTime(new Date(refund.created_at)) : "—"}
        </span>
        <span className="block text-label text-t3" title={refund.order_id}>
          {t("orders.refund.col.order")} {shortId(refund.order_id)}
        </span>
        {refund.via === "offline" ? (
          <span className="block text-label text-t3">{t("orders.refund.viaOffline")}</span>
        ) : null}
        <span className="mt-1 block text-label text-t3 sm:hidden">
          {refundStateText(t, refund)}
          {" · "}
          {credits}
        </span>
      </Td>
      <Td className="hidden md:table-cell">
        <Code title={refund.account_id}>{shortId(refund.account_id)}</Code>
      </Td>
      <Td className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">{credits}</Td>
      <Td className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">
        {refund.amount ? format.pay(refund.amount) : "—"}
      </Td>
      <Td className="hidden max-w-64 lg:table-cell">
        <span className="block truncate" title={refund.reason}>
          {refund.reason ?? "—"}
        </span>
        <span className="block text-label text-t3" title={refund.requested_by ?? refund.requested_by_agent}>
          {refund.requested_by ?? refund.requested_by_agent ?? ""}
        </span>
      </Td>
      <Td className="hidden sm:table-cell">
        <State tone={refundTone(refund)}>{refundStateText(t, refund)}</State>
        {refund.provider_ref ? <span className="mt-1 block text-label text-t3">{refund.provider_ref}</span> : null}
      </Td>
      <Td>
        <RefundActions refund={refund} self={self} firstBySelf={firstBySelf} onDecide={onDecide} />
      </Td>
    </tr>
  );
}

// A refund frozen without a releasing approver waits for one here. The requester and the operator whose first approval
// is already recorded see why they cannot release it; the approver sees which of the two approvals the click records.
export function RefundActions({ refund, self, firstBySelf, onDecide }: RefundRowProps) {
  const { t } = useTranslation();
  if (refund.state !== "frozen" || refund.approved_by) {
    return null;
  }
  const second = refund.second_required === true && refund.first_approved_by !== undefined;
  return (
    <div className="flex flex-col items-end gap-2 sm:flex-row sm:flex-wrap sm:justify-end">
      {self || firstBySelf ? (
        <span className="text-label text-t3">
          {t(firstBySelf ? "orders.refund.firstApproved" : "orders.refund.secondPerson")}
        </span>
      ) : (
        <Button size="sm" onClick={() => onDecide("approve")}>
          {t(second ? "orders.refund.secondApprove" : "orders.refund.approve")}
        </Button>
      )}
      <Button size="sm" variant="ghost" onClick={() => onDecide("reject")}>
        {t("orders.refund.reject")}
      </Button>
    </div>
  );
}

function refundStateText(t: TFunction, refund: Refund): string {
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

function RefundDecision({ kind, refund, onClose }: Decision & { onClose: () => void }) {
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

// RefundFacts keeps the decision's basis readable at every width: the queue hides its wider columns on a phone, so the
// confirmation has the account, cash, credits, reason and requester before the person approves or rejects.
function RefundFacts({ refund }: { refund: Refund }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <div className="space-y-2">
      <p className="text-label text-t3">{t("orders.refund.review")}</p>
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
        <dt className="text-label text-t3">{t("orders.refund.col.account")}</dt>
        <dd>
          <Code title={refund.account_id}>{refund.account_id ?? "—"}</Code>
        </dd>
        <dt className="text-label text-t3">{t("orders.refund.col.order")}</dt>
        <dd>
          <Code title={refund.order_id}>{refund.order_id ?? "—"}</Code>
        </dd>
        <dt className="text-label text-t3">{t("orders.refund.col.credits")}</dt>
        <dd>{refund.credits ? format.money(refund.credits) : "—"}</dd>
        <dt className="text-label text-t3">{t("orders.refund.col.amount")}</dt>
        <dd>{refund.amount ? format.pay(refund.amount) : "—"}</dd>
        <dt className="text-label text-t3">{t("orders.refund.col.reason")}</dt>
        <dd className="break-words">{refund.reason ?? "—"}</dd>
        <dt className="text-label text-t3">{t("orders.refund.col.requested")}</dt>
        <dd>
          <Code title={refund.requested_by ?? refund.requested_by_agent}>
            {refund.requested_by ?? refund.requested_by_agent ?? "—"}
          </Code>
        </dd>
        {refund.first_approved_by ? (
          <>
            <dt className="text-label text-t3">{t("orders.refund.col.firstApproved")}</dt>
            <dd>
              <Code title={refund.first_approved_by}>{refund.first_approved_by}</Code>
            </dd>
          </>
        ) : null}
      </dl>
    </div>
  );
}

// The per-order refund list in the orders table, where the wider columns have room for it.
export function Refunds({ refunds }: { refunds: Refund[] | undefined }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (refunds === undefined) {
    return <span className="text-label text-t3">{t("orders.refundsUnavailable")}</span>;
  }
  if (refunds.length === 0) {
    return <span className="text-label text-t3">{t("orders.noRefunds")}</span>;
  }
  return (
    <ul className="space-y-1">
      {refunds.map((refund) => (
        <li key={refund.id} className="flex flex-wrap items-center gap-x-2 gap-y-1 text-label">
          <State tone={refundTone(refund)}>{refundStateText(t, refund)}</State>
          <span className="tabular-nums">{refund.credits ? format.money(refund.credits) : "—"}</span>
          <span className="tabular-nums text-t3">{refund.amount ? format.pay(refund.amount) : "—"}</span>
        </li>
      ))}
    </ul>
  );
}
