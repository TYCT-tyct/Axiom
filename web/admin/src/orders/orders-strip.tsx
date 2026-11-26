import type { AdminSchemas } from "@axiom/api";
import { MetricStrip, type StripItem, useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { discrepanciesQuery } from "../finance-queries";
import { overviewQuery, paymentRefundsQuery } from "../queries";

// The first day of this UTC month, which is when the dashboard's month begins.
const monthStart = (now: Date) => new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1));

// The figures above the orders (the design's strip), only those the API gives: the cash the month has taken in net of
// refunds, in each payment currency the ledger has, which is the dashboard's own figure for a window that is the month so
// far; and what waits for a person, counted from the first page of each queue, which says "n+" when there is more.
// Receipts and refunds taken apart are not an API figure, so they are not here.
export function OrdersStrip({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const [now] = useState(() => new Date());
  const month = useQuery({ ...overviewQuery(monthStart(now).toISOString(), now.toISOString()), retry: false });
  const refunds = useInfiniteQuery(paymentRefundsQuery(siteId, "frozen"));
  const open = useInfiniteQuery(discrepanciesQuery("open"));
  const count = (query: typeof refunds | typeof open) => {
    const first = query.data?.pages[0]?.data;
    return first === undefined ? undefined : `${format.number(first.length)}${query.hasNextPage ? "+" : ""}`;
  };
  const items: StripItem[] = [];
  if (month.data) {
    items.push({
      key: "cash",
      label: t("orders.strip.cash"),
      value: month.data.cash?.length
        ? month.data.cash.map((amount) => format.money(amount)).join(" · ")
        : format.number(0),
      note: t("orders.strip.cashNote"),
    });
  }
  const frozen = count(refunds);
  if (frozen !== undefined) {
    items.push({
      key: "refunds",
      label: t("orders.strip.refunds"),
      value: frozen,
      suffix: ` ${t("orders.strip.unit")}`,
      note: t("orders.strip.refundsNote"),
      link: <Link to="/orders/refunds" />,
    });
  }
  const differences = count(open);
  if (differences !== undefined) {
    items.push({
      key: "differences",
      label: t("orders.strip.differences"),
      value: differences,
      suffix: ` ${t("orders.strip.unit")}`,
      note: t("orders.strip.differencesNote"),
      link: <Link to="/orders/discrepancies" search={{ state: "open" }} />,
    });
  }
  return items.length ? <MetricStrip label={t("orders.strip.label")} items={items} /> : null;
}
