import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { LoadMore, useSession } from "@axiom/app";
import {
  ActionButton,
  Card,
  Cluster,
  DataState,
  EmptyState,
  Facts,
  Mono,
  Stack,
  Table,
  Td,
  Text,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
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
    <Card title={t("orders.refund.title")} lede={t("orders.refund.description")} layout="flush">
      <DataState
        query={refunds}
        isEmpty={() => rows.length === 0}
        empty={<EmptyState title={t("orders.refund.emptyTitle")}>{t("orders.refund.emptyBody")}</EmptyState>}
      >
        {() => (
          <Table label={t("orders.refund.title")}>
            <Head
              cols={[
                t("orders.refund.col.refund"),
                t("orders.refund.col.account"),
                [t("orders.refund.col.credits"), { align: "end" }],
                [t("orders.refund.col.amount"), { align: "end" }],
                [t("orders.refund.col.reason"), { main: true }],
                t("orders.refund.col.state"),
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
    </Card>
  );
}

type RefundRowProps = {
  refund: Refund;
  self: boolean;
  firstBySelf: boolean;
  onDecide: (kind: Decision["kind"]) => void;
};

function Small({ children }: { children: ReactNode }) {
  return (
    <Text as="div" size="sm" tone="faint">
      {children}
    </Text>
  );
}

function RefundRow({ refund, self, firstBySelf, onDecide }: RefundRowProps) {
  const { t } = useTranslation();
  const format = useFormat();
  const credits = refund.credits ? format.money(refund.credits) : "—";
  return (
    <tr>
      <Td>
        <Code title={refund.id}>{shortId(refund.id)}</Code>
        <Small>{refund.created_at ? format.dateTime(new Date(refund.created_at)) : "—"}</Small>
        <Small>
          {t("orders.refund.col.order")} <Mono title={refund.order_id}>{shortId(refund.order_id)}</Mono>
        </Small>
        {refund.via === "offline" ? <Small>{t("orders.refund.viaOffline")}</Small> : null}
      </Td>
      <Td>
        <Code title={refund.account_id}>{shortId(refund.account_id)}</Code>
      </Td>
      <Td align="end">{credits}</Td>
      <Td align="end">{refund.amount ? format.pay(refund.amount) : "—"}</Td>
      <Td main>
        {refund.reason ?? "—"}
        <Small>
          <Mono title={refund.requested_by ?? refund.requested_by_agent}>
            {refund.requested_by ?? refund.requested_by_agent ?? ""}
          </Mono>
        </Small>
      </Td>
      <Td>
        <State tone={refundTone(refund)}>{refundStateText(t, refund)}</State>
        {refund.provider_ref ? <Small>{refund.provider_ref}</Small> : null}
      </Td>
      <Td shrink>
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

// The per-order refund list in the orders table, where the wider columns have room for it.
export function Refunds({ refunds }: { refunds: Refund[] | undefined }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (refunds === undefined) {
    return (
      <Text as="span" size="sm" tone="faint">
        {t("orders.refundsUnavailable")}
      </Text>
    );
  }
  if (refunds.length === 0) {
    return (
      <Text as="span" size="sm" tone="faint">
        {t("orders.noRefunds")}
      </Text>
    );
  }
  return (
    <Stack gap={4}>
      {refunds.map((refund) => (
        <Cluster key={refund.id}>
          <State tone={refundTone(refund)}>{refundStateText(t, refund)}</State>
          <Text as="span" size="sm" numeric>
            {refund.credits ? format.money(refund.credits) : "—"}
          </Text>
          <Text as="span" size="sm" tone="faint" numeric>
            {refund.amount ? format.pay(refund.amount) : "—"}
          </Text>
        </Cluster>
      ))}
    </Stack>
  );
}
