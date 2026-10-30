import { ApiError, type Schemas } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
import { Button, FormError, Panel } from "@axiom/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { NewTopUp } from "./new-top-up";
import { useOrderActions } from "./order-actions";
import { mergeOrder } from "./order-merge";
import { OrderStatus } from "./order-status";
import { balanceQuery, orderQuery, ordersQuery } from "./queries";

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

// useWatchedOrder is the order the top-up panel follows: the one this tab remembers across the channel's return page,
// the creating answer that alone has the client secret, and the poll that notices a payment arriving without that page.
// The balance and the order list were read before such a payment, so they are refreshed as soon as the state moves; a
// remembered order this site does not have is forgotten and the flow starts over.
function useWatchedOrder() {
  const client = useQueryClient();
  const [orderId, setOrderId] = useState<string | null>(readStored);
  const [created, setCreated] = useState<Schemas["Order"] | null>(null);
  const live = useQuery({ ...orderQuery(orderId ?? ""), enabled: orderId !== null });
  const lastState = useRef<{ id: string; state: Schemas["OrderState"] } | undefined>(undefined);
  const forget = () => {
    clearStored();
    setOrderId(null);
    setCreated(null);
  };
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
  // A remembered order this site does not have is reset, not reported as a failure.
  useEffect(() => {
    if (live.error instanceof ApiError && live.error.status === 404) {
      forget();
    }
  }, [live.error]);
  const started = (order: Schemas["Order"]) => {
    setCreated(order);
    setOrderId(order.id);
    writeStored(order.id);
  };
  const notFound = live.error instanceof ApiError && live.error.status === 404;
  return {
    orderId,
    live,
    order: mergeOrder(created, live.data),
    failure: notFound ? undefined : live.error,
    restart: forget,
    started,
  };
}

// TopUpFlow keeps the order across the channel's return page and polls it while it waits: a payment often arrives
// without the browser coming back through that page.
function TopUpFlow({ currency }: { currency: string }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const watched = useWatchedOrder();
  const actions = useOrderActions(watched.orderId ?? "");
  const failure = watched.failure ?? actions.verify.error ?? actions.cancel.error;
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
        {watched.order ? (
          <OrderStatus
            order={watched.order}
            verifying={actions.verify.isPending}
            cancelling={actions.cancel.isPending}
            onVerify={() => actions.verify.mutate()}
            onCancel={() => actions.cancel.mutate()}
            onRestart={watched.restart}
          />
        ) : watched.orderId !== null ? (
          // The stored order is still being read; the form waits so a second one cannot be started by accident.
          <div className="space-y-2">
            {watched.live.isPending ? <p className="text-t3">{t("topup.loading")}</p> : null}
            {watched.live.isError ? (
              <Button variant="secondary" size="md" onClick={watched.restart}>
                {t("topup.new")}
              </Button>
            ) : null}
          </div>
        ) : (
          <NewTopUp currency={currency} onCreated={watched.started} />
        )}
      </div>
    </Panel>
  );
}
