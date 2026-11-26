import type { AdminSchemas } from "@axiom/api";
import { Text, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ListTable } from "../list-table";
import { paymentRefundsQuery } from "../queries";
import { useSelected } from "../row-drawer";
import { RefundDrawer } from "./refund-drawer";
import { refundsColumns } from "./refunds-columns";

type Refund = AdminSchemas["PaymentRefund"];
type RefundFilter = "all" | AdminSchemas["PaymentRefundState"];
const refundStates = [
  "all",
  "frozen",
  "sending",
  "pending",
  "succeeded",
  "failed",
] as const satisfies readonly RefundFilter[];

// The refunds tab: the site's refunds, newest first, by state on the server. A refund frozen for a decision is the one
// waiting for a person; a row opens it with what was paid, used and is left to refund, and the decision. A refund takes
// two people when it is above the site's limit, and the requester never approves their own: the drawer hides the
// approval from them (RefundActions), and the server refuses it as well.
export function RefundsTab({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const [selected, select] = useSelected();
  const [state, setState] = useState<RefundFilter>("all");
  const [text, setText] = useState("");
  const query = useInfiniteQuery(paymentRefundsQuery(siteId, state === "all" ? undefined : state));
  const loaded = query.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const word = text.trim().toLowerCase();
  const rows = loaded.filter((refund) =>
    [refund.id, refund.order_id, refund.account_id, refund.reason].some((value) => value?.toLowerCase().includes(word)),
  );
  const open = loaded.find((refund) => refund.id === selected);
  return (
    <>
      <Text tone="muted">{t("orders.refund.description")}</Text>
      <ListTable<Refund, RefundFilter>
        label={t("orders.refund.title")}
        unit={t("orders.refund.unit")}
        search={{ label: t("orders.refund.search"), value: text, onChange: setText }}
        filter={{
          label: t("orders.refund.col.state"),
          value: state,
          options: refundStates.map((value) => ({
            value,
            label: value === "all" ? t("orders.filters.stateAny") : t(`orders.refund.filters.${value}`),
          })),
          onChange: setState,
        }}
        query={query}
        rows={rows}
        loaded={loaded.length}
        rowKey={(refund) => refund.id ?? ""}
        selected={selected}
        onOpen={(refund) => refund.id && select(refund.id)}
        narrowed={Boolean(word) || state !== "all"}
        empty={{ title: t("orders.refund.emptyTitle"), body: t("orders.refund.emptyBody") }}
        columns={refundsColumns(t, format)}
      />
      {open ? (
        <RefundDrawer refund={open} keys={rows.flatMap((refund) => (refund.id ? [refund.id] : []))} select={select} />
      ) : null}
    </>
  );
}
