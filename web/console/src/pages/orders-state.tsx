import type { Schemas } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Button, Cluster, FormError, Stack, Tag, useFormat } from "@axiom/ui";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useOrderActions } from "../order-actions";
import { receiptState } from "../receipt";

export type Order = Schemas["Order"];
type OrderRefund = NonNullable<Order["refunds"]>[number];

const STATE_TONE: Record<Order["state"], "neutral" | "ok" | "warn" | "bad"> = {
  pending: "warn",
  paid: "ok",
  expired: "neutral",
  cancelled: "neutral",
  failed: "bad",
  review: "warn",
  partially_refunded: "warn",
  refunded: "neutral",
  charged_back: "bad",
};

const REFUND_TONE: Record<OrderRefund["state"], "neutral" | "ok" | "warn" | "bad"> = {
  pending_review: "warn",
  processing: "warn",
  succeeded: "ok",
  declined: "bad",
};

function OrderStateBadge({ state }: { state: Order["state"] }) {
  const { t } = useTranslation();
  const labels: Record<Order["state"], string> = {
    pending: t("orders.state.pending"),
    paid: t("orders.state.paid"),
    expired: t("orders.state.expired"),
    cancelled: t("orders.state.cancelled"),
    failed: t("orders.state.failed"),
    review: t("orders.state.review"),
    partially_refunded: t("orders.state.partially_refunded"),
    refunded: t("orders.state.refunded"),
    charged_back: t("orders.state.charged_back"),
  };
  return <Tag tone={STATE_TONE[state]}>{labels[state]}</Tag>;
}

function RefundStateBadge({ state }: { state: OrderRefund["state"] }) {
  const { t } = useTranslation();
  const labels: Record<OrderRefund["state"], string> = {
    pending_review: t("orders.refundState.pending_review"),
    processing: t("orders.refundState.processing"),
    succeeded: t("orders.refundState.succeeded"),
    declined: t("orders.refundState.declined"),
  };
  return <Tag tone={REFUND_TONE[state]}>{labels[state]}</Tag>;
}

const hasPendingRefund = (order: Order) =>
  (order.refunds ?? []).some((refund) => refund.state === "pending_review" || refund.state === "processing");

const canRequestRefund = (order: Order) =>
  order.refund_allowed === true &&
  (order.state === "paid" || order.state === "partially_refunded") &&
  !hasPendingRefund(order);

export function RefundList({ refunds }: { refunds: Order["refunds"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (refunds === undefined) return <span>{t("orders.unavailable")}</span>;
  if (refunds.length === 0) return <span>{t("orders.noRefunds")}</span>;
  return (
    <Stack gap={4}>
      {refunds.map((refund) => (
        <Cluster key={refund.id} gap={8}>
          <RefundStateBadge state={refund.state} />
          <span>{format.pay(refund.amount)}</span>
          <span>{format.money(refund.credits)}</span>
        </Cluster>
      ))}
    </Stack>
  );
}

export function OrderState({
  order,
  allowed,
  onRefund,
}: {
  order: Order;
  allowed: boolean;
  onRefund: (order: Order) => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const actions = useOrderActions(order.id);
  const showVerify = order.state === "pending";
  const showReceipt = receiptState(order).kind === "ready";
  const showRefund = allowed && canRequestRefund(order);
  const pending = actions.verify.isPending || actions.cancel.isPending;
  const actionError = actions.verify.error ?? actions.cancel.error;

  return (
    <Stack gap={8}>
      <Cluster gap={8}>
        <OrderStateBadge state={order.state} />
        {order.checkout?.pay_url ? (
          <a href={order.checkout.pay_url} target="_blank" rel="noopener" referrerPolicy="origin">
            {t("orders.pay")}
          </a>
        ) : null}
      </Cluster>
      {showVerify || showReceipt || showRefund ? (
        <Cluster gap={8}>
          {showVerify ? (
            <>
              <Button size="sm" variant="primary" disabled={pending} onClick={() => actions.verify.mutate()}>
                {t("orders.verify")}
              </Button>
              <Button size="sm" variant="ghost" disabled={pending} onClick={() => actions.cancel.mutate()}>
                {t("orders.cancelOrder")}
              </Button>
            </>
          ) : null}
          {showReceipt ? (
            <Button asChild size="sm" variant="ghost">
              <Link to="/orders/$orderId/receipt" params={{ orderId: order.id }}>
                {t("orders.receipt")}
              </Link>
            </Button>
          ) : null}
          {showRefund ? (
            <Button size="sm" variant="secondary" onClick={() => onRefund(order)}>
              {t("orders.refund")}
            </Button>
          ) : null}
        </Cluster>
      ) : null}
      {actionError ? <FormError>{errors.message(actionError)}</FormError> : null}
    </Stack>
  );
}
