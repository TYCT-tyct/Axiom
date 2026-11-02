import type { AdminSchemas } from "@axiom/api";
import { LoadMore, useSession } from "@axiom/app";
import { DataState, EmptyState, PageHeader, Panel, Table, Td, TextField, useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { orderAmounts } from "../orders/amounts";
import { CashMovements } from "../orders/cash";
import { RefundQueue, Refunds } from "../orders/refunds";
import { orderTone, paymentOrderState, paymentOrderStates, shortId } from "../orders/states";
import {
  ChoiceFilter,
  Code,
  FilterBar,
  Head,
  mainCell,
  PageBody,
  State,
  text,
  toInstant,
  toLocalInput,
} from "../parts";
import { accountQuery, merchantsQuery, paymentOrdersQuery } from "../queries";

type Order = AdminSchemas["PaymentOrder"];

// The admin's orders and refunds page: the site's orders with each one's refunds, and the refunds frozen for a
// decision above them. The site is the admin account's, as on the settings page (M1 has no site list).
export default function Orders() {
  const { t } = useTranslation();
  const owner = useQuery(accountQuery(useSession().account));
  return (
    <>
      <PageHeader title={t("nav.orders")} description={t("orders.description")} />
      <PageBody>
        <DataState query={owner}>{(account) => <Current siteId={account.site_id} />}</DataState>
      </PageBody>
    </>
  );
}

function Current({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const filters = useSearch({ from: "/app/orders" });
  if (filters.cash === "1") {
    return <CashMovements siteId={siteId} since={filters.since} until={filters.until} />;
  }
  return (
    <>
      <RefundQueue siteId={siteId} />
      <OrdersList siteId={siteId} filters={filters} />
    </>
  );
}

type FiltersValue = {
  account_id?: string;
  user_id?: string;
  state?: AdminSchemas["PaymentOrderState"];
  merchant_id?: string;
  since?: string;
  until?: string;
};

function OrdersList({ siteId, filters }: { siteId: AdminSchemas["SiteId"]; filters: FiltersValue }) {
  const { t } = useTranslation();
  const orders = useInfiniteQuery(paymentOrdersQuery(siteId, filters));
  const rows = orders.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const narrowed = Object.values(filters).some(Boolean);
  return (
    <>
      <Filters siteId={siteId} filters={filters} />
      <Panel>
        <DataState
          query={orders}
          isEmpty={() => rows.length === 0}
          empty={
            <EmptyState title={narrowed ? t("orders.noMatch.title") : t("orders.empty.title")}>
              {narrowed ? t("orders.noMatch.body") : t("orders.empty.body")}
            </EmptyState>
          }
        >
          {() => <OrdersTable rows={rows} />}
        </DataState>
        <LoadMore list={orders} />
      </Panel>
    </>
  );
}

function Filters({ siteId, filters }: { siteId: AdminSchemas["SiteId"]; filters: FiltersValue }) {
  const navigate = useNavigate();
  const merchants = useQuery(merchantsQuery(siteId));
  const channels = (merchants.data?.data ?? []).flatMap((merchant) =>
    merchant.id && merchant.name ? [{ value: merchant.id, label: merchant.name }] : [],
  );
  return (
    <FilterBar
      values={filters}
      onApply={(values) =>
        navigate({
          to: "/orders",
          search: {
            account_id: text(values.account_id),
            user_id: text(values.user_id),
            state: paymentOrderState(values.state),
            merchant_id: text(values.merchant_id),
            since: toInstant(values.since),
            until: toInstant(values.until),
          },
        })
      }
    >
      <FilterFields filters={filters} channels={channels} />
    </FilterBar>
  );
}

function FilterFields({ filters, channels }: { filters: FiltersValue; channels: { value: string; label: string }[] }) {
  const { t } = useTranslation();
  return (
    <>
      <TextField
        label={t("orders.filters.account")}
        hint={t("orders.filters.accountHint")}
        name="account_id"
        defaultValue={filters.account_id}
        maxLength={64}
        autoComplete="off"
        spellCheck={false}
      />
      <TextField
        label={t("orders.filters.user")}
        hint={t("orders.filters.userHint")}
        name="user_id"
        defaultValue={filters.user_id}
        maxLength={64}
        autoComplete="off"
        spellCheck={false}
      />
      <ChoiceFilter
        label={t("orders.filters.state")}
        name="state"
        value={filters.state}
        any={t("orders.filters.stateAny")}
        options={paymentOrderStates.map((state) => ({ value: state, label: t(`orders.state.${state}`) }))}
      />
      <ChoiceFilter
        label={t("orders.filters.channel")}
        name="merchant_id"
        value={filters.merchant_id}
        any={t("orders.filters.channelAny")}
        options={channels}
      />
      <TextField
        label={t("orders.filters.since")}
        name="since"
        type="datetime-local"
        defaultValue={toLocalInput(filters.since)}
      />
      <TextField
        label={t("orders.filters.until")}
        name="until"
        type="datetime-local"
        defaultValue={toLocalInput(filters.until)}
      />
    </>
  );
}

function OrdersTable({ rows }: { rows: Order[] }) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.orders")}>
      <Head
        cols={[
          [t("orders.col.order")],
          [t("orders.col.account"), "hidden md:table-cell"],
          [t("orders.col.state")],
          [t("orders.col.credits"), "text-right"],
          [t("orders.col.total"), "hidden text-right sm:table-cell"],
          [t("orders.col.refunds"), "hidden lg:table-cell"],
        ]}
      />
      <tbody>
        {rows.map((order, index) => (
          <OrderRow key={order.id ?? index} order={order} />
        ))}
      </tbody>
    </Table>
  );
}

function OrderRow({ order }: { order: Order }) {
  const { t } = useTranslation();
  const format = useFormat();
  const { paid, paidFee, quote, credited, quotedCredits, paidMissing, creditedMissing } = orderAmounts(order);
  return (
    <tr className="align-top hover:bg-s2">
      <Td className={mainCell}>
        <Code title={order.id}>{shortId(order.id)}</Code>
        <span className="block text-label text-t3" title={order.created_at}>
          {order.created_at ? format.dateTime(new Date(order.created_at)) : "—"}
        </span>
        <span className="block text-label text-t3">
          {[order.merchant_name, order.method].filter(Boolean).join(" · ")}
        </span>
      </Td>
      <Td className="hidden md:table-cell">
        <Code title={order.account_id}>{shortId(order.account_id)}</Code>
        <span className="block text-label text-t3" title={order.user_id}>
          {shortId(order.user_id)}
        </span>
      </Td>
      <Td>
        {order.state ? <State tone={orderTone(order.state)}>{t(`orders.state.${order.state}`)}</State> : "—"}
        {order.trade_no ? <span className="mt-1 block text-label text-t3">{order.trade_no}</span> : null}
      </Td>
      <Td className="whitespace-nowrap text-right tabular-nums">
        {credited ? (
          format.money(credited)
        ) : (
          <span className="text-label text-t3">
            {t(creditedMissing ? "orders.creditedUnavailable" : "orders.notCredited")}
          </span>
        )}
        {quotedCredits ? (
          <span className="block text-label text-t3">
            {t("orders.quote")} {format.money(quotedCredits)}
          </span>
        ) : null}
      </Td>
      <Td className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">
        {paid ? (
          format.pay(paid)
        ) : (
          <span className="text-label text-t3">{t(paidMissing ? "orders.paidUnavailable" : "orders.notPaid")}</span>
        )}
        {quote ? (
          <span className="block text-label text-t3">
            {t("orders.quote")} {format.pay(quote)}
          </span>
        ) : null}
        {paidFee ? (
          <span className="block text-label text-t3">
            {t("orders.fee")} {format.pay(paidFee)}
          </span>
        ) : null}
      </Td>
      <Td className="hidden max-w-72 lg:table-cell">
        <Refunds refunds={order.refunds} />
      </Td>
    </tr>
  );
}
