import { ApiError, consoleApi, isTransient, type Schemas, unwrapWithStatus } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
import {
  Badge,
  Button,
  CopyButton,
  DataState,
  FormError,
  Icon,
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
import { balanceQuery, orderQuery, paymentMethodsQuery } from "./queries";

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

// TopUpFlow keeps the order across the channel's return page and polls it while it waits: a payment often arrives
// without the browser coming back through that page.
function TopUpFlow({ currency }: { currency: string }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [orderId, setOrderId] = useState<string | null>(readStored);
  const [created, setCreated] = useState<Schemas["Order"] | null>(null);
  const live = useQuery({ ...orderQuery(orderId ?? ""), enabled: orderId !== null });
  const actions = useOrderActions(orderId ?? "");

  const order = mergeOrder(created, live.data);

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

// mergeOrder keeps what the created answer alone has, the channel's client secret, and takes the state from the newer
// read. A read of a different order is not merged.
function mergeOrder(created: Schemas["Order"] | null, live: Schemas["Order"] | undefined) {
  if (!created) {
    return live;
  }
  if (!live || created.id !== live.id) {
    return created;
  }
  return { ...created, ...live, checkout: { ...live.checkout, ...created.checkout } };
}

// NewTopUp asks the amount, then the methods that can take it, and places one order. Each intended order has its own
// key: a lost answer is retried under the same key so it cannot make two orders, and a changed or refused request gets
// a new one.
function NewTopUp({ currency, onCreated }: { currency: string; onCreated: (order: Schemas["Order"]) => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const client = useQueryClient();
  const [amount, setAmount] = useState("");
  const [checked, setChecked] = useState<string | null>(null);
  const [method, setMethod] = useState("");
  const [failure, setFailure] = useState<unknown>(null);
  const key = useRef("");
  if (key.current === "") {
    key.current = crypto.randomUUID();
  }
  const credits = parseMoney(amount);
  const askable = credits !== null && credits !== "0";
  const asked = askable && checked === credits;
  const create = useMutation({
    mutationFn: (order: { method: string; credits: string }) =>
      unwrapWithStatus(
        consoleApi.POST("/api/console/v1/orders", {
          params: { header: { "Idempotency-Key": key.current } },
          body: { method: order.method, credits: { amount: order.credits, currency } },
        }),
      ),
    onSuccess: ({ data, status }) => {
      setFailure(null);
      onCreated(data);
      // A replayed key can answer with an order that is already paid; the balance has then moved.
      if (status === 200) {
        void client.invalidateQueries({ queryKey: balanceQuery.queryKey });
      }
    },
    onError: (error) => {
      setFailure(error);
      if (!isTransient(error)) {
        key.current = crypto.randomUUID();
      }
    },
  });
  const changeAmount = (value: string) => {
    setAmount(value);
    setChecked(null);
    setFailure(null);
    key.current = crypto.randomUUID();
  };
  const error = failure;
  return (
    <>
      <form
        className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end"
        onSubmit={(event) => {
          event.preventDefault();
          if (askable) {
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
          onChange={(event) => changeAmount(event.target.value)}
          error={errors.field(error, "credits")}
        />
        <Button type="submit" size="md" disabled={!askable}>
          {t("topup.check")}
        </Button>
      </form>
      {error ? <FormError>{errors.message(error)}</FormError> : null}
      {asked && credits ? (
        <Quotes
          credits={credits}
          method={method}
          onMethod={(value) => {
            setMethod(value);
            setFailure(null);
            key.current = crypto.randomUUID();
          }}
          pending={create.isPending}
          onPay={(chosen) => create.mutate({ method: chosen, credits })}
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
function Checkout({ checkout }: { checkout: Schemas["Checkout"] }) {
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
      {checkout.qr_code ? <CodeBlock title={t("topup.qr")} hint={t("topup.qrHint")} code={checkout.qr_code} /> : null}
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
