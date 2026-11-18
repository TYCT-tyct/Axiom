import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { type Attempt, failed, opened, type Sendable, useErrors } from "@axiom/app";
import {
  Button,
  Cluster,
  Dialog,
  Field,
  FormError,
  moneyInput,
  Note,
  parseMoney,
  Stack,
  SubmitButton,
  TextArea,
} from "@axiom/ui";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { clearAttempt, readAttempt, unanswered, writeAttempt } from "../attempt-store";
import { balanceQuery, ordersQuery } from "../queries";
import type { Order } from "./orders-state";

export type RefundRequest = Schemas["RefundRequest"];

type OrderRefundEntry = {
  attempt: Attempt<RefundRequest>;
  sentRequest: RefundRequest | null;
  failure: unknown;
  pending: boolean;
};

type RefundVars = {
  orderId: string;
  from: Sendable<RefundRequest>;
  request: RefundRequest;
};

const REFUND_STORE = "axiom.console.refund";
const refundKey = (scope: string, orderId: string) => `${scope}/${REFUND_STORE}/${orderId}`;

function isRefundRequest(value: unknown): value is RefundRequest {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Record<string, unknown>;
  if (typeof candidate.reason !== "string" || typeof candidate.credits !== "object" || candidate.credits === null) {
    return false;
  }
  const credits = candidate.credits as Record<string, unknown>;
  return typeof credits.amount === "string" && typeof credits.currency === "string";
}

function initialRefundEntry(scope: string, orderId: string): OrderRefundEntry {
  return {
    attempt: readAttempt(refundKey(scope, orderId), isRefundRequest) ?? opened(crypto.randomUUID()),
    sentRequest: null,
    failure: null,
    pending: false,
  };
}

/** Refund responses belong to the submitted order and key, even if another dialog is open. */
export function useRefundController(scope: string, onSent: () => void) {
  const queryClient = useQueryClient();
  const [activeOrder, setActiveOrder] = useState<Order | null>(null);
  const [entries, setEntries] = useState<Record<string, OrderRefundEntry>>({});
  const activeOrderId = useRef<string | null>(null);
  const pendingKeys = useRef(new Map<string, string>());
  const update = (orderId: string, entry: OrderRefundEntry) => {
    setEntries((prev) => ({ ...prev, [orderId]: entry }));
  };
  const close = () => {
    activeOrderId.current = null;
    setActiveOrder(null);
  };
  const reset = (orderId: string) => {
    if (pendingKeys.current.has(orderId)) return;
    clearAttempt(refundKey(scope, orderId));
    update(orderId, initialRefundEntry(scope, orderId));
  };
  const mutation = useMutation({
    mutationFn: ({ orderId, from, request }: RefundVars) =>
      unwrap(
        consoleApi.POST("/api/console/v1/orders/{order_id}/refunds", {
          params: { path: { order_id: orderId }, header: { "Idempotency-Key": from.key } },
          body: request,
        }),
      ),
    onSuccess: (_data, { orderId, from }) => {
      if (pendingKeys.current.get(orderId) !== from.key) return;
      pendingKeys.current.delete(orderId);
      reset(orderId);
      void queryClient.invalidateQueries({ queryKey: ordersQuery.queryKey });
      void queryClient.invalidateQueries({ queryKey: balanceQuery.queryKey });
      onSent();
      if (activeOrderId.current === orderId) close();
    },
    onError: (failure, { orderId, from, request }) => {
      if (pendingKeys.current.get(orderId) !== from.key) return;
      pendingKeys.current.delete(orderId);
      const attempt = failed(from, request, failure, () => crypto.randomUUID());
      writeAttempt(refundKey(scope, orderId), attempt);
      update(orderId, { attempt, sentRequest: request, failure, pending: false });
    },
  });
  const submit = ({ orderId, from, request }: RefundVars) => {
    if (pendingKeys.current.has(orderId)) return;
    pendingKeys.current.set(orderId, from.key);
    writeAttempt(refundKey(scope, orderId), unanswered(from, request));
    update(orderId, { attempt: from, sentRequest: request, failure: null, pending: true });
    mutation.mutate({ orderId, from, request });
  };
  const open = (order: Order) => {
    activeOrderId.current = order.id;
    setActiveOrder(order);
    if (!entries[order.id]) update(order.id, initialRefundEntry(scope, order.id));
  };
  const entry = activeOrder && entries[activeOrder.id];
  const active = activeOrder && entry ? { order: activeOrder, ...entry } : null;
  return { active, open, close, submit, reset };
}

function initialRefundDraft(order: Order, attempt: Attempt<RefundRequest>, sentRequest: RefundRequest | null) {
  if (attempt.state === "unknown") return attempt.request;
  if (sentRequest) return sentRequest;
  return { credits: order.credits, reason: "" };
}

function useRefundDraft({
  order,
  attempt,
  sentRequest,
  pending,
  onSubmit,
}: {
  order: Order;
  attempt: Attempt<RefundRequest>;
  sentRequest: RefundRequest | null;
  pending: boolean;
  onSubmit: (vars: RefundVars) => void;
}) {
  const seed = initialRefundDraft(order, attempt, sentRequest);
  const [creditsText, setCreditsText] = useState(() => moneyInput(seed.credits.amount));
  const [reasonText, setReasonText] = useState(() => seed.reason);
  const parsedCredits = parseMoney(creditsText);
  const creditsInvalid = parsedCredits === null || BigInt(parsedCredits) <= 0n;
  const isUnknown = attempt.state === "unknown";
  const isBlocked = attempt.state === "reused" || attempt.state === "stale";

  const handleFormSubmit = () => {
    if (pending) return;
    if (isUnknown) return onSubmit({ orderId: order.id, from: attempt, request: attempt.request });
    const reason = reasonText.trim();
    if (attempt.state !== "editing" || creditsInvalid || !parsedCredits || !reason) return;
    onSubmit({
      orderId: order.id,
      from: attempt,
      request: { credits: { amount: parsedCredits, currency: order.credits.currency }, reason },
    });
  };

  return {
    creditsText,
    setCreditsText,
    reasonText,
    setReasonText,
    isUnknown,
    isBlocked,
    locked: pending || isUnknown || isBlocked,
    submitDisabled: pending || (!isUnknown && (creditsInvalid || !reasonText.trim())),
    handleFormSubmit,
  };
}

function RefundFormFields({
  order,
  attempt,
  sentRequest,
  failure,
  pending,
  onCancel,
  onResetAndClose,
  onSubmit,
}: {
  order: Order;
  attempt: Attempt<RefundRequest>;
  sentRequest: RefundRequest | null;
  failure: unknown;
  pending: boolean;
  onCancel: () => void;
  onResetAndClose: () => void;
  onSubmit: (vars: RefundVars) => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const draft = useRefundDraft({ order, attempt, sentRequest, pending, onSubmit });
  const formError = failure ? errors.message(failure) : undefined;

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        draft.handleFormSubmit();
      }}
    >
      <Stack gap={12}>
        {draft.isUnknown ? <Note title={t("orders.refundUnknown")} /> : null}
        {draft.isBlocked ? <FormError>{t("orders.refundReused")}</FormError> : null}
        <Field
          label={t("orders.refundCredits")}
          hint={t("orders.refundCreditsHint", { currency: order.credits.currency })}
          inputMode="decimal"
          disabled={draft.locked}
          value={draft.creditsText}
          error={failure ? errors.field(failure, "credits") : undefined}
          onChange={(event) => draft.setCreditsText(event.target.value)}
        />
        <TextArea
          label={t("orders.refundReason")}
          hint={t("orders.refundReasonHint")}
          maxLength={1000}
          disabled={draft.locked}
          value={draft.reasonText}
          error={failure ? errors.field(failure, "reason") : undefined}
          onChange={(event) => draft.setReasonText(event.target.value)}
        />
        {formError ? <FormError>{formError}</FormError> : null}
        <Cluster gap={8} justify="end">
          <Button variant="ghost" onClick={draft.isBlocked ? onResetAndClose : onCancel}>
            {t("orders.close")}
          </Button>
          {draft.isBlocked ? null : (
            <SubmitButton pending={pending} disabled={draft.submitDisabled}>
              {t(draft.isUnknown ? "orders.refundRetry" : "orders.refundSubmit")}
            </SubmitButton>
          )}
        </Cluster>
      </Stack>
    </form>
  );
}

export function RefundDialog({
  order,
  attempt,
  sentRequest,
  failure,
  pending,
  onSubmit,
  onReset,
  onClose,
}: {
  order: Order;
  attempt: Attempt<RefundRequest>;
  sentRequest: RefundRequest | null;
  failure: unknown;
  pending: boolean;
  onSubmit: (vars: RefundVars) => void;
  onReset: (orderId: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();

  return (
    <Dialog title={t("orders.refundTitle")} onClose={onClose}>
      <Stack gap={16}>
        <p>{t("orders.refundBody")}</p>
        <RefundFormFields
          order={order}
          attempt={attempt}
          sentRequest={sentRequest}
          failure={failure}
          pending={pending}
          onCancel={onClose}
          onResetAndClose={() => {
            onReset(order.id);
            onClose();
          }}
          onSubmit={onSubmit}
        />
      </Stack>
    </Dialog>
  );
}
