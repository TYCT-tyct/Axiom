import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import {
  type Attempt,
  accountKey,
  can,
  failed,
  LoadMore,
  opened,
  type Sendable,
  useErrors,
  useSession,
} from "@axiom/app";
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
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { clearAttempt, readAttempt, unanswered, writeAttempt } from "../attempt-store";
import { useOrderActions } from "../order-actions";
import { balanceQuery, ordersQuery } from "../queries";

// RefundRequest is what a refund asks for; the key and this body are sent together every time.
type RefundRequest = { credits: string; reason: string };

const refundStore = "axiom.console.refund.attempt";
const refundKey = (scope: string, order: string) => `${scope}/${order}/${refundStore}`;

function isRefundRequest(value: unknown): value is RefundRequest {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const { credits, reason } = value as { credits?: unknown; reason?: unknown };
  return typeof credits === "string" && typeof reason === "string";
}

// useRefunding is the refund dialog's state: which order it is for and the attempt it resumes. An attempt whose answer
// is unknown is kept when the dialog closes, so reopening it sends the same request again; one nothing was sent with
// is not, because the person closed it without asking.
function useRefunding(scope: string, onSent: () => void) {
  const [refunding, setRefunding] = useState<{ order: Schemas["Order"]; attempt: Attempt<RefundRequest> } | null>(null);
  const kept = (order: string) => readAttempt(refundKey(scope, order), isRefundRequest);
  const open = (order: Schemas["Order"]) =>
    setRefunding({ order, attempt: kept(order.id) ?? opened(crypto.randomUUID()) });
  const close = () => {
    if (refunding && kept(refunding.order.id)?.state === "editing") {
      clearAttempt(refundKey(scope, refunding.order.id));
    }
    setRefunding(null);
  };
  const keep = (next: Attempt<RefundRequest>) => {
    if (refunding) {
      writeAttempt(refundKey(scope, refunding.order.id), next);
    }
  };
  const sent = () => {
    if (refunding) {
      clearAttempt(refundKey(scope, refunding.order.id));
    }
    setRefunding(null);
    onSent();
  };
  return { refunding, open, close, keep, sent };
}

// Orders is the account's top-ups, newest first, with where each payment stands and what a refund of it is doing.
export default function Orders() {
  const { t } = useTranslation();
  const session = useSession();
  const list = useInfiniteQuery(ordersQuery);
  const orders = useMemo(() => list.data?.pages.flatMap((page) => page.data) ?? [], [list.data]);
  const [noticed, setNoticed] = useState(false);
  const allowed = can(session, "balance.top_up");
  const refundsKnown = orders.every((order) => order.refund_allowed !== undefined);
  const refund = useRefunding(`${session.user.id}/${session.account}/refund`, () => setNoticed(true));
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
              <>
                <OrdersTable orders={orders} allowed={allowed} refundsKnown={refundsKnown} onRefund={refund.open} />
                <LoadMore list={list} />
              </>
            )}
          </DataState>
        </Panel>
      </div>
      {refund.refunding ? (
        <RefundDialog
          order={refund.refunding.order}
          initial={refund.refunding.attempt}
          onAttempt={refund.keep}
          onClose={refund.close}
          onDone={refund.sent}
        />
      ) : null}
    </>
  );
}

// OrdersTable is one page of the account's orders: where each payment stands, what a refund of it is doing, and the
// note that older nodes do not yet say whether a refund may be asked for.
function OrdersTable({
  orders,
  allowed,
  refundsKnown,
  onRefund,
}: {
  orders: Schemas["Order"][];
  allowed: boolean;
  refundsKnown: boolean;
  onRefund: (order: Schemas["Order"]) => void;
}) {
  const { t } = useTranslation();
  return (
    <>
      <Table label={t("nav.orders")}>
        <thead>
          <tr>
            <Th>{t("orders.col.order")}</Th>
            <Th>{t("orders.col.state")}</Th>
            <Th className="hidden text-right sm:table-cell">{t("orders.col.credits")}</Th>
            <Th className="hidden text-right sm:table-cell">{t("orders.col.total")}</Th>
            <Th className="hidden md:table-cell">{t("orders.col.refunds")}</Th>
          </tr>
        </thead>
        <tbody>
          {orders.map((order) => (
            <OrderRow key={order.id} order={order} allowed={allowed} onRefund={() => onRefund(order)} />
          ))}
        </tbody>
      </Table>
      {refundsKnown ? null : <p className="px-4 py-3 text-label text-t3">{t("orders.refundsUnavailable")}</p>}
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
  return (
    <tr className="align-top hover:bg-s2">
      <Td>
        <span className="font-mono text-label text-t2" title={order.id}>
          {order.id.slice(-8)}
        </span>
        <span className="block text-label text-t3">{format.dateTime(new Date(order.created_at))}</span>
        <span className="block max-w-48 text-label text-t3 [overflow-wrap:anywhere]">
          {t("orders.method", { method: order.method })}
        </span>
        {/* Below sm the amount columns are hidden, so what was bought and paid reads under the order instead. */}
        <span className="mt-1 block text-ui text-t1 tabular-nums [overflow-wrap:anywhere] sm:hidden">
          {format.money(order.credits)}
          <span className="block text-label text-t3">{format.pay(order.total)}</span>
        </span>
      </Td>
      <Td>
        <OrderState order={order} allowed={allowed} onRefund={onRefund} />
      </Td>
      <Td className="hidden text-right whitespace-nowrap tabular-nums sm:table-cell">{format.money(order.credits)}</Td>
      <Td className="hidden text-right whitespace-nowrap tabular-nums sm:table-cell">
        {format.pay(order.total)}
        <span className="block text-label text-t3">{t("orders.fee", { fee: format.pay(order.fee) })}</span>
      </Td>
      <Td className="hidden md:table-cell">
        <RefundList refunds={order.refunds} />
      </Td>
    </tr>
  );
}

// OrderState is where the payment stands and what can be done about it; below md the refunds read here too, since
// their column is hidden.
function OrderState({ order, allowed, onRefund }: { order: Schemas["Order"]; allowed: boolean; onRefund: () => void }) {
  const { t } = useTranslation();
  const actions = useOrderActions(order.id);
  const failure = actions.verify.error ?? actions.cancel.error;
  const errors = useErrors();
  return (
    <>
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
        <div className="mt-2">
          <Button onClick={onRefund}>{t("orders.refund")}</Button>
        </div>
      ) : null}
      {order.refunds?.length ? (
        <div className="mt-2 md:hidden">
          <RefundList refunds={order.refunds} />
        </div>
      ) : null}
    </>
  );
}

// RefundDialog freezes unused credits and sends the request for an administrator's review. The request that went out is
// fixed: while its answer is unknown the fields are locked and only that request goes out again, also after the dialog
// was closed and opened again, or the page was reloaded. A refusal is the only answer that allows a different request.
function RefundDialog({
  order,
  initial,
  onAttempt,
  onClose,
  onDone,
}: {
  order: Schemas["Order"];
  initial: Attempt<RefundRequest>;
  onAttempt: (attempt: Attempt<RefundRequest>) => void;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const carried = initial.state === "unknown" ? initial.request : undefined;
  const [attempt, setAttempt] = useState(initial);
  const [credits, setCredits] = useState(carried ? moneyInput(carried.credits) : moneyInput(order.credits.amount));
  const [reason, setReason] = useState(carried?.reason ?? "");
  const parsed = parseMoney(credits);
  const editing = attempt.state === "editing";
  const valid = editing && parsed !== null && parsed !== "0" && reason.trim() !== "";
  const change = (next: Attempt<RefundRequest>) => {
    setAttempt(next);
    onAttempt(next);
  };
  const refund = useRefundRequest(order, change, onDone);
  return (
    <Dialog title={t("orders.refundTitle")} onClose={onClose}>
      <form
        className="space-y-3"
        onSubmit={(event) => {
          event.preventDefault();
          if (attempt.state === "unknown") {
            refund.mutate({ from: attempt, request: attempt.request });
          } else if (valid && parsed) {
            refund.mutate({ from: attempt, request: { credits: parsed, reason } });
          }
        }}
      >
        <p className="max-w-prose text-ui text-t2">{t("orders.refundBody")}</p>
        <RefundFields
          order={order}
          credits={credits}
          reason={reason}
          editing={editing}
          failure={refund.failure}
          onCredits={setCredits}
          onReason={setReason}
        />
        {attempt.state === "unknown" ? <p className="text-label text-t3">{t("orders.refundUnknown")}</p> : null}
        {attempt.state === "reused" ? <p className="text-label text-t3">{t("orders.refundReused")}</p> : null}
        <RefundActions attempt={attempt} editing={editing} pending={refund.pending} onClose={onClose} />
      </form>
    </Dialog>
  );
}

// useRefundRequest sends one refund request and settles the order list and the balance when the platform took it. The
// dialog shows a failure and keeps the request, so only that request can go out again, under the same key.
function useRefundRequest(order: Schemas["Order"], change: (next: Attempt<RefundRequest>) => void, onDone: () => void) {
  const client = useQueryClient();
  const [failure, setFailure] = useState<unknown>(null);
  const refund = useMutation({
    // Recorded before it is sent: if no answer comes back, reopening the dialog sends this request again, never another.
    onMutate: ({ from, request }: { from: Sendable<RefundRequest>; request: RefundRequest }) =>
      change(unanswered(from, request)),
    mutationFn: ({ from, request }: { from: Sendable<RefundRequest>; request: RefundRequest }) =>
      unwrap(
        consoleApi.POST("/api/console/v1/orders/{order_id}/refunds", {
          params: { path: { order_id: order.id }, header: { "Idempotency-Key": from.key } },
          body: { credits: { amount: request.credits, currency: order.credits.currency }, reason: request.reason },
        }),
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: [accountKey, "orders"] });
      void client.invalidateQueries({ queryKey: balanceQuery.queryKey });
      onDone();
    },
    onError: (error, sent) => {
      setFailure(error);
      change(failed(sent.from, sent.request, error, () => crypto.randomUUID()));
    },
  });
  return { failure, pending: refund.isPending, mutate: refund.mutate };
}

// RefundFields is the amount to refund and why; both are locked while an answer is unknown.
function RefundFields({
  order,
  credits,
  reason,
  editing,
  failure,
  onCredits,
  onReason,
}: {
  order: Schemas["Order"];
  credits: string;
  reason: string;
  editing: boolean;
  failure: unknown;
  onCredits: (credits: string) => void;
  onReason: (reason: string) => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextField
        label={t("orders.refundCredits")}
        hint={t("orders.refundCreditsHint", { currency: order.credits.currency })}
        inputMode="decimal"
        required
        disabled={!editing}
        value={credits}
        onChange={(event) => onCredits(event.target.value)}
        error={errors.field(failure, "credits")}
      />
      <TextAreaField
        label={t("orders.refundReason")}
        hint={t("orders.refundReasonHint")}
        maxLength={1000}
        required
        disabled={!editing}
        value={reason}
        onChange={(event) => onReason(event.target.value)}
      />
      {failure ? <FormError>{errors.message(failure)}</FormError> : null}
    </>
  );
}

// RefundActions closes the dialog or sends the request; one that was refused can be edited and sent again, but one
// whose answer is unknown only goes out as it was.
function RefundActions({
  attempt,
  editing,
  pending,
  onClose,
}: {
  attempt: Attempt<RefundRequest>;
  editing: boolean;
  pending: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap justify-end gap-2">
      <Button variant="ghost" onClick={onClose}>
        {t("orders.close")}
      </Button>
      {editing || attempt.state === "unknown" ? (
        <SubmitButton pending={pending}>
          {attempt.state === "unknown" ? t("orders.refundRetry") : t("orders.refundSubmit")}
        </SubmitButton>
      ) : null}
    </div>
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
