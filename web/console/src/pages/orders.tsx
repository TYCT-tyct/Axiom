import { ApiError, type Schemas } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import {
  Button,
  Card,
  DataState,
  DataTable,
  EmptyState,
  IdText,
  Note,
  Stack,
  useFormat,
  usePageTitle,
} from "@axiom/ui";
import { type UseQueryResult, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useState } from "react";
import { useTranslation } from "react-i18next";
import { balanceQuery, ordersQuery, siteQuery } from "../queries";
import { receiptState } from "../receipt";
import { useBoardKind } from "./bill-data";
import { BillShell } from "./bill-shell";
import { useBillTopUpSession } from "./bill-topup-state";
import { RefundDialog, useRefundController } from "./orders-refund";
import { type Order, OrderState, RefundList } from "./orders-state";

const LazyBillTopUpModal = lazy(() => import("./bill-topup").then((m) => ({ default: m.BillTopUpModal })));

type OrderLabels = {
  order: string;
  state: string;
  credits: string;
  total: string;
  refunds: string;
  method: (m: string) => string;
  fee: (f: string) => string;
};

type OrderColumnArgs = {
  allowed: boolean;
  onOpenRefund: (order: Order) => void;
  format: ReturnType<typeof useFormat>;
  labels: OrderLabels;
};

function shortMethod(method: string) {
  return method.length > 14 ? `${method.slice(0, 12)}…` : method;
}

function OrderPaySummary({
  order,
  format,
  feeLabel,
}: {
  order: Order;
  format: ReturnType<typeof useFormat>;
  feeLabel: (fee: string) => string;
}) {
  return (
    <Stack gap={4}>
      <span>{format.pay(order.paid_total ?? order.total)}</span>
      <span>{feeLabel(format.pay(order.fee))}</span>
    </Stack>
  );
}

function buildPhoneOrderColumns({ allowed, onOpenRefund, format, labels }: OrderColumnArgs) {
  return [
    {
      key: "order",
      header: labels.order,
      width: 164,
      cell: (order: Order) => (
        <Stack gap={4}>
          <span>{order.id.slice(-8)}</span>
          <span>{format.dateTimeMinute(new Date(order.created_at))}</span>
          <span title={order.method}>{labels.method(shortMethod(order.method))}</span>
          <strong>{format.money(order.credits)}</strong>
          <OrderPaySummary order={order} format={format} feeLabel={labels.fee} />
        </Stack>
      ),
    },
    {
      key: "state",
      header: labels.state,
      cell: (order: Order) => (
        <Stack gap={8}>
          <OrderState order={order} allowed={allowed} onRefund={onOpenRefund} />
          <RefundList refunds={order.refunds} />
        </Stack>
      ),
    },
  ];
}

function buildOrderColumns({ allowed, onOpenRefund, format, labels }: OrderColumnArgs) {
  return [
    {
      key: "order",
      header: labels.order,
      cell: (order: Order) => (
        <Stack gap={4}>
          <span>{format.dateTimeMinute(new Date(order.created_at))}</span>
          <span>{labels.method(order.method)}</span>
          <IdText value={order.id} />
        </Stack>
      ),
    },
    {
      key: "state",
      header: labels.state,
      cell: (order: Order) => <OrderState order={order} allowed={allowed} onRefund={onOpenRefund} />,
    },
    {
      key: "credits",
      header: labels.credits,
      align: "end" as const,
      cell: (order: Order) => <strong>{format.money(order.credits)}</strong>,
    },
    {
      key: "total",
      header: labels.total,
      align: "end" as const,
      cell: (order: Order) => <OrderPaySummary order={order} format={format} feeLabel={labels.fee} />,
    },
    {
      key: "refunds",
      header: labels.refunds,
      cell: (order: Order) => <RefundList refunds={order.refunds} />,
    },
  ];
}

function OrdersList({
  orders,
  hasNextPage,
  fetchingNext,
  allowed,
  onFetchNext,
  onOpenTopUp,
  onOpenRefund,
}: {
  orders: readonly Order[];
  hasNextPage: boolean;
  fetchingNext: boolean;
  allowed: boolean;
  onFetchNext: () => void;
  onOpenTopUp: () => void;
  onOpenRefund: (order: Order) => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const isPhone = useBoardKind() === "phone";
  const receiptsKnown = orders.every((order) => receiptState(order).kind !== "unavailable");
  const refundsKnown = orders.every((order) => order.refund_allowed !== undefined);
  const labels: OrderLabels = {
    order: t("orders.col.order"),
    state: t("orders.col.state"),
    credits: t("orders.col.credits"),
    total: t("orders.col.total"),
    refunds: t("orders.col.refunds"),
    method: (method) => t("orders.method", { method }),
    fee: (fee) => t("orders.fee", { fee }),
  };
  const columnArgs = { allowed, onOpenRefund, format, labels };
  const columns = isPhone ? buildPhoneOrderColumns(columnArgs) : buildOrderColumns(columnArgs);

  if (orders.length === 0) {
    return (
      <Card title={t("orders.title")} lede={t("orders.description")}>
        <EmptyState
          title={t("orders.empty.title")}
          action={
            allowed ? (
              <Button variant="primary" onClick={onOpenTopUp}>
                {t("orders.empty.topUp")}
              </Button>
            ) : undefined
          }
        >
          {t("orders.empty.body")}
        </EmptyState>
      </Card>
    );
  }

  return (
    <Stack gap={12}>
      <Card
        title={t("orders.title")}
        actions={
          allowed ? (
            <Button variant="primary" size="sm" onClick={onOpenTopUp}>
              {t("topup.title")}
            </Button>
          ) : undefined
        }
        layout="flush"
      >
        <DataTable<Order>
          label={t("orders.title")}
          rows={orders}
          rowKey={(order) => order.id}
          columns={columns}
          fixed={isPhone}
        />
      </Card>
      {!receiptsKnown ? <Note title={t("orders.receiptsUnavailable")} /> : null}
      {!refundsKnown ? <Note title={t("orders.refundsUnavailable")} /> : null}
      <LoadMore list={{ hasNextPage, isFetchingNextPage: fetchingNext, fetchNextPage: onFetchNext }} />
    </Stack>
  );
}

function OrdersTopUpModal({
  open,
  currency,
  currencyQuery,
  currentBalance,
  session,
  onClose,
}: {
  open: boolean;
  currency: string | undefined;
  currencyQuery: UseQueryResult<unknown, unknown>;
  currentBalance: Schemas["Money"] | undefined;
  session: ReturnType<typeof useBillTopUpSession>;
  onClose: () => void;
}) {
  if (!open) return null;
  return (
    <Suspense fallback={null}>
      <LazyBillTopUpModal
        open={open}
        currency={currency}
        currencyQuery={currencyQuery}
        currentBalance={currentBalance}
        session={session}
        onClose={onClose}
      />
    </Suspense>
  );
}

function RefundSentBanner({ onDismiss }: { onDismiss: () => void }) {
  const { t } = useTranslation();
  return (
    <Note
      title={t("orders.refundSent")}
      action={
        <Button size="sm" variant="ghost" onClick={onDismiss}>
          {t("orders.dismiss")}
        </Button>
      }
    />
  );
}

export default function OrdersPage() {
  const { t } = useTranslation();
  const session = useSession();
  const allowed = can(session, "balance.top_up");
  const query = useInfiniteQuery(ordersQuery);
  const balance = useQuery(balanceQuery);
  const site = useQuery(siteQuery);
  const scope = session.account ? `${session.user.id}/${session.account}` : session.user.id;
  const [topUpOpen, setTopUpOpen] = useState(false);
  const [sentNotice, setSentNotice] = useState(false);
  const refund = useRefundController(scope, () => setSentNotice(true));
  const topUpSession = useBillTopUpSession(allowed);
  usePageTitle(t("orders.title"));

  const currency = balance.data?.balance.currency ?? site.data?.currency;
  const currencyQuery = !site.data && site.isPending ? site : !balance.data && balance.isPending ? balance : site;
  const currentBalance = balance.data?.balance;
  const unavailable = query.error instanceof ApiError && query.error.status === 404;

  return (
    <BillShell activeTab="orders" title={t("orders.title")} subtitle={t("orders.description")}>
      {sentNotice ? <RefundSentBanner onDismiss={() => setSentNotice(false)} /> : null}
      {unavailable ? (
        <Note title={t("orders.unavailable")} />
      ) : (
        <DataState query={query}>
          {(pages) => (
            <OrdersList
              orders={pages.pages.flatMap((page) => page.data)}
              hasNextPage={query.hasNextPage}
              fetchingNext={query.isFetchingNextPage}
              allowed={allowed}
              onFetchNext={() => void query.fetchNextPage()}
              onOpenTopUp={() => setTopUpOpen(true)}
              onOpenRefund={refund.open}
            />
          )}
        </DataState>
      )}
      {refund.active ? (
        <RefundDialog
          key={refund.active.order.id}
          order={refund.active.order}
          attempt={refund.active.attempt}
          sentRequest={refund.active.sentRequest}
          failure={refund.active.failure}
          pending={refund.active.pending}
          onSubmit={refund.submit}
          onReset={refund.reset}
          onClose={refund.close}
        />
      ) : null}
      <OrdersTopUpModal
        open={topUpOpen}
        currency={currency}
        currencyQuery={currencyQuery}
        currentBalance={currentBalance}
        session={topUpSession}
        onClose={() => setTopUpOpen(false)}
      />
    </BillShell>
  );
}
