import type { AdminSchemas } from "@axiom/api";
import { useSession } from "@axiom/app";
import { DrawerFacts, DrawerSection, Tally, type TallyRow, Text, TextLink, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Code, State } from "../parts";
import { accountQuery } from "../queries";
import { RowDrawer, type useSelected } from "../row-drawer";
import { type Decision, RefundActions, RefundDecision, refundStateText } from "./refunds";
import { batchIdOf, refundTone, shortId } from "./states";

type Refund = AdminSchemas["PaymentRefund"];
type Money = AdminSchemas["Money"];

// What the order's credit batch says about the refund (the design's 付了多少, 用了多少, 最多退多少): the batch of a paid
// top-up has the order's own identity (batchIdOf), so the account's batches hold it. What is used is what left it, what can be refunded at
// most is what is still in it (the credits this very refund froze are in it), as whole integers in billionths. An account
// whose batches this node does not give, or a batch already spent away, has no such line, and the drawer says so rather
// than guess.
export function refundMath(refund: Refund, batches: readonly AdminSchemas["CreditBatch"][] | undefined) {
  const id = batchIdOf(refund.order_id);
  const batch = id ? batches?.find((item) => item.id === id) : undefined;
  if (!batch) return undefined;
  const used = (BigInt(batch.amount.amount) - BigInt(batch.remaining.amount)).toString();
  return { paid: batch.amount, used: { ...batch.amount, amount: used }, most: batch.remaining };
}

// One refund in a drawer: the sum of what was paid, what is used and what at most can go back, the reason, who asked and
// who decided; and, for a refund frozen without its releasing approver, the decision. The requester and an operator who
// has already given the first of two approvals are not offered the approval, and the dialogs are the existing ones.
export function RefundDrawer({
  refund,
  keys,
  select,
}: {
  refund: Refund;
  keys: readonly string[];
  select: ReturnType<typeof useSelected>[1];
}) {
  const { t } = useTranslation();
  const session = useSession();
  const [decision, setDecision] = useState<Decision>();
  const self = refund.requested_by !== undefined && refund.requested_by === session.user.id;
  const firstBySelf = refund.first_approved_by !== undefined && refund.first_approved_by === session.user.id;
  return (
    <RowDrawer
      title={t("orders.refund.drawerTitle", { id: shortId(refund.id) })}
      subtitle={t("orders.refund.drawerSubtitle", { account: shortId(refund.account_id) })}
      keys={keys}
      selected={refund.id ?? ""}
      select={select}
      footer={
        <>
          <RefundActions
            refund={refund}
            self={self}
            firstBySelf={firstBySelf}
            onDecide={(kind) => setDecision({ kind, refund })}
          />
          {decision ? <RefundDecision {...decision} onClose={() => setDecision(undefined)} /> : null}
        </>
      }
    >
      <Body refund={refund} />
    </RowDrawer>
  );
}

function Body({ refund }: { refund: Refund }) {
  const { t } = useTranslation();
  const format = useFormat();
  const account = useQuery({
    ...accountQuery(refund.account_id as AdminSchemas["AccountId"]),
    enabled: Boolean(refund.account_id),
  });
  const math = refundMath(refund, account.data?.balance.batches);
  const money = (amount: Money) => format.money(amount);
  const rows: TallyRow[] = [
    ...(math
      ? [
          { label: t("orders.refund.paid"), value: money(math.paid) },
          { label: t("orders.refund.used"), value: money(math.used) },
          { label: t("orders.refund.most"), value: money(math.most), sum: true },
        ]
      : []),
    { label: t("orders.refund.asked"), value: refund.credits ? money(refund.credits) : "—", sum: !math },
  ];
  return (
    <>
      <Tally label={t("orders.refund.sum")} rows={rows} />
      {math ? null : (
        <Text size="sm" tone="muted">
          {t("orders.refund.noBatch")}
        </Text>
      )}
      <DrawerSection title={t("orders.refund.col.reason")}>
        <Text breaks>{refund.reason ?? "—"}</Text>
      </DrawerSection>
      <RefundFacts refund={refund} />
    </>
  );
}

function RefundFacts({ refund }: { refund: Refund }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <DrawerFacts
      rows={[
        [
          t("orders.refund.col.state"),
          <State key="state" tone={refundTone(refund)}>
            {refundStateText(t, refund)}
          </State>,
        ],
        ...(refund.amount ? [[t("orders.refund.col.amount"), format.pay(refund.amount)] as const] : []),
        [
          t("orders.refund.col.order"),
          <TextLink key="order">
            <Link to="/orders" search={{ selected: refund.order_id }}>
              <Code title={refund.order_id}>{shortId(refund.order_id)}</Code>
            </Link>
          </TextLink>,
        ],
        [
          t("orders.refund.col.requested"),
          <Code key="by" title={refund.requested_by ?? refund.requested_by_agent}>
            {refund.requested_by ?? refund.requested_by_agent ?? "—"}
          </Code>,
        ],
        ...(refund.first_approved_by
          ? [[t("orders.refund.col.firstApproved"), <Code key="first">{refund.first_approved_by}</Code>] as const]
          : []),
        ...(refund.approved_by
          ? [[t("orders.refund.decidedBy"), <Code key="by2">{refund.approved_by}</Code>] as const]
          : []),
        ...(refund.provider_ref
          ? [[t("orders.refund.providerRef"), <Code key="ref">{refund.provider_ref}</Code>] as const]
          : []),
      ]}
    />
  );
}
