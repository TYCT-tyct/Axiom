import type { AdminSchemas } from "@axiom/api";
import { LoadMore, useSession } from "@axiom/app";
import { Card, DataState, EmptyState, Field, Mono, PageHead, Table, Td, Text, useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { orderAmounts } from "../orders/amounts";
import { CashMovements } from "../orders/cash";
import { PaidBatches } from "../orders/paid-batches";
import { RefundQueue, Refunds } from "../orders/refunds";
import { orderTone, paymentOrderState, paymentOrderStates, shortId } from "../orders/states";
import { ChoiceFilter, Code, FilterBar, Head, PageBody, State, SubLine, text, toInstant, toLocalInput } from "../parts";
import { accountQuery, merchantsQuery, paymentOrdersQuery } from "../queries";

type Order = AdminSchemas["PaymentOrder"];

// The admin's orders and refunds page: the site's orders with each one's refunds, and the refunds frozen for a
// decision above them. The site is the admin account's, as on the settings page (M1 has no site list).
export default function Orders() {
  const { t } = useTranslation();
  const owner = useQuery(accountQuery(useSession().account));
  return (
    <>
      <PageHead title={t("nav.orders")} lede={t("orders.description")} />
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
  if (filters.paid === "batches") {
    return <PaidBatches siteId={siteId} since={filters.since} until={filters.until} />;
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
      <Card layout="flush">
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
      </Card>
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
      <Field
        label={t("orders.filters.account")}
        hint={t("orders.filters.accountHint")}
        name="account_id"
        defaultValue={filters.account_id}
        maxLength={64}
        autoComplete="off"
        spellCheck={false}
      />
      <Field
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
      <Field
        label={t("orders.filters.since")}
        name="since"
        type="datetime-local"
        defaultValue={toLocalInput(filters.since)}
      />
      <Field
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
          [t("orders.col.order"), { main: true }],
          t("orders.col.account"),
          t("orders.col.state"),
          [t("orders.col.credits"), { align: "end" }],
          [t("orders.col.total"), { align: "end" }],
          t("orders.col.refunds"),
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
    <tr>
      <Td main>
        <Code title={order.id}>{shortId(order.id)}</Code>
        <SubLine>{order.created_at ? format.dateTime(new Date(order.created_at)) : "—"}</SubLine>
        <SubLine>{[order.merchant_name, order.method].filter(Boolean).join(" · ")}</SubLine>
      </Td>
      <Td>
        <Code title={order.account_id}>{shortId(order.account_id)}</Code>
        <SubLine>
          <Mono title={order.user_id}>{shortId(order.user_id)}</Mono>
        </SubLine>
      </Td>
      <Td>
        {order.state ? <State tone={orderTone(order.state)}>{t(`orders.state.${order.state}`)}</State> : "—"}
        {order.trade_no ? <SubLine>{order.trade_no}</SubLine> : null}
      </Td>
      <Td align="end">
        {credited ? (
          format.money(credited)
        ) : (
          <Text as="span" size="sm" tone="faint">
            {t(creditedMissing ? "orders.creditedUnavailable" : "orders.notCredited")}
          </Text>
        )}
        {quotedCredits ? (
          <SubLine>
            {t("orders.quote")} {format.money(quotedCredits)}
          </SubLine>
        ) : null}
      </Td>
      <Td align="end">
        {paid ? (
          format.pay(paid)
        ) : (
          <Text as="span" size="sm" tone="faint">
            {t(paidMissing ? "orders.paidUnavailable" : "orders.notPaid")}
          </Text>
        )}
        {quote ? (
          <SubLine>
            {t("orders.quote")} {format.pay(quote)}
          </SubLine>
        ) : null}
        {paidFee ? (
          <SubLine>
            {t("orders.fee")} {format.pay(paidFee)}
          </SubLine>
        ) : null}
      </Td>
      <Td>
        <Refunds refunds={order.refunds} />
      </Td>
    </tr>
  );
}
