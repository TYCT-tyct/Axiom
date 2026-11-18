import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { type Attempt, failed, opened, type Sendable, useSession } from "@axiom/app";
import { moneyInput, parseMoney } from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type RefCallback, useCallback, useEffect, useRef, useState } from "react";
import { clearAttempt, readAttempt, unanswered, writeAttempt } from "../attempt-store";
import { balanceQuery, orderQuery, ordersQuery, paymentMethodsQuery } from "../queries";
import { useBillAttempt } from "./bill-attempt";
import { quickAmountsForCurrency } from "./bill-data";

export type OrderRequest = Schemas["OrderCreate"];
export type Order = Schemas["Order"];
export type ActiveOrderState = { id: string; created: Order | null };

const TOPUP_SESSION_KEY = "axiom.console.top-up";
const ATTEMPT_STORE = "axiom.console.top-up.attempt";
const POLL_MS = 3000;

function useBillScope() {
  const session = useSession();
  return session.account ? `${session.user.id}/${session.account}` : session.user.id;
}

function readStoredOrderId(scope: string): string | null {
  try {
    return sessionStorage.getItem(`${scope}/${TOPUP_SESSION_KEY}`);
  } catch {
    return null;
  }
}

function writeStoredOrderId(scope: string, id: string | null) {
  try {
    const key = `${scope}/${TOPUP_SESSION_KEY}`;
    if (id) sessionStorage.setItem(key, id);
    else sessionStorage.removeItem(key);
  } catch {
    // Keep session order id in React state if storage is unavailable.
  }
}

/** Reports whether an observed order transition may have updated account balance or order state. */
export function balanceMoved(previous: Schemas["OrderState"] | undefined, state: Schemas["OrderState"]) {
  if (previous === state) return false;
  return previous !== undefined || state === "paid" || state === "refunded" || state === "partially_refunded";
}

/** Tracks state transitions for one watched order ID without confusing sequential orders. */
export function orderMoved(
  previous: { id: string; state: Schemas["OrderState"] } | undefined,
  id: string,
  state: Schemas["OrderState"],
) {
  return balanceMoved(previous?.id === id ? previous.state : undefined, state);
}

const isOrderRequest = (value: unknown): value is OrderRequest => {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Record<string, unknown>;
  if (typeof candidate.method !== "string" || typeof candidate.credits !== "object" || candidate.credits === null) {
    return false;
  }
  const credits = candidate.credits as Record<string, unknown>;
  return typeof credits.amount === "string" && typeof credits.currency === "string";
};

export function parsePositiveCredits(raw: string): string | null {
  const parsed = parseMoney(raw);
  if (!parsed) return null;
  return BigInt(parsed) > 0n ? parsed : null;
}

function useActiveOrder(orderId: string | null) {
  const queryClient = useQueryClient();
  const prevState = useRef<{ id: string; state: Schemas["OrderState"] } | undefined>(undefined);
  const query = useQuery({
    ...orderQuery(orderId ?? ""),
    enabled: orderId !== null,
    refetchInterval: ({ state }) => {
      if (state.error instanceof ApiError && [401, 403, 404].includes(state.error.status)) return false;
      return !state.data || state.data.state === "pending" ? POLL_MS : false;
    },
  });
  const liveOrder = query.data?.id === orderId ? query.data : undefined;
  const state = liveOrder?.state;
  useEffect(() => {
    if (!orderId) {
      prevState.current = undefined;
      return;
    }
    if (!state) return;
    if (orderMoved(prevState.current, orderId, state)) {
      void queryClient.invalidateQueries({ queryKey: balanceQuery.queryKey });
      void queryClient.invalidateQueries({ queryKey: ordersQuery.queryKey });
    }
    prevState.current = { id: orderId, state };
  }, [orderId, state, queryClient]);
  const isNotFound = query.error instanceof ApiError && query.error.status === 404;
  return { query, liveOrder, isNotFound };
}

/** Keeps the current active order watched across modal open/close and payment-channel redirects. */
export function useBillTopUpSession(enabled = true) {
  const scope = useBillScope();
  const { active, change, placement } = useTopUpAttempt(scope);
  const selectActive = useCallback(
    (next: ActiveOrderState | null) =>
      change((current) => {
        writeStoredOrderId(scope, next?.id ?? null);
        return { ...current, active: next };
      }),
    [change, scope],
  );
  const activeOrder = useActiveOrder(enabled ? (active?.id ?? null) : null);
  useEffect(() => {
    if (active && activeOrder.isNotFound) selectActive(null);
  }, [active, activeOrder.isNotFound, selectActive]);

  return { active, activeOrder, selectActive, placement };
}

type Placement = { from: Sendable<OrderRequest>; request: OrderRequest };
type TopUpState = {
  attempt: Attempt<OrderRequest>;
  pending: boolean;
  active: ActiveOrderState | null;
  draft?: OrderRequest;
  failure?: unknown;
};

function freshTopUp(active: ActiveOrderState | null): TopUpState {
  return { active, attempt: opened(crypto.randomUUID()), pending: false };
}

function useTopUpAttempt(scope: string) {
  const storeKey = `${scope}/${ATTEMPT_STORE}`;
  const queryClient = useQueryClient();
  const { state, change, start, finish } = useBillAttempt<TopUpState>(scope, "top-up", () => {
    const saved = readStoredOrderId(scope);
    const initial = freshTopUp(saved ? { id: saved, created: null } : null);
    return { ...initial, attempt: readAttempt(storeKey, isOrderRequest) ?? initial.attempt };
  });
  const create = useMutation({
    mutationFn: ({ from, request }: Placement) =>
      unwrap(
        consoleApi.POST("/api/console/v1/orders", {
          body: request,
          params: { header: { "Idempotency-Key": from.key } },
        }),
      ),
    onSuccess: (order, { from }) => {
      const committed = finish(from.key, () => {
        writeStoredOrderId(scope, order.id);
        clearAttempt(storeKey);
        return freshTopUp({ id: order.id, created: order });
      });
      if (committed) {
        void queryClient.invalidateQueries({ queryKey: balanceQuery.queryKey });
        void queryClient.invalidateQueries({ queryKey: ordersQuery.queryKey });
      }
    },
    onError: (failure, { from, request }) => {
      finish(from.key, (current) => {
        const attempt = failed(from, request, failure, () => crypto.randomUUID());
        writeAttempt(storeKey, attempt);
        return { ...current, attempt, draft: request, failure };
      });
    },
  });

  const reset = () =>
    change((current) => {
      if (current.pending) return undefined;
      clearAttempt(storeKey);
      return freshTopUp(current.active);
    });

  const send = ({ from, request }: Placement) => {
    if (
      start(from.key, (current) => {
        const attempt = unanswered(from, request);
        writeAttempt(storeKey, attempt);
        return { ...current, attempt, failure: undefined };
      })
    )
      create.mutate({ from, request });
  };

  const { active, pending: creating, ...placement } = state;
  return { active, change, placement: { ...placement, creating, send, reset } };
}

function pickMethod(methods: readonly Schemas["PaymentMethod"][], chosenMethod: string | undefined) {
  return chosenMethod === undefined ? methods[0] : methods.find((item) => item.method === chosenMethod);
}

function useAmount(initial: string, locked: boolean) {
  const inputRef = useRef<HTMLInputElement | null>(null);
  const [amountText, setAmountText] = useState(initial);
  const attachInput: RefCallback<Element> = (node) => {
    const input = node as HTMLInputElement | null;
    inputRef.current = input;
    if (input) {
      input.readOnly = locked;
      input.value = amountText;
    }
  };
  const applyAmount = (next: string) => {
    setAmountText(next);
    if (inputRef.current) inputRef.current.value = next;
  };
  return { amountText, inputRef, attachInput, applyAmount };
}

function usePaymentQuote(activeCredits: string | null, editable: boolean, chosenMethod: string | undefined) {
  const methodsQuery = useQuery({
    ...paymentMethodsQuery(activeCredits ?? "0"),
    enabled: editable && activeCredits !== null,
  });
  const methods = editable ? (methodsQuery.data?.data ?? []) : [];
  const selected = pickMethod(methods, chosenMethod);
  return { methodsQuery, methods, selected };
}

export function useStepOneController(
  currency: string,
  initialUnits: string | undefined,
  { attempt, draft, failure, creating, send, reset }: ReturnType<typeof useTopUpAttempt>["placement"],
) {
  const presets = quickAmountsForCurrency(currency);
  const sent = attempt.state === "unknown" ? attempt.request : undefined;
  const isUnknown = !!sent && !creating;
  const isBlocked = attempt.state === "reused" || attempt.state === "stale";
  const editable = !sent && !isBlocked;
  const locked = creating || !editable;
  const restored = sent ?? draft;
  const amount = useAmount(
    restored ? moneyInput(restored.credits.amount) : (initialUnits ?? presets[1] ?? "200"),
    locked,
  );
  const [chosenMethod, setChosenMethod] = useState(() => restored?.method);
  const activeCredits = sent?.credits.amount ?? parsePositiveCredits(amount.amountText);
  const { methodsQuery, methods, selected } = usePaymentQuote(activeCredits, editable, chosenMethod);

  const submit = () => {
    if (attempt.state === "unknown") send({ from: attempt, request: attempt.request });
    if (attempt.state === "editing" && activeCredits && selected) {
      send({ from: attempt, request: { method: selected.method, credits: { amount: activeCredits, currency } } });
    }
  };

  return {
    presets,
    ...amount,
    attempt,
    failure,
    creating,
    reset,
    locked,
    isUnknown,
    isBlocked,
    activeCredits,
    methodsQuery,
    methods,
    selected,
    setChosenMethod,
    submit,
  };
}
