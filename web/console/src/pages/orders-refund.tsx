import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { type Attempt, failedFrom, opened, type Sendable, useErrors } from "@axiom/app";
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
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { clearAttempt, readAttempt, unanswered, writeAttempt } from "../attempt-store";
import { balanceQuery, ordersQuery } from "../queries";

type Order = Schemas["Order"];
type RefundRequest = { credits: { amount: string; currency: string }; reason: string };

const refundKey = (scope: string, orderId: string) => `${scope}/axiom.console.refund.${orderId}`;
const isRefundRequest = (value: unknown): value is RefundRequest => {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Record<string, unknown>;
  if (typeof candidate.reason !== "string" || typeof candidate.credits !== "object" || candidate.credits === null) {
    return false;
  }
  const credits = candidate.credits as Record<string, unknown>;
  return typeof credits.amount === "string" && typeof credits.currency === "string";
};

export function useRefundController(scope: string) {
  const [target, setTarget] = useState<{ order: Order; attempt: Attempt<RefundRequest> } | null>(null);
  const open = (order: Order) => {
    const saved = readAttempt(refundKey(scope, order.id), isRefundRequest);
    setTarget({ order, attempt: saved ?? opened(crypto.randomUUID()) });
  };
  const close = () => setTarget(null);
  const update = (next: Attempt<RefundRequest>) => {
    setTarget((prev) => {
      if (!prev) return null;
      writeAttempt(refundKey(scope, prev.order.id), next);
      return { order: prev.order, attempt: next };
    });
  };
  const reset = () => {
    setTarget((prev) => {
      if (!prev) return null;
      clearAttempt(refundKey(scope, prev.order.id));
      return { order: prev.order, attempt: opened(crypto.randomUUID()) };
    });
  };
  return { target, open, close, update, reset };
}

function useRefundMutation(orderId: string, onAttempt: (next: Attempt<RefundRequest>) => void, onDone: () => void) {
  const queryClient = useQueryClient();
  const [failure, setFailure] = useState<unknown>(null);
  const mutation = useMutation({
    mutationFn: ({ from, request }: { from: Sendable<RefundRequest>; request: RefundRequest }) =>
      unwrap(
        consoleApi.POST("/api/console/v1/orders/{order_id}/refunds", {
          params: { path: { order_id: orderId }, header: { "Idempotency-Key": from.key } },
          body: request,
        }),
      ),
    onMutate: ({ from, request }) => {
      setFailure(null);
      onAttempt(unanswered(from, request));
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ordersQuery.queryKey });
      void queryClient.invalidateQueries({ queryKey: balanceQuery.queryKey });
      onDone();
    },
    onError: (error, { from, request }) => failedFrom(onAttempt, setFailure)(error, { from, request }),
  });
  return { mutation, failure };
}

function RefundFormFields({
  order,
  failure,
  pending,
  onCancel,
  onSubmit,
}: {
  order: Order;
  failure: unknown;
  pending: boolean;
  onCancel: () => void;
  onSubmit: (request: RefundRequest) => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [creditsText, setCreditsText] = useState(() => moneyInput(order.credits.amount));
  const [reasonText, setReasonText] = useState("");
  const parsedCredits = parseMoney(creditsText);
  const creditsInvalid = parsedCredits === null || BigInt(parsedCredits) <= 0n;
  const formError = failure ? errors.message(failure) : undefined;

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        if (creditsInvalid || !parsedCredits) return;
        onSubmit({ credits: { amount: parsedCredits, currency: order.credits.currency }, reason: reasonText.trim() });
      }}
    >
      <Stack gap={12}>
        <Field
          label={t("orders.refundCredits")}
          hint={t("orders.refundCreditsHint", { currency: order.credits.currency })}
          inputMode="decimal"
          value={creditsText}
          error={failure ? errors.field(failure, "credits") : undefined}
          onChange={(event) => setCreditsText(event.target.value)}
        />
        <TextArea
          label={t("orders.refundReason")}
          hint={t("orders.refundReasonHint")}
          maxLength={1000}
          value={reasonText}
          error={failure ? errors.field(failure, "reason") : undefined}
          onChange={(event) => setReasonText(event.target.value)}
        />
        {formError ? <FormError>{formError}</FormError> : null}
        <Cluster gap={8} justify="end">
          <Button variant="ghost" onClick={onCancel}>
            {t("orders.close")}
          </Button>
          <SubmitButton pending={pending} disabled={creditsInvalid || !reasonText.trim()}>
            {t("orders.refundSubmit")}
          </SubmitButton>
        </Cluster>
      </Stack>
    </form>
  );
}

export function RefundDialog({
  order,
  attempt,
  onAttempt,
  onReset,
  onClose,
  onSent,
}: {
  order: Order;
  attempt: Attempt<RefundRequest>;
  onAttempt: (next: Attempt<RefundRequest>) => void;
  onReset: () => void;
  onClose: () => void;
  onSent: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const { mutation, failure } = useRefundMutation(order.id, onAttempt, () => {
    onReset();
    onSent();
    onClose();
  });

  return (
    <Dialog title={t("orders.refundTitle")} onClose={onClose}>
      <Stack gap={16}>
        <p>{t("orders.refundBody")}</p>
        {attempt.state === "unknown" ? (
          <Stack gap={8}>
            <Note title={t("orders.refundUnknown")} />
            {failure ? <FormError>{errors.message(failure)}</FormError> : null}
            <Cluster gap={8}>
              <SubmitButton
                pending={mutation.isPending}
                onClick={() => mutation.mutate({ from: attempt, request: attempt.request })}
              >
                {t("orders.refundRetry")}
              </SubmitButton>
              <Button variant="ghost" onClick={onClose}>
                {t("orders.close")}
              </Button>
            </Cluster>
          </Stack>
        ) : attempt.state === "reused" || attempt.state === "stale" ? (
          <Stack gap={8}>
            <FormError>{t("orders.refundReused")}</FormError>
            <Button
              variant="secondary"
              onClick={() => {
                onReset();
                onClose();
              }}
            >
              {t("orders.close")}
            </Button>
          </Stack>
        ) : (
          <RefundFormFields
            order={order}
            failure={failure}
            pending={mutation.isPending}
            onCancel={onClose}
            onSubmit={(request) => mutation.mutate({ from: attempt, request })}
          />
        )}
      </Stack>
    </Dialog>
  );
}
