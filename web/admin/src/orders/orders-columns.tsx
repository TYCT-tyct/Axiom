import type { AdminSchemas } from "@axiom/api";
import type { TFunction } from "i18next";
import type { Columns, Format } from "../list-table";
import { Code, State, SubLine } from "../parts";
import { CreditedAmount, PaidAmount } from "./order-amounts";
import { orderTone, shortId } from "./states";

type Order = AdminSchemas["PaymentOrder"];

// The orders tab's columns: the order and its channel, the account, what the channel took and what was credited (the money
// rules of order-amounts.ts), when, and where it stands.
export function ordersColumns(t: TFunction, format: Format): Columns<Order> {
  return [
    {
      key: "order",
      header: t("orders.col.order"),
      cell: (order) => (
        <>
          <Code title={order.id}>{shortId(order.id)}</Code>
          <SubLine>{[order.merchant_name, order.method].filter(Boolean).join(" · ")}</SubLine>
        </>
      ),
    },
    {
      key: "account",
      header: t("orders.col.account"),
      cell: (order) => <Code title={order.account_id}>{shortId(order.account_id)}</Code>,
    },
    { key: "paid", header: t("orders.col.total"), align: "end", cell: (order) => <PaidAmount order={order} /> },
    {
      key: "credited",
      header: t("orders.col.credits"),
      align: "end",
      cell: (order) => <CreditedAmount order={order} />,
    },
    {
      key: "created",
      header: t("orders.col.time"),
      muted: true,
      cell: (order) => (order.created_at ? format.dateTime(new Date(order.created_at)) : "—"),
      sort: (order) => (order.created_at ? Date.parse(order.created_at) : null),
    },
    {
      key: "state",
      header: t("orders.col.state"),
      cell: (order) =>
        order.state ? <State tone={orderTone(order.state)}>{t(`orders.state.${order.state}`)}</State> : "—",
    },
  ];
}
