import type { AdminSchemas } from "@axiom/api";
import type { TFunction } from "i18next";
import type { Columns, Format } from "../list-table";
import { ShortId, State, SubLine } from "../parts";
import { CreditedAmount, PaidAmount } from "./order-amounts";
import { orderTone } from "./states";

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
          <ShortId id={order.id} />
          <SubLine>{[order.merchant_name, order.method].filter(Boolean).join(" · ")}</SubLine>
        </>
      ),
    },
    {
      key: "account",
      from: "sm",
      header: t("orders.col.account"),
      cell: (order) => <ShortId id={order.account_id} />,
    },
    { key: "paid", header: t("orders.col.total"), align: "end", cell: (order) => <PaidAmount order={order} /> },
    {
      key: "credited",
      from: "lg",
      header: t("orders.col.credits"),
      align: "end",
      cell: (order) => <CreditedAmount order={order} />,
    },
    {
      key: "created",
      from: "lg",
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
