import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { type Attempt, failedFrom, opened, type Sendable, useSession } from "@axiom/app";
import { moneyInput, parseMoney } from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { clearAttempt, readAttempt, unanswered, writeAttempt } from "../attempt-store";
import { balanceQuery, orderQuery, ordersQuery, paymentMethodsQuery } from "../queries";
import { balanceMoved, orderMoved } from "../top-up";
import { quickAmountsForCurrency } from "./bill-data";

export type OrderRequest = Schemas["OrderCreate"];
export type Order = Schemas["Order"];

const TOPUP_SESSION_KEY = "axiom.console.top-up";
const ATTEMPT_STORE = "axiom.console.top-up.attempt";
const POLL_MS = 3000;

export function useBillScope() {
  const session = useSession();
  return session.account ? `${session.user.id}/${session.account}` : session.user.id;
}

export function readStoredOrderId(scope: string): string | null {
  try {
    return sessionStorage.getItem(`${scope}/${TOPUP_SESSION_KEY}`);
  } catch {
    return null;
  }
}

export function writeStoredOrderId(scope: string, id: string | null) {
  try {
    const key = `${scope}/${TOPUP_SESSION_KEY}`;
    if (id) sessionStorage.setItem(key, id);
    else sessionStorage.removeItem(key);
  } catch {
    // Keep session order id in React state if storage is unavailable.
  }
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

export function useActiveOrder(orderId: string | null) {
  const queryClient = useQueryClient();
  const prevState = useRef<{ id: string; state: Schemas["OrderState"] } | undefined>(undefined);
  const query = useQuery({
    ...orderQuery(orderId ?? ""),
    enabled: orderId !== null,
    refetchInterval: (q) => (q.state.data?.state === "pending" ? POLL_MS : false),
  });
  const liveOrder = query.data?.id === orderId ? query.data : undefined;
  const state = liveOrder?.state;
  useEffect(() => {
    if (!orderId) {
      prevState.current = undefined;
      return;
    }
    if (!state) return;
    if (balanceMoved(prevState.current?.id === orderId ? prevState.current.state : undefined, state)) {
      void queryClient.invalidateQueries({ queryKey: balanceQuery.queryKey });
    }
    if (orderMoved(prevState.current, orderId, state)) {
      void queryClient.invalidateQueries({ queryKey: ordersQuery.queryKey });
    }
    prevState.current = { id: orderId, state };
  }, [orderId, state, queryClient]);
  const isNotFound = query.error instanceof ApiError && query.error.status === 404;
  return { query, liveOrder, isNotFound };
}

function useTopUpAttempt(scope: string, onCreated: (order: Order) => void) {
  const storeKey = `${scope}/${ATTEMPT_STORE}`;
  const queryClient = useQueryClient();
  const [attempt, setAttempt] = useState<Attempt<OrderRequest>>(
    () => readAttempt(storeKey, isOrderRequest) ?? opened(crypto.randomUUID()),
  );
  const [failure, setFailure] = useState<unknown>(null);

  const commit = (next: Attempt<OrderRequest>) => {
    setAttempt(next);
    writeAttempt(storeKey, next);
  };

  const reset = () => {
    setFailure(null);
    setAttempt(opened<OrderRequest>(crypto.randomUUID()));
    clearAttempt(storeKey);
  };

  const create = useMutation({
    mutationFn: ({ from, request }: { from: Sendable<OrderRequest>; request: OrderRequest }) =>
      unwrap(
        consoleApi.POST("/api/console/v1/orders", {
          body: request,
          params: { header: { "Idempotency-Key": from.key } },
        }),
      ),
    onMutate: ({ from, request }) => {
      setFailure(null);
      commit(unanswered(from, request));
    },
    onSuccess: (order) => {
      setAttempt(opened(crypto.randomUUID()));
      clearAttempt(storeKey);
      void queryClient.invalidateQueries({ queryKey: balanceQuery.queryKey });
      void queryClient.invalidateQueries({ queryKey: ordersQuery.queryKey });
      onCreated(order);
    },
    onError: (error, { from, request }) => failedFrom(commit, setFailure)(error, { from, request }),
  });

  return { attempt, failure, creating: create.isPending, create, reset };
}

function pickMethod(
  methods: readonly Schemas["PaymentMethod"][],
  attempt: Attempt<OrderRequest>,
  chosenMethod: string | null,
) {
  const preferred = attempt.state === "unknown" ? attempt.request.method : chosenMethod;
  return methods.find((item) => item.method === preferred) ?? methods[0];
}

function useSyncedAmount(attempt: Attempt<OrderRequest>, initialUnits: string | undefined, fallback: string) {
  const inputRef = useRef<HTMLInputElement | null>(null);
  const [amountText, setAmountText] = useState(() =>
    attempt.state === "unknown" ? moneyInput(attempt.request.credits.amount) : (initialUnits ?? fallback),
  );
  const setSynced = (next: string) => {
    setAmountText(next);
    if (inputRef.current) inputRef.current.value = next;
  };
  useEffect(() => {
    if (attempt.state === "unknown") {
      setSynced(moneyInput(attempt.request.credits.amount));
    } else if (initialUnits && attempt.state === "editing") {
      setSynced(initialUnits);
    }
  }, [initialUnits, attempt]);
  return { amountText, inputRef, applyAmount: setSynced };
}

export function useStepOneController({
  currency,
  initialUnits,
  onClose,
  onCreated,
}: {
  currency: string;
  initialUnits?: string;
  onClose: () => void;
  onCreated: (order: Order) => void;
}) {
  const scope = useBillScope();
  const presets = quickAmountsForCurrency(currency);
  const { attempt, failure, creating, create, reset } = useTopUpAttempt(scope, onCreated);
  const { amountText, inputRef, applyAmount } = useSyncedAmount(attempt, initialUnits, presets[1] ?? "200");
  const [chosenMethod, setChosenMethod] = useState<string | null>(null);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => event.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const isUnknown = attempt.state === "unknown";
  const isBlocked = attempt.state === "reused" || attempt.state === "stale";
  const activeCredits = isUnknown ? attempt.request.credits.amount : parsePositiveCredits(amountText);
  const methodsQuery = useQuery({ ...paymentMethodsQuery(activeCredits ?? "0"), enabled: activeCredits !== null });
  const methods = methodsQuery.data?.data ?? [];
  const selected = pickMethod(methods, attempt, chosenMethod);

  const submit = () => {
    if (creating) return;
    if (attempt.state === "unknown") return create.mutate({ from: attempt, request: attempt.request });
    if (attempt.state === "editing" && activeCredits && selected) {
      create.mutate({
        from: attempt,
        request: { method: selected.method, credits: { amount: activeCredits, currency } },
      });
    }
  };

  return {
    presets,
    amountText,
    inputRef,
    attempt,
    failure,
    creating,
    reset,
    isUnknown,
    isBlocked,
    activeCredits,
    methodsQuery,
    methods,
    selected,
    setChosenMethod,
    applyAmount,
    submit,
  };
}
