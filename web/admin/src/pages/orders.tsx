import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { LoadMore, useSession } from "@axiom/app";
import { Button, DataState, EmptyState, PageHeader, Panel, Table, Td, TextField, useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { orderAmounts } from "../orders/amounts";
import { orderTone, paymentOrderState, paymentOrderStates, refundTone, shortId } from "../orders/states";
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
import { accountQuery, merchantsQuery, paymentOrdersQuery, paymentRefundsQuery } from "../queries";

type Order = AdminSchemas["PaymentOrder"];
type Refund = AdminSchemas["PaymentRefund"];

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
  return (
    <>
      <RefundQueue siteId={siteId} />
      <OrdersList siteId={siteId} filters={filters} />
    </>
  );
}

type Decision = { kind: "approve" | "reject"; refund: Refund };

function RefundQueue({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const { t } = useTranslation();
  const session = useSession();
  const refunds = useInfiniteQuery(paymentRefundsQuery(siteId, "frozen"));
  const rows = refunds.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const [decision, setDecision] = useState<Decision | null>(null);
  return (
    <Panel title={t("orders.refund.title")}>
      <p className="px-4 pt-4 text-t2">{t("orders.refund.description")}</p>
      <DataState
        query={refunds}
        isEmpty={() => rows.length === 0}
        empty={<EmptyState title={t("orders.refund.emptyTitle")}>{t("orders.refund.emptyBody")}</EmptyState>}
      >
        {() => (
          <Table label={t("orders.refund.title")}>
            <Head
              cols={[
                [t("orders.refund.col.refund")],
                [t("orders.refund.col.account"), "hidden md:table-cell"],
                [t("orders.refund.col.credits"), "hidden sm:table-cell"],
                [t("orders.refund.col.amount"), "hidden sm:table-cell"],
                [t("orders.refund.col.reason"), "hidden lg:table-cell"],
                [t("orders.refund.col.state"), "hidden sm:table-cell"],
              ]}
              action
            />
            <tbody>
              {rows.map((refund, index) => (
                <RefundRow
                  key={refund.id ?? index}
                  refund={refund}
                  self={refund.requested_by !== undefined && refund.requested_by === session.user.id}
                  onDecide={(kind) => setDecision({ kind, refund })}
                />
              ))}
            </tbody>
          </Table>
        )}
      </DataState>
      <LoadMore list={refunds} />
      {decision ? <RefundDecision {...decision} onClose={() => setDecision(null)} /> : null}
    </Panel>
  );
}

// A refund frozen without an approver is the one waiting for review; the approval is a second person's, so the person
// who asked never sees the button here.
function RefundRow({
  refund,
  self,
  onDecide,
}: {
  refund: Refund;
  self: boolean;
  onDecide: (kind: Decision["kind"]) => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const waiting = refund.state === "frozen" && !refund.approved_by;
  const credits = refund.credits ? format.money(refund.credits) : "—";
  return (
    <tr className="align-top hover:bg-s2">
      <Td>
        <Code title={refund.id}>{shortId(refund.id)}</Code>
        <span className="block text-label text-t3" title={refund.created_at}>
          {refund.created_at ? format.dateTime(new Date(refund.created_at)) : "—"}
        </span>
        <span className="block text-label text-t3" title={refund.order_id}>
          {t("orders.refund.col.order")} {shortId(refund.order_id)}
        </span>
        {refund.via === "offline" ? (
          <span className="block text-label text-t3">{t("orders.refund.viaOffline")}</span>
        ) : null}
        <span className="mt-1 block text-label text-t3 sm:hidden">
          {refundStateText(t, refund)}
          {" · "}
          {credits}
        </span>
      </Td>
      <Td className="hidden md:table-cell">
        <Code title={refund.account_id}>{shortId(refund.account_id)}</Code>
      </Td>
      <Td className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">{credits}</Td>
      <Td className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">
        {refund.amount ? format.pay(refund.amount) : "—"}
      </Td>
      <Td className="hidden max-w-64 lg:table-cell">
        <span className="block truncate" title={refund.reason}>
          {refund.reason ?? "—"}
        </span>
        <span className="block text-label text-t3" title={refund.requested_by}>
          {refund.requested_by ?? ""}
        </span>
      </Td>
      <Td className="hidden sm:table-cell">
        <State tone={refundTone(refund)}>{refundStateText(t, refund)}</State>
        {refund.provider_ref ? <span className="mt-1 block text-label text-t3">{refund.provider_ref}</span> : null}
      </Td>
      <Td>
        {waiting ? (
          <div className="flex flex-col items-end gap-2 sm:flex-row sm:flex-wrap sm:justify-end">
            {self ? (
              <span className="text-label text-t3">{t("orders.refund.secondPerson")}</span>
            ) : (
              <Button size="sm" onClick={() => onDecide("approve")}>
                {t("orders.refund.approve")}
              </Button>
            )}
            <Button size="sm" variant="ghost" onClick={() => onDecide("reject")}>
              {t("orders.refund.reject")}
            </Button>
          </div>
        ) : null}
      </Td>
    </tr>
  );
}

function refundStateText(t: TFunction, refund: Refund): string {
  switch (refund.state) {
    case "frozen":
      return refund.approved_by ? t("orders.refund.state.approved") : t("orders.refund.state.pendingReview");
    case "sending":
      return t("orders.refund.state.sending");
    case "pending":
      return t("orders.refund.state.pending");
    case "succeeded":
      return t("orders.refund.state.succeeded");
    case "failed":
      return t("orders.refund.state.failed");
    default:
      return "—";
  }
}

function RefundDecision({ kind, refund, onClose }: Decision & { onClose: () => void }) {
  const { t } = useTranslation();
  const approve = kind === "approve";
  const offline = refund.via === "offline";
  const id = refund.id ?? "";
  return (
    <ChangeDialog
      title={t(approve ? "orders.refund.approveTitle" : "orders.refund.rejectTitle", { id: shortId(refund.id) })}
      operation={`${kind}-refund:${id}`}
      intro={
        <>
          <p>
            {t(
              approve
                ? offline
                  ? "orders.refund.approveIntroOffline"
                  : "orders.refund.approveIntro"
                : "orders.refund.rejectIntro",
            )}
          </p>
          <RefundFacts refund={refund} />
        </>
      }
      action={t(approve ? "orders.refund.approveAction" : "orders.refund.rejectAction")}
      danger={approve}
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST(
            approve
              ? "/api/admin/v1/payments/refunds/{refund_id}/approve"
              : "/api/admin/v1/payments/refunds/{refund_id}/reject",
            { params: { path: { refund_id: id }, header: { "Idempotency-Key": key } }, body: { reason } },
          ),
        )
      }
      done={() => (
        <p>
          {t(
            approve ? (offline ? "orders.refund.approvedOffline" : "orders.refund.approved") : "orders.refund.rejected",
          )}
        </p>
      )}
      onClose={onClose}
    />
  );
}

// RefundFacts keeps the decision's basis readable at every width: the queue hides its wider columns on a phone, so the
// confirmation has the account, cash, credits, reason and requester before the person approves or rejects.
function RefundFacts({ refund }: { refund: Refund }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <div className="space-y-2">
      <p className="text-label text-t3">{t("orders.refund.review")}</p>
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
        <dt className="text-label text-t3">{t("orders.refund.col.account")}</dt>
        <dd>
          <Code title={refund.account_id}>{refund.account_id ?? "—"}</Code>
        </dd>
        <dt className="text-label text-t3">{t("orders.refund.col.order")}</dt>
        <dd>
          <Code title={refund.order_id}>{refund.order_id ?? "—"}</Code>
        </dd>
        <dt className="text-label text-t3">{t("orders.refund.col.credits")}</dt>
        <dd>{refund.credits ? format.money(refund.credits) : "—"}</dd>
        <dt className="text-label text-t3">{t("orders.refund.col.amount")}</dt>
        <dd>{refund.amount ? format.pay(refund.amount) : "—"}</dd>
        <dt className="text-label text-t3">{t("orders.refund.col.reason")}</dt>
        <dd className="break-words">{refund.reason ?? "—"}</dd>
        <dt className="text-label text-t3">{t("orders.refund.col.requested")}</dt>
        <dd>
          <Code title={refund.requested_by}>{refund.requested_by ?? "—"}</Code>
        </dd>
      </dl>
    </div>
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
          [t("orders.col.credits")],
          [t("orders.col.total"), "hidden sm:table-cell"],
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

function Refunds({ refunds }: { refunds: Refund[] | undefined }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (refunds === undefined) {
    return <span className="text-label text-t3">{t("orders.refundsUnavailable")}</span>;
  }
  if (refunds.length === 0) {
    return <span className="text-label text-t3">{t("orders.noRefunds")}</span>;
  }
  return (
    <ul className="space-y-1">
      {refunds.map((refund) => (
        <li key={refund.id} className="flex flex-wrap items-center gap-x-2 gap-y-1 text-label">
          <State tone={refundTone(refund)}>{refundStateText(t, refund)}</State>
          <span className="tabular-nums">{refund.credits ? format.money(refund.credits) : "—"}</span>
          <span className="tabular-nums text-t3">{refund.amount ? format.pay(refund.amount) : "—"}</span>
        </li>
      ))}
    </ul>
  );
}
