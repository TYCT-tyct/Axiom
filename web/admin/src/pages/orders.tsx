import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { LoadMore, useSession } from "@axiom/app";
import {
  Button,
  DataState,
  EmptyState,
  PageHeader,
  Panel,
  Table,
  Td,
  TextField,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { orderTone, paymentOrderStates, refundTone, shortId } from "../orders/states";
import { ChoiceFilter, Code, FilterBar, Head, mainCell, PageBody, State, text, toInstant, toLocalInput } from "../parts";
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
        empty={
          <EmptyState title={t("orders.refund.emptyTitle")}>{t("orders.refund.emptyBody")}</EmptyState>
        }
      >
        {() => (
          <Table label={t("orders.refund.title")}>
            <Head
              cols={[
                [t("orders.refund.col.refund")],
                [t("orders.refund.col.account"), "hidden md:table-cell"],
                [t("orders.refund.col.credits")],
                [t("orders.refund.col.amount"), "hidden sm:table-cell"],
                [t("orders.refund.col.reason"), "hidden lg:table-cell"],
                [t("orders.refund.col.state")],
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
      </Td>
      <Td className="hidden md:table-cell">
        <Code title={refund.account_id}>{shortId(refund.account_id)}</Code>
      </Td>
      <Td className="whitespace-nowrap text-right tabular-nums">
        {refund.credits ? format.money(refund.credits) : "—"}
      </Td>
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
      <Td>
        <State tone={refundTone(refund)}>{refundStateText(t, refund)}</State>
        {refund.provider_ref ? <span className="mt-1 block text-label text-t3">{refund.provider_ref}</span> : null}
      </Td>
      <Td className="whitespace-nowrap">
        {waiting ? (
          <div className="flex flex-wrap justify-end gap-2">
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
        <p>
          {t(
            approve
              ? offline
                ? "orders.refund.approveIntroOffline"
                : "orders.refund.approveIntro"
              : "orders.refund.rejectIntro",
          )}
        </p>
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
        <p>{t(approve ? (offline ? "orders.refund.approvedOffline" : "orders.refund.approved") : "orders.refund.rejected")}</p>
      )}
      onClose={onClose}
    />
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
  const { t } = useTranslation();
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
            state: paymentOrderStates.find((state) => state === values.state),
            merchant_id: text(values.merchant_id),
            since: toInstant(values.since),
            until: toInstant(values.until),
          },
        })
      }
    >
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
    </FilterBar>
  );
}

function OrdersTable({ rows }: { rows: Order[] }) {
  const { t } = useTranslation();
  const format = useFormat();
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
          <tr key={order.id ?? index} className="align-top hover:bg-s2">
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
              {order.state ? (
                <State tone={orderTone(order.state)}>{t(`orders.state.${order.state}`)}</State>
              ) : (
                "—"
              )}
              {order.trade_no ? <span className="mt-1 block text-label text-t3">{order.trade_no}</span> : null}
            </Td>
            <Td className="whitespace-nowrap text-right tabular-nums">
              {order.credits ? format.money(order.credits) : "—"}
              {order.credited ? (
                <span className="block text-label text-t3">{format.money(order.credited)}</span>
              ) : null}
            </Td>
            <Td className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">
              {order.total ? format.pay(order.total) : "—"}
              {order.fee ? <span className="block text-label text-t3">{format.pay(order.fee)}</span> : null}
            </Td>
            <Td className="hidden max-w-72 lg:table-cell">
              <Refunds refunds={order.refunds} />
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
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
