import type { AdminSchemas } from "@axiom/api";
import { DrawerFacts, DrawerSection, Figure, Id, Text, TextLink, Timeline, useFormat } from "@axiom/ui";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Code, State } from "../parts";
import { RowDrawer, type useSelected } from "../row-drawer";
import { CreditedAmount, PaidAmount } from "./order-amounts";
import { orderSteps } from "./order-steps";
import { refundStateText } from "./refunds";
import { refundTone, shortId } from "./states";

type Order = AdminSchemas["PaymentOrder"];

// One order in a drawer: what the channel took, the steps it went through, and its numbers and refunds. The amounts say
// what they are: the cash the channel took, the credit that went in, the quote only beside them when it differs.
export function OrderDrawer({
  order,
  keys,
  select,
}: {
  order: Order;
  keys: readonly string[];
  select: ReturnType<typeof useSelected>[1];
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <RowDrawer
      title={t("orders.drawer.title", { id: shortId(order.id) })}
      subtitle={order.state ? t(`orders.state.${order.state}`) : undefined}
      keys={keys}
      selected={order.id ?? ""}
      select={select}
    >
      <Figure
        value={<PaidAmount order={order} />}
        caption={[order.method, order.merchant_name].filter(Boolean).join("，") || undefined}
      />
      <DrawerSection title={t("orders.drawer.steps")}>
        <Timeline label={t("orders.drawer.steps")} steps={orderSteps(order, t, format)} />
      </DrawerSection>
      <DrawerFacts
        rows={[
          ...(order.account_id
            ? [
                [
                  t("orders.col.account"),
                  <TextLink key="account">
                    <Link to="/users/accounts" search={{ selected: order.account_id }}>
                      <Code title={order.account_id}>{order.account_id}</Code>
                    </Link>
                  </TextLink>,
                ] as const,
              ]
            : []),
          ...(order.id ? [[t("orders.drawer.id"), <Id key="id" value={order.id} />] as const] : []),
          ...(order.trade_no ? [[t("orders.drawer.tradeNo"), <Id key="trade" value={order.trade_no} />] as const] : []),
          [t("orders.col.credits"), <CreditedAmount key="credited" order={order} />],
        ]}
      />
      <OrderRefunds order={order} />
    </RowDrawer>
  );
}

// The refunds the order has, each in the state the channel has it, linked to the refund's own drawer. An order the node
// gives no refunds for says so, which is not the same as having none.
function OrderRefunds({ order }: { order: Order }) {
  const { t } = useTranslation();
  const format = useFormat();
  const refunds = order.refunds ?? [];
  return (
    <DrawerSection title={t("orders.refund.title")}>
      {order.refunds === undefined ? (
        <Text tone="muted">{t("orders.refundsUnavailable")}</Text>
      ) : refunds.length === 0 ? (
        <Text tone="muted">{t("orders.noRefunds")}</Text>
      ) : (
        <DrawerFacts
          rows={refunds.map((refund, index) => [
            refund.created_at ? format.dateTime(new Date(refund.created_at)) : "—",
            <span key={refund.id ?? index}>
              <State tone={refundTone(refund)}>{refundStateText(t, refund)}</State>{" "}
              {refund.id ? (
                <TextLink>
                  <Link to="/orders/refunds" search={{ selected: refund.id }}>
                    {refund.amount ? format.pay(refund.amount) : shortId(refund.id)}
                  </Link>
                </TextLink>
              ) : null}
            </span>,
            refund.id ?? String(index),
          ])}
        />
      )}
    </DrawerSection>
  );
}
