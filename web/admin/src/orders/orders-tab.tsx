import type { AdminSchemas } from "@axiom/api";
import { useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ListTable } from "../list-table";
import { paymentOrdersQuery } from "../queries";
import { useSelected } from "../row-drawer";
import { OrderDrawer } from "./order-drawer";
import { ordersColumns } from "./orders-columns";
import { paymentOrderState, paymentOrderStates } from "./states";

type Order = AdminSchemas["PaymentOrder"];
type OrderFilter = "all" | AdminSchemas["PaymentOrderState"];

const accountId = /^acct_[0-9a-hjkmnp-tv-z]{26}$/;
const userId = /^user_[0-9a-hjkmnp-tv-z]{26}$/;

// The orders tab: the site's top-up orders, newest first, found by an account's or a user's ID (the server's filters) or by
// a word in what has been loaded, narrowed by state, which is the address's, as are the channel and the dates a link may
// carry. A row opens the order as a timeline.
export function OrdersTab({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const navigate = useNavigate();
  const filters = useSearch({ from: "/app/orders" });
  const [selected, select] = useSelected();
  const [text, setText] = useState(filters.account_id ?? filters.user_id ?? "");
  const word = text.trim();
  const state = paymentOrderState(filters.state);
  const query = useInfiniteQuery(
    paymentOrdersQuery(siteId, {
      account_id: accountId.test(word) ? (word as AdminSchemas["AccountId"]) : filters.account_id,
      user_id: userId.test(word) ? (word as AdminSchemas["UserId"]) : filters.user_id,
      state,
      merchant_id: filters.merchant_id,
      since: filters.since,
      until: filters.until,
    }),
  );
  const loaded = query.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const exact = accountId.test(word) || userId.test(word);
  const rows = loaded.filter((order) => exact || !word || matches(order, word.toLowerCase()));
  const open = loaded.find((order) => order.id === selected);
  return (
    <>
      <ListTable<Order, OrderFilter>
        label={t("orders.tabs.orders")}
        unit={t("orders.unit")}
        search={{ label: t("orders.search"), value: text, onChange: setText }}
        filter={{
          label: t("orders.filters.state"),
          value: state ?? "all",
          options: [
            { value: "all", label: t("orders.filters.stateAny") },
            ...paymentOrderStates.map((value) => ({ value, label: t(`orders.state.${value}`) })),
          ],
          onChange: (value) =>
            void navigate({
              to: "/orders",
              search: { ...filters, state: paymentOrderState(value), selected: undefined },
              replace: true,
            }),
        }}
        query={query}
        rows={rows}
        loaded={loaded.length}
        rowKey={(order) => order.id ?? ""}
        selected={selected}
        onOpen={(order) => order.id && select(order.id)}
        narrowed={Boolean(word || state)}
        empty={{ title: t("orders.empty.title"), body: t("orders.empty.body") }}
        columns={ordersColumns(t, format)}
      />
      {open ? (
        <OrderDrawer order={open} keys={rows.flatMap((order) => (order.id ? [order.id] : []))} select={select} />
      ) : null}
    </>
  );
}

// A word finds an order by its number, its channel's number, its account, or the channel it went through.
const matches = (order: Order, word: string) =>
  [order.id, order.trade_no, order.account_id, order.merchant_name, order.method].some((value) =>
    value?.toLowerCase().includes(word),
  );
