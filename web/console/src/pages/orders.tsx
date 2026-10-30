import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey, can, LoadMore, useErrors, useSession } from "@axiom/app";
import {
  Badge,
  Button,
  DataState,
  Dialog,
  EmptyState,
  FormError,
  moneyInput,
  Notice,
  PageHeader,
  Panel,
  parseMoney,
  SubmitButton,
  Table,
  Td,
  TextAreaField,
  TextField,
  Th,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOrderActions } from "../order-actions";
import { balanceQuery, ordersQuery } from "../queries";

// Orders is the account's top-ups, newest first, with where each payment stands and what a refund of it is doing.
export default function Orders() {
  const { t } = useTranslation();
  const session = useSession();
  const list = useInfiniteQuery(ordersQuery);
  const orders = useMemo(() => list.data?.pages.flatMap((page) => page.data) ?? [], [list.data]);
  const [refunding, setRefunding] = useState<Schemas["Order"] | null>(null);
  const [noticed, setNoticed] = useState(false);
  const allowed = can(session, "balance.top_up");
  const refundsKnown = orders.every((order) => order.refund_allowed !== undefined);
  return (
    <>
      <PageHeader title={t("nav.orders")} description={t("orders.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        {noticed ? (
          <Notice action={<Button onClick={() => setNoticed(false)}>{t("orders.dismiss")}</Button>}>
            {t("orders.refundSent")}
          </Notice>
        ) : null}
        <Panel title={t("orders.title")}>
          <DataState
            query={list}
            isEmpty={() => orders.length === 0}
            empty={
              <EmptyState title={t("orders.empty.title")}>
                {t("orders.empty.body")}
                <p className="mt-2">
                  <Link to="/balance" className="text-accent underline-offset-4 hover:underline">
                    {t("orders.empty.topUp")}
                  </Link>
                </p>
              </EmptyState>
            }
          >
            {() => (
              <Table label={t("nav.orders")}>
                <thead>
                  <tr>
                    <Th>{t("orders.col.order")}</Th>
                    <Th>{t("orders.col.state")}</Th>
                    <Th className="text-right">{t("orders.col.credits")}</Th>
                    <Th className="text-right">{t("orders.col.total")}</Th>
                    <Th>{t("orders.col.refunds")}</Th>
                  </tr>
                </thead>
                <tbody>
                  {orders.map((order) => (
                    <OrderRow key={order.id} order={order} allowed={allowed} onRefund={() => setRefunding(order)} />
                  ))}
                </tbody>
              </Table>
            )}
          </DataState>
          {!refundsKnown ? <p className="px-4 py-3 text-label text-t3">{t("orders.refundsUnavailable")}</p> : null}
          <LoadMore list={list} />
        </Panel>
      </div>
      {refunding ? (
        <RefundDialog
          order={refunding}
          onClose={() => setRefunding(null)}
          onDone={() => {
            setRefunding(null);
            setNoticed(true);
          }}
        />
      ) : null}
    </>
  );
}

// A refund is in flight while it waits for review or is with the channel; only then is another request refused.
function inFlight(order: Schemas["Order"]) {
  return (order.refunds ?? []).some((refund) => refund.state === "pending_review" || refund.state === "processing");
}

function refundable(order: Schemas["Order"]) {
  return (
    order.refund_allowed === true &&
    (order.state === "paid" || order.state === "partially_refunded") &&
    !inFlight(order)
  );
}

function OrderRow({ order, allowed, onRefund }: { order: Schemas["Order"]; allowed: boolean; onRefund: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const actions = useOrderActions(order.id);
  const failure = actions.verify.error ?? actions.cancel.error;
  const errors = useErrors();
  return (
    <tr className="align-top hover:bg-s2">
      <Td>
        <span className="font-mono text-label text-t2" title={order.id}>
          {order.id.slice(-8)}
        </span>
        <span className="block text-label text-t3">{format.dateTime(new Date(order.created_at))}</span>
        <span className="block text-label text-t3">{t("orders.method", { method: order.method })}</span>
      </Td>
      <Td>
        <StateBadge state={order.state} />
        {failure ? <p className="mt-1 text-label text-bad">{errors.message(failure)}</p> : null}
        {order.state === "pending" ? (
          <div className="mt-2 flex flex-wrap gap-1.5">
            {order.checkout?.pay_url ? (
              <Button asChild>
                <a href={order.checkout.pay_url} rel="noreferrer">
                  {t("orders.pay")}
                </a>
              </Button>
            ) : null}
            <Button disabled={actions.verify.isPending} onClick={() => actions.verify.mutate()}>
              {t("orders.verify")}
            </Button>
            <Button variant="ghost" disabled={actions.cancel.isPending} onClick={() => actions.cancel.mutate()}>
              {t("orders.cancelOrder")}
            </Button>
          </div>
        ) : null}
        {allowed && refundable(order) ? (
          <Button className="mt-2" onClick={onRefund}>
            {t("orders.refund")}
          </Button>
        ) : null}
      </Td>
      <Td className="text-right whitespace-nowrap tabular-nums">{format.money(order.credits)}</Td>
      <Td className="text-right whitespace-nowrap tabular-nums">
        {format.pay(order.total)}
        <span className="block text-label text-t3">{t("orders.fee", { fee: format.pay(order.fee) })}</span>
      </Td>
      <Td>
        <RefundList refunds={order.refunds} />
      </Td>
    </tr>
  );
}

// RefundDialog freezes unused credits and sends the request for an administrator's review. The key belongs to the
// dialog's one intended request, so a lost answer can be asked for again without freezing twice.
function RefundDialog({
  order,
  onClose,
  onDone,
}: {
  order: Schemas["Order"];
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const client = useQueryClient();
  const [credits, setCredits] = useState(moneyInput(order.credits.amount));
  const [reason, setReason] = useState("");
  const [failure, setFailure] = useState<unknown>(null);
  const key = useRef("");
  if (key.current === "") {
    key.current = crypto.randomUUID();
  }
  const parsed = parseMoney(credits);
  const valid = parsed !== null && parsed !== "0" && reason.trim() !== "";
  const send = useMutation({
    mutationFn: () =>
      unwrap(
        consoleApi.POST("/api/console/v1/orders/{order_id}/refunds", {
          params: { path: { order_id: order.id }, header: { "Idempotency-Key": key.current } },
          body: { credits: { amount: parsed ?? "0", currency: order.credits.currency }, reason },
        }),
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: [accountKey, "orders"] });
      void client.invalidateQueries({ queryKey: balanceQuery.queryKey });
      onDone();
    },
    onError: (error) => {
      setFailure(error);
      if (error instanceof ApiError && error.code === "idempotency_key_reused") {
        key.current = crypto.randomUUID();
      }
    },
  });
  return (
    <Dialog title={t("orders.refundTitle")} onClose={onClose}>
      <form
        className="space-y-3"
        onSubmit={(event) => {
          event.preventDefault();
          if (valid) {
            send.mutate();
          }
        }}
      >
        <p className="max-w-prose text-ui text-t2">{t("orders.refundBody")}</p>
        <TextField
          label={t("orders.refundCredits")}
          hint={t("orders.refundCreditsHint", { currency: order.credits.currency })}
          inputMode="decimal"
          required
          value={credits}
          onChange={(event) => setCredits(event.target.value)}
          error={errors.field(failure, "credits")}
        />
        <TextAreaField
          label={t("orders.refundReason")}
          hint={t("orders.refundReasonHint")}
          maxLength={1000}
          required
          value={reason}
          onChange={(event) => setReason(event.target.value)}
        />
        {failure ? <FormError>{errors.message(failure)}</FormError> : null}
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>
            {t("orders.close")}
          </Button>
          <SubmitButton pending={send.isPending}>{t("orders.refundSubmit")}</SubmitButton>
        </div>
      </form>
    </Dialog>
  );
}

function StateBadge({ state }: { state: Schemas["OrderState"] }) {
  const { t } = useTranslation();
  const tone = state === "paid" ? "ok" : state === "pending" || state === "review" ? "warn" : "neutral";
  return (
    <Badge
      tone={state === "failed" || state === "charged_back" ? "bad" : tone}
      icon={state === "paid" ? "check" : state === "pending" ? "refresh" : "alert"}
    >
      {t(`orders.state.${state}`)}
    </Badge>
  );
}

function RefundList({ refunds }: { refunds: Schemas["Order"]["refunds"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (refunds === undefined) {
    return <span className="text-t3">{t("orders.unavailable")}</span>;
  }
  if (refunds.length === 0) {
    return <span className="text-t3">{t("orders.noRefunds")}</span>;
  }
  return (
    <ul className="space-y-1">
      {refunds.map((refund) => (
        <li key={refund.id}>
          <span className="text-t2">{t(`orders.refundState.${refund.state}`)}</span>
          <span className="block text-label text-t3 tabular-nums">{format.money(refund.credits)}</span>
          <span className="block text-label text-t3 tabular-nums">{format.pay(refund.amount)}</span>
        </li>
      ))}
    </ul>
  );
}
