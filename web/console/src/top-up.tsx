import { ApiError, consoleApi, type Schemas, unwrapWithStatus } from "@axiom/api";
import { type Attempt, can, failed, opened, QrCode, type Sendable, useErrors, useSession } from "@axiom/app";
import {
  Badge,
  Button,
  CopyButton,
  DataState,
  FormError,
  Icon,
  moneyInput,
  Panel,
  parseMoney,
  SelectField,
  SubmitButton,
  TextField,
  useFormat,
} from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOrderActions } from "./order-actions";
import { mergeOrder } from "./order-merge";
import { balanceQuery, orderQuery, ordersQuery, paymentMethodsQuery } from "./queries";

// The address the order is kept at across the channel's return page. It names the order, not its checkout: the channel's
// client secret is never stored and is shown only in the answer that made the order.
const stored = "axiom.console.top-up";

function readStored() {
  try {
    return sessionStorage.getItem(stored);
  } catch {
    return null;
  }
}

function writeStored(id: string) {
  try {
    sessionStorage.setItem(stored, id);
  } catch {
    // The order still shows on this page; only a return from the channel loses it.
  }
}

function clearStored() {
  try {
    sessionStorage.removeItem(stored);
  } catch {
    // nothing to clear
  }
}

// TopUp is the balance page's top-up entry: a role that may not buy credit sees why, and everyone else gets the flow.
export function TopUp({ currency }: { currency: string }) {
  const { t } = useTranslation();
  const session = useSession();
  if (!can(session, "balance.top_up")) {
    return (
      <Panel title={t("topup.title")}>
        <p className="px-4 py-3 text-t3">{t("topup.forbidden")}</p>
      </Panel>
    );
  }
  return <TopUpFlow currency={currency} />;
}

// balanceMoved says whether a state the poll just saw means the balance or the order list may have changed since they
// were read. A first read of a paid or refunded order counts: that payment may have landed after the balance query.
export function balanceMoved(previous: Schemas["OrderState"] | undefined, state: Schemas["OrderState"]) {
  if (previous === state) {
    return false;
  }
  return previous !== undefined || state === "paid" || state === "refunded" || state === "partially_refunded";
}

// orderMoved is balanceMoved for the order the page is watching now. A state remembered for another order is not that
// order's previous state: the next order is read for the first time even when both are paid.
export function orderMoved(
  previous: { id: string; state: Schemas["OrderState"] } | undefined,
  id: string,
  state: Schemas["OrderState"],
) {
  return balanceMoved(previous?.id === id ? previous.state : undefined, state);
}

// TopUpFlow keeps the order across the channel's return page and polls it while it waits: a payment often arrives
// without the browser coming back through that page.
function TopUpFlow({ currency }: { currency: string }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [orderId, setOrderId] = useState<string | null>(readStored);
  const [created, setCreated] = useState<Schemas["Order"] | null>(null);
  const live = useQuery({ ...orderQuery(orderId ?? ""), enabled: orderId !== null });
  const actions = useOrderActions(orderId ?? "");
  const client = useQueryClient();

  const order = mergeOrder(created, live.data);

  // The poll confirms a payment made in another tab or on another device. The balance and the order list were read
  // before it, so they are refreshed as soon as the state moves; a first read of a paid order does the same because
  // that payment may have landed after the balance query.
  const lastState = useRef<{ id: string; state: Schemas["OrderState"] } | undefined>(undefined);
  useEffect(() => {
    const id = live.data?.id;
    const state = live.data?.state;
    // A read that does not belong to the order the page is watching now (an old answer after a restart) is not its state.
    if (!id || !state || id !== orderId) {
      return;
    }
    const previous = lastState.current;
    lastState.current = { id, state };
    if (orderMoved(previous, id, state)) {
      void client.invalidateQueries({ queryKey: balanceQuery.queryKey });
      void client.invalidateQueries({ queryKey: ordersQuery.queryKey });
    }
  }, [live.data?.id, live.data?.state, orderId, client]);

  // A remembered order this site does not have starts the flow over instead of showing an error nobody can act on.
  useEffect(() => {
    if (live.error instanceof ApiError && live.error.status === 404) {
      clearStored();
      setOrderId(null);
      setCreated(null);
    }
  }, [live.error]);

  const restart = () => {
    clearStored();
    setOrderId(null);
    setCreated(null);
  };
  // A remembered order this site does not have is reset below, not reported as a failure.
  const liveError = live.error instanceof ApiError && live.error.status === 404 ? undefined : live.error;
  const failure = liveError ?? actions.verify.error ?? actions.cancel.error;
  return (
    <Panel
      title={t("topup.title")}
      action={
        <Link to="/orders" className="text-ui text-accent underline-offset-4 hover:underline">
          {t("topup.orders")}
        </Link>
      }
    >
      <div className="space-y-4 p-4">
        {failure ? <FormError>{errors.message(failure)}</FormError> : null}
        {order ? (
          <OrderStatus
            order={order}
            verifying={actions.verify.isPending}
            cancelling={actions.cancel.isPending}
            onVerify={() => actions.verify.mutate()}
            onCancel={() => actions.cancel.mutate()}
            onRestart={restart}
          />
        ) : orderId !== null ? (
          // The stored order is still being read; the form waits so a second one cannot be started by accident.
          <div className="space-y-2">
            {live.isPending ? <p className="text-t3">{t("topup.loading")}</p> : null}
            {live.isError ? (
              <Button variant="secondary" size="md" onClick={restart}>
                {t("topup.new")}
              </Button>
            ) : null}
          </div>
        ) : (
          <NewTopUp
            currency={currency}
            onCreated={(next) => {
              setCreated(next);
              setOrderId(next.id);
              writeStored(next.id);
            }}
          />
        )}
      </div>
    </Panel>
  );
}

type OrderRequest = { method: string; credits: string };

// The request whose answer was lost, kept for this tab and this person past a reload: the same key and body go out
// again, so the server replays the order instead of making a second one. Nothing here is a secret.
const attemptStore = "axiom.console.top-up.attempt";
const attemptKey = (scope: string) => `${scope}/${attemptStore}`;

function isOrderRequest(value: unknown): value is OrderRequest {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const { method, credits } = value as { method?: unknown; credits?: unknown };
  return typeof method === "string" && typeof credits === "string";
}

function readAttempt(scope: string): Attempt<OrderRequest> | null {
  try {
    const raw = sessionStorage.getItem(attemptKey(scope));
    if (!raw) {
      return null;
    }
    const value: unknown = JSON.parse(raw);
    if (typeof value !== "object" || value === null) {
      return null;
    }
    if ((value as { state?: unknown }).state === "reused") {
      return { state: "reused" };
    }
    const key = (value as { key?: unknown }).key;
    if (typeof key !== "string") {
      return null;
    }
    if ((value as { state?: unknown }).state === "editing") {
      return { state: "editing", key };
    }
    const request = (value as { request?: unknown }).request;
    if ((value as { state?: unknown }).state === "unknown" && isOrderRequest(request)) {
      return { state: "unknown", key, request };
    }
    return null;
  } catch {
    return null;
  }
}

function writeAttempt(scope: string, attempt: Attempt<OrderRequest>) {
  try {
    if (attempt.state === "editing" || attempt.state === "stale") {
      sessionStorage.removeItem(attemptKey(scope));
      return;
    }
    sessionStorage.setItem(attemptKey(scope), JSON.stringify(attempt));
  } catch {
    // The form still locks this attempt; only a reload loses it.
  }
}

function clearAttempt(scope: string) {
  try {
    sessionStorage.removeItem(attemptKey(scope));
  } catch {
    // nothing to clear
  }
}

// NewTopUp asks the amount, then the methods that can take it, and places one order. The request that was sent and its
// key are fixed: while its answer is unknown the form is locked and only that request goes out again, also after a
// reload. A refusal is the only answer that allows a different request, with a key of its own.
function NewTopUp({ currency, onCreated }: { currency: string; onCreated: (order: Schemas["Order"]) => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const format = useFormat();
  const client = useQueryClient();
  const { user, account } = useSession();
  const scope = `${user.id}/${account}/top-up`;
  const [initial] = useState(() => readAttempt(scope));
  const [attempt, setAttempt] = useState<Attempt<OrderRequest>>(() => initial ?? opened(crypto.randomUUID()));
  const [amount, setAmount] = useState(() => (initial?.state === "unknown" ? moneyInput(initial.request.credits) : ""));
  const [method, setMethod] = useState(() => (initial?.state === "unknown" ? initial.request.method : ""));
  const [checked, setChecked] = useState<string | null>(null);
  const [failure, setFailure] = useState<unknown>(null);
  const credits = parseMoney(amount);
  const askable = credits !== null && credits !== "0";
  const asked = askable && checked === credits;
  const change = (next: Attempt<OrderRequest>) => {
    setAttempt(next);
    writeAttempt(scope, next);
  };
  type Sent = { from: Sendable<OrderRequest>; request: OrderRequest };
  const create = useMutation({
    mutationFn: ({ from, request }: Sent) =>
      unwrapWithStatus(
        consoleApi.POST("/api/console/v1/orders", {
          params: { header: { "Idempotency-Key": from.key } },
          body: { method: request.method, credits: { amount: request.credits, currency } },
        }),
      ),
    onSuccess: ({ data, status }) => {
      setFailure(null);
      clearAttempt(scope);
      onCreated(data);
      // A replayed key can answer with an order that is already paid; the balance has then moved.
      if (status === 200) {
        void client.invalidateQueries({ queryKey: balanceQuery.queryKey });
      }
    },
    onError: (error, sent) => {
      setFailure(error);
      change(failed(sent.from, sent.request, error, () => crypto.randomUUID()));
    },
  });
  const send = (from: Sendable<OrderRequest>, request: OrderRequest) => {
    change({ state: "unknown", key: from.key, request });
    create.mutate({ from, request });
  };
  const changeAmount = (value: string) => {
    if (attempt.state !== "editing") {
      return;
    }
    setAmount(value);
    setChecked(null);
    setFailure(null);
    change(opened(crypto.randomUUID()));
  };
  if (attempt.state === "reused") {
    return (
      <div className="space-y-3">
        {failure ? <FormError>{errors.message(failure)}</FormError> : null}
        <p className="max-w-prose text-ui text-t2">{t("topup.reused")}</p>
        <Button asChild variant="secondary" size="md">
          <Link to="/orders">{t("topup.orders")}</Link>
        </Button>
      </div>
    );
  }
  if (attempt.state === "stale") {
    return <p className="max-w-prose text-ui text-t2">{t("topup.unknown")}</p>;
  }
  const retry = attempt.state === "unknown";
  return (
    <>
      <form
        className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end"
        onSubmit={(event) => {
          event.preventDefault();
          if (retry) {
            send(attempt, attempt.request);
          } else if (askable) {
            setChecked(credits);
          }
        }}
      >
        <TextField
          label={t("topup.amount")}
          hint={t("topup.amountHint", { currency })}
          inputMode="decimal"
          autoComplete="off"
          placeholder="10"
          value={amount}
          disabled={retry}
          onChange={(event) => changeAmount(event.target.value)}
          error={errors.field(failure, "credits")}
        />
        {retry ? null : (
          <Button type="submit" size="md" disabled={!askable}>
            {t("topup.check")}
          </Button>
        )}
      </form>
      {failure ? <FormError>{errors.message(failure)}</FormError> : null}
      {retry ? (
        <div className="space-y-3">
          <p className="text-ui text-t2">
            {t("topup.retrySummary", {
              credits: format.money({ amount: attempt.request.credits, currency }),
              method: attempt.request.method,
            })}
          </p>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              send(attempt, attempt.request);
            }}
          >
            <SubmitButton pending={create.isPending}>{t("topup.retry")}</SubmitButton>
          </form>
          <p className="text-label text-t3">{t("topup.unknown")}</p>
        </div>
      ) : asked && credits ? (
        <Quotes
          credits={credits}
          method={method}
          onMethod={(value) => {
            if (attempt.state !== "editing") {
              return;
            }
            setMethod(value);
            setFailure(null);
            change(opened(crypto.randomUUID()));
          }}
          pending={create.isPending}
          onPay={(chosen) => send(attempt, { method: chosen, credits })}
        />
      ) : null}
    </>
  );
}

// Quotes lists what the amount costs through each method; only the method the user picks is sent.
function Quotes({
  credits,
  method,
  onMethod,
  pending,
  onPay,
}: {
  credits: string;
  method: string;
  onMethod: (method: string) => void;
  pending: boolean;
  onPay: (method: string) => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const methods = useQuery(paymentMethodsQuery(credits));
  return (
    <DataState
      query={methods}
      isEmpty={(data) => data.data.length === 0}
      empty={<p className="text-t3">{t("topup.noMethods")}</p>}
    >
      {(data) => {
        const first = data.data[0];
        if (!first) {
          return null;
        }
        const chosen = data.data.some((m) => m.method === method) ? method : first.method;
        const selected = data.data.find((m) => m.method === chosen) ?? first;
        return (
          <form
            className="space-y-3"
            onSubmit={(event) => {
              event.preventDefault();
              onPay(chosen);
            }}
          >
            <SelectField label={t("topup.method")} value={chosen} onChange={(e) => onMethod(e.target.value)}>
              {data.data.map((m) => (
                <option key={m.method} value={m.method}>
                  {t("topup.methodOption", { method: m.method, total: format.pay(m.total) })}
                </option>
              ))}
            </SelectField>
            <p className="text-ui text-t2">
              {t("topup.youPay", { total: format.pay(selected.total) })}{" "}
              <span className="text-t3">{t("topup.feeIncluded", { fee: format.pay(selected.fee) })}</span>
            </p>
            <SubmitButton pending={pending}>{t("topup.submit")}</SubmitButton>
          </form>
        );
      }}
    </DataState>
  );
}

function OrderStatus({
  order,
  verifying,
  cancelling,
  onVerify,
  onCancel,
  onRestart,
}: {
  order: Schemas["Order"];
  verifying: boolean;
  cancelling: boolean;
  onVerify: () => void;
  onCancel: () => void;
  onRestart: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Badge tone={tone(order.state)} icon={order.state === "paid" ? "check" : "refresh"}>
          {t(`orders.state.${order.state}`)}
        </Badge>
        <span className="text-ui text-t2">
          {t("topup.summary", { credits: format.money(order.credits), total: format.pay(order.total) })}
        </span>
      </div>
      {order.state === "pending" ? (
        <PendingPayment
          order={order}
          verifying={verifying}
          cancelling={cancelling}
          onVerify={onVerify}
          onCancel={onCancel}
        />
      ) : (
        <OrderOutcome order={order} onRestart={onRestart} />
      )}
    </>
  );
}

// tone names the state without relying on its colour: paid is done, waiting is open, and the rest are closed.
function tone(state: Schemas["OrderState"]) {
  if (state === "paid") {
    return "ok" as const;
  }
  if (state === "pending" || state === "review") {
    return "warn" as const;
  }
  return "neutral" as const;
}

function PendingPayment({
  order,
  verifying,
  cancelling,
  onVerify,
  onCancel,
}: {
  order: Schemas["Order"];
  verifying: boolean;
  cancelling: boolean;
  onVerify: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <>
      {order.checkout ? (
        <p className="max-w-prose text-ui text-t2">{t("topup.waiting")}</p>
      ) : (
        <div className="max-w-prose space-y-1">
          <p className="text-ui text-t2">{t("topup.unconfirmed")}</p>
          <p className="text-ui text-t3">{t("topup.unconfirmedBody")}</p>
        </div>
      )}
      {order.checkout ? <Checkout checkout={order.checkout} /> : null}
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" size="md" disabled={verifying} onClick={onVerify}>
          <Icon name="refresh" className={verifying ? "animate-spin" : undefined} />
          {t("topup.verify")}
        </Button>
        <Button variant="ghost" size="md" disabled={cancelling} onClick={onCancel}>
          {t("topup.cancel")}
        </Button>
      </div>
      <p className="text-label text-t3">{t("topup.expires", { at: format.dateTime(new Date(order.expires_at)) })}</p>
    </>
  );
}

// Checkout is where to pay: a link, a code to show, or a secret the channel's own page needs, each only when the
// channel gave it.
export function Checkout({ checkout }: { checkout: Schemas["Checkout"] }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-3">
      {checkout.pay_url ? (
        <>
          <Button asChild variant="primary" size="md">
            <a href={checkout.pay_url} rel="noreferrer">
              <Icon name="card" />
              {t("topup.payNow")}
            </a>
          </Button>
          <p className="text-ui text-t3">{t("topup.returnHint")}</p>
        </>
      ) : null}
      {checkout.qr_code ? (
        <div className="space-y-2">
          <QrCode value={checkout.qr_code} label={t("topup.qr")} />
          <CodeBlock title={t("topup.qr")} hint={t("topup.qrHint")} code={checkout.qr_code} />
        </div>
      ) : null}
      {checkout.client_secret ? (
        <CodeBlock title={t("topup.secret")} hint={t("topup.secretHint")} code={checkout.client_secret} />
      ) : null}
    </div>
  );
}

function CodeBlock({ title, hint, code }: { title: string; hint: string; code: string }) {
  return (
    <div className="space-y-2">
      <p className="text-ui text-t2">{title}</p>
      <div className="flex flex-wrap items-start gap-2">
        <code className="max-w-full rounded-btn bg-s2 px-3 py-2 font-mono text-label break-all text-t1">{code}</code>
        <CopyButton text={code} />
      </div>
      <p className="text-label text-t3">{hint}</p>
    </div>
  );
}

function OrderOutcome({ order, onRestart }: { order: Schemas["Order"]; onRestart: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <>
      {order.state === "paid" ? (
        <p className="text-ui text-t2">
          {t("topup.paidBody", { credits: format.money(order.credits) })}{" "}
          <Link to="/statement" className="text-accent underline-offset-4 hover:underline">
            {t("topup.seeStatement")}
          </Link>
        </p>
      ) : null}
      {order.state === "review" ? <p className="text-ui text-t2">{t("topup.reviewBody")}</p> : null}
      {order.state !== "paid" && order.state !== "review" ? (
        <p className="text-ui text-t2">{t("topup.closedBody", { state: t(`orders.state.${order.state}`) })}</p>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" size="md" onClick={onRestart}>
          <Icon name="plus" />
          {t("topup.new")}
        </Button>
        <Button asChild variant="ghost" size="md">
          <Link to="/orders">{t("topup.orders")}</Link>
        </Button>
      </div>
    </>
  );
}
