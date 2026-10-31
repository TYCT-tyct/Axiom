import { consoleApi, type Schemas, unwrapWithStatus } from "@axiom/api";
import { type Attempt, failedFrom, opened, type Sendable, useErrors, useSession } from "@axiom/app";
import {
  Button,
  DataState,
  FormError,
  moneyInput,
  parseMoney,
  SelectField,
  SubmitButton,
  TextField,
  useFormat,
} from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { clearAttempt, readAttempt, unanswered, writeAttempt } from "./attempt-store";
import { balanceQuery, paymentMethodsQuery } from "./queries";

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

// useTopUpRequest is the order being placed, from the amount to the key that makes a retry the same order. The request
// and its key are fixed while the answer is unknown, also after a reload or the channel's return page; a refusal is
// the only answer that allows a different request, with a key of its own.
function useTopUpRequest(currency: string, onCreated: (order: Schemas["Order"]) => void) {
  const client = useQueryClient();
  const { user, account } = useSession();
  const scope = `${user.id}/${account}/top-up`;
  const [initial] = useState(() => readAttempt(attemptKey(scope), isOrderRequest));
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
    writeAttempt(attemptKey(scope), next);
  };
  type Sent = { from: Sendable<OrderRequest>; request: OrderRequest };
  const create = useMutation({
    // The request is recorded before it is sent: if no answer comes back, only this request under this key goes out
    // again, so retrying cannot make a second order.
    onMutate: ({ from, request }: Sent) => change(unanswered(from, request)),
    mutationFn: ({ from, request }: Sent) =>
      unwrapWithStatus(
        consoleApi.POST("/api/console/v1/orders", {
          params: { header: { "Idempotency-Key": from.key } },
          body: { method: request.method, credits: { amount: request.credits, currency } },
        }),
      ),
    onSuccess: ({ data, status }) => {
      setFailure(null);
      clearAttempt(attemptKey(scope));
      onCreated(data);
      // A replayed key can answer with an order that is already paid; the balance has then moved.
      if (status === 200) {
        void client.invalidateQueries({ queryKey: balanceQuery.queryKey });
      }
    },
    onError: failedFrom(change, setFailure),
  });
  // Editing either half starts a new request and its key; a new amount also drops the check made for the old one, and
  // while an answer is unknown nothing here is the person's to edit.
  const edit = (next: Partial<OrderRequest>) => {
    if (attempt.state !== "editing") {
      return;
    }
    if (next.credits !== undefined) {
      setAmount(next.credits);
      setChecked(null);
    }
    if (next.method !== undefined) {
      setMethod(next.method);
    }
    setFailure(null);
    change(opened(crypto.randomUUID()));
  };
  const check = () => {
    if (askable) {
      setChecked(credits);
    }
  };
  return { attempt, amount, method, credits, asked, askable, failure, creating: create.isPending, create, check, edit };
}

// NewTopUp asks the amount, then the methods that can take it, and places one order.
export function NewTopUp({ currency, onCreated }: { currency: string; onCreated: (order: Schemas["Order"]) => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const { attempt, amount, method, credits, asked, askable, failure, creating, create, check, edit } = useTopUpRequest(
    currency,
    onCreated,
  );
  if (attempt.state === "reused") {
    return <ReusedTopUp failure={failure} />;
  }
  if (attempt.state === "stale") {
    return <p className="max-w-prose text-ui text-t2">{t("topup.unknown")}</p>;
  }
  const field = errors.field(failure, "credits");
  if (attempt.state === "unknown") {
    return (
      <>
        <AmountForm
          currency={currency}
          value={amount}
          locked
          checkable={false}
          error={field}
          onChange={(value) => edit({ credits: value })}
          onCheck={check}
        />
        {failure ? <FormError>{errors.message(failure)}</FormError> : null}
        <RetryTopUp
          request={attempt.request}
          currency={currency}
          pending={creating}
          onRetry={() => create.mutate({ from: attempt, request: attempt.request })}
        />
      </>
    );
  }
  return (
    <>
      <AmountForm
        currency={currency}
        value={amount}
        locked={false}
        checkable={askable}
        error={field}
        onChange={(value) => edit({ credits: value })}
        onCheck={check}
      />
      {failure ? <FormError>{errors.message(failure)}</FormError> : null}
      {asked && credits ? (
        <Quotes
          credits={credits}
          method={method}
          onMethod={(next) => edit({ method: next })}
          pending={creating}
          onPay={(next) => create.mutate({ from: attempt, request: { method: next, credits } })}
        />
      ) : null}
    </>
  );
}

// ReusedTopUp says the server holds this key for another request, so the first one may have been done: nothing more is
// sent from here, and the orders page is where to look.
function ReusedTopUp({ failure }: { failure: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
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

// RetryTopUp sends the request whose answer was lost again, unchanged and under the same key: nothing else may go out
// until its outcome is known.
function RetryTopUp({
  request,
  currency,
  pending,
  onRetry,
}: {
  request: OrderRequest;
  currency: string;
  pending: boolean;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <div className="space-y-3">
      <p className="text-ui text-t2">
        {t("topup.retrySummary", {
          credits: format.money({ amount: request.credits, currency }),
          method: request.method,
        })}
      </p>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          onRetry();
        }}
      >
        <SubmitButton pending={pending}>{t("topup.retry")}</SubmitButton>
      </form>
      <p className="text-label text-t3">{t("topup.unknown")}</p>
    </div>
  );
}

// AmountForm asks the credits to buy. It stays visible but locked while a request whose answer was lost waits.
function AmountForm({
  currency,
  value,
  locked,
  checkable,
  error,
  onChange,
  onCheck,
}: {
  currency: string;
  value: string;
  locked: boolean;
  checkable: boolean;
  error: string | undefined;
  onChange: (value: string) => void;
  onCheck: () => void;
}) {
  const { t } = useTranslation();
  return (
    <form
      className="max-w-md space-y-3"
      onSubmit={(event) => {
        event.preventDefault();
        onCheck();
      }}
    >
      <TextField
        label={t("topup.amount")}
        hint={t("topup.amountHint", { currency })}
        inputMode="decimal"
        autoComplete="off"
        placeholder="10"
        value={value}
        disabled={locked}
        onChange={(event) => onChange(event.target.value)}
        error={error}
      />
      {locked ? null : (
        <Button type="submit" size="md" disabled={!checkable}>
          {t("topup.check")}
        </Button>
      )}
    </form>
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
            className="max-w-md space-y-3"
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
