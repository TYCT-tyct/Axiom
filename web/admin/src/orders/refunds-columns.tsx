import type { AdminSchemas } from "@axiom/api";
import { Text } from "@axiom/ui";
import type { TFunction } from "i18next";
import type { Columns, Format } from "../list-table";
import { ShortId, State, SubLine } from "../parts";
import { refundStateText } from "./refunds";
import { refundTone } from "./states";

type Refund = AdminSchemas["PaymentRefund"];

// The refunds tab's columns: the refund and its order, the account, what was asked for, why, when, and where it stands.
export function refundsColumns(t: TFunction, format: Format): Columns<Refund> {
  return [
    {
      key: "refund",
      header: t("orders.refund.col.refund"),
      cell: (refund) => (
        <>
          <ShortId id={refund.id} />
          <SubLine>
            {t("orders.refund.col.order")} <ShortId id={refund.order_id} />
            {refund.via === "offline" ? ` · ${t("orders.refund.viaOffline")}` : ""}
          </SubLine>
        </>
      ),
    },
    {
      key: "account",
      from: "sm",
      header: t("orders.refund.col.account"),
      cell: (refund) => <ShortId id={refund.account_id} />,
    },
    {
      key: "amount",
      header: t("orders.refund.col.asked"),
      align: "end",
      cell: (refund) => (
        <>
          {refund.credits ? format.money(refund.credits) : "—"}
          {refund.amount ? <SubLine>{format.pay(refund.amount)}</SubLine> : null}
        </>
      ),
    },
    {
      key: "reason",
      from: "lg",
      header: t("orders.refund.col.reason"),
      cell: (refund) => (
        <Text lines={2} title={refund.reason}>
          {refund.reason ?? "—"}
        </Text>
      ),
    },
    {
      key: "asked",
      from: "sm",
      header: t("orders.refund.col.requested"),
      muted: true,
      cell: (refund) => (refund.created_at ? format.dateTime(new Date(refund.created_at)) : "—"),
      sort: (refund) => (refund.created_at ? Date.parse(refund.created_at) : null),
    },
    {
      key: "state",
      header: t("orders.refund.col.state"),
      cell: (refund) => <State tone={refundTone(refund)}>{refundStateText(t, refund)}</State>,
    },
  ];
}
