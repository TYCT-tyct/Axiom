import type { AdminSchemas } from "@axiom/api";
import type { TimelineStep, useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import { orderAmounts } from "./amounts";
import { refundStateText } from "./refunds";

type Order = AdminSchemas["PaymentOrder"];
type Format = ReturnType<typeof useFormat>;

const joined = (parts: (string | undefined)[]) => parts.filter(Boolean).join("，") || undefined;
const at = (format: Format, time: string | undefined) => (time ? format.dateTime(new Date(time)) : undefined);

// An order that was never paid ends where it stopped: waiting for the user, or closed.
function unpaidStep(order: Order, t: TFunction, format: Format): TimelineStep {
  if (order.state === "pending") {
    return {
      key: "waiting",
      title: t("orders.timeline.waiting"),
      detail: order.expires_at ? t("orders.timeline.closesAt", { at: at(format, order.expires_at) ?? "" }) : undefined,
      state: "current",
    };
  }
  return {
    key: "closed",
    title: t(
      `orders.timeline.${order.state === "failed" ? "failed" : order.state === "cancelled" ? "cancelled" : "expired"}`,
    ),
    state: order.state === "failed" ? "failed" : "pending",
  };
}

// A paid order: the channel took the money, and the credit went in, or the order does not say it did. Only what the order
// has a record of is a step that happened: a paid order without `credited` says its credit is not recorded and does not
// claim the credit went in.
function paidSteps(order: Order, t: TFunction, format: Format): TimelineStep[] {
  const { credited, creditedMissing } = orderAmounts(order);
  const review = order.state === "review";
  return [
    {
      key: "confirmed",
      title: t("orders.timeline.confirmed"),
      detail: joined([at(format, order.paid_at), order.merchant_name]),
    },
    credited
      ? {
          key: "credited",
          title: t("orders.timeline.credited"),
          detail: t("orders.timeline.creditedBy", { amount: format.money(credited) }),
        }
      : {
          key: "credited",
          title: t("orders.timeline.credited"),
          detail: t(creditedMissing ? "orders.creditedUnavailable" : "orders.notCredited"),
          state: review ? "current" : "pending",
        },
    ...(review ? [{ key: "review", title: t("orders.timeline.review"), state: "current" as const }] : []),
  ];
}

// What was given back, a step for each refund in the state the channel has it, and a chargeback at the end.
function returnSteps(order: Order, t: TFunction, format: Format): TimelineStep[] {
  const refunds = (order.refunds ?? []).map((refund, index): TimelineStep => {
    const done = refund.state === "succeeded";
    return {
      key: refund.id ?? `refund-${index}`,
      title: t("orders.timeline.refund"),
      detail: joined([refund.amount ? format.pay(refund.amount) : undefined, refundStateText(t, refund)]),
      state: done ? "done" : refund.state === "failed" ? "failed" : "current",
    };
  });
  const back = order.state === "charged_back";
  return [
    ...refunds,
    ...(back ? [{ key: "charged-back", title: t("orders.timeline.chargedBack"), state: "failed" as const }] : []),
  ];
}

// The steps an order went through (the design's 下单, 渠道确认, 额度入账), from what the order says it did.
export function orderSteps(order: Order, t: TFunction, format: Format): TimelineStep[] {
  const channel = [order.merchant_name, order.method].filter(Boolean).join(" · ");
  const placed: TimelineStep = {
    key: "placed",
    title: t("orders.timeline.placed"),
    detail: joined([at(format, order.created_at), channel]),
  };
  const unpaid = ["pending", "expired", "cancelled", "failed"].includes(order.state ?? "");
  return unpaid
    ? [placed, unpaidStep(order, t, format)]
    : [placed, ...paidSteps(order, t, format), ...returnSteps(order, t, format)];
}
