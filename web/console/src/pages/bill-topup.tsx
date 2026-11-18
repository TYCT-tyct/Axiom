import { type Attempt, useErrors } from "@axiom/app";
import BillPayDesktop, { type Props as BillPayProps } from "@axiom/boards/AxApp/regions/BillPay";
import BillPayEn from "@axiom/boards/AxApp/regions/BillPay.en";
import BillPayZh from "@axiom/boards/AxApp/regions/BillPay.zh";
import BillPayPhone from "@axiom/boards/AxAppPhone/regions/BillPay";
import { Button, DataState, Dialog, FormError, Stack, useFormat } from "@axiom/ui";
import { type FormEvent, useEffect, useId, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { useOrderActions } from "../order-actions";
import { mergeOrder } from "../order-merge";
import { OrderStatus } from "../order-status";
import {
  currencyInputPrefix,
  formatQuickAmount,
  type Money,
  type PaymentMethodQuote,
  projectedBalanceAfter,
  useBoardKind,
} from "./bill-data";
import {
  type Order,
  type OrderRequest,
  parsePositiveCredits,
  readStoredOrderId,
  useActiveOrder,
  useBillScope,
  useStepOneController,
  writeStoredOrderId,
} from "./bill-topup-state";

function useBillPayTranslator() {
  const { i18n } = useTranslation();
  const isEnglish = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  const messages = (isEnglish ? BillPayEn : BillPayZh) as Record<string, string>;
  return (key: keyof typeof BillPayZh) => messages[key] ?? "";
}

function buildQuickRows(
  presets: readonly string[],
  activeUnits: string,
  currency: string,
  formatMoney: (value: Money) => string,
): BillPayProps["rows"]["quick0"] {
  const normalized = activeUnits.trim();
  return presets.map((units) => ({
    key: units,
    kind: "1" as const,
    f0: "button" as const,
    f1: units,
    f2: normalized === units ? "on" : "",
    f3: formatQuickAmount(units, currency, formatMoney),
  }));
}

function buildMethodRows(
  methods: readonly PaymentMethodQuote[],
  selectedMethod: string | null,
  formatPay: ReturnType<typeof useFormat>["pay"],
  feeLabel: (fee: string) => string,
): BillPayProps["rows"]["methods0"] {
  return methods.map((item) => ({
    key: item.method,
    kind: "1" as const,
    f0: "button" as const,
    f1: item.method === selectedMethod ? "pym on" : "pym",
    f2: item.method,
    f3: "pymi",
    f4: item.method.slice(0, 1).toUpperCase(),
    f5: "pymt",
    f6: item.method,
    f7: BigInt(item.fee.amount) > 0n ? feeLabel(formatPay(item.fee)) : formatPay(item.total),
    f8: "pyrd",
  }));
}

function useFeeTotalText(c: ReturnType<typeof useStepOneController>) {
  const { t } = useTranslation();
  const format = useFormat();
  const errors = useErrors();
  if (c.activeCredits === null) return "—";
  if (c.methodsQuery.error) {
    return t("statement.bill.topUpModal.methodsError", { reason: errors.message(c.methodsQuery.error) });
  }
  if (c.methodsQuery.isPending) return t("statement.bill.topUpModal.checkingMethods");
  if (!c.selected) return t("topup.noMethods");
  if (BigInt(c.selected.fee.amount) > 0n) {
    return t("statement.bill.topUpModal.feeLine", {
      total: format.pay(c.selected.total),
      fee: format.pay(c.selected.fee),
    });
  }
  return t("statement.bill.topUpModal.feeZero", { total: format.pay(c.selected.total) });
}

function useStepOneTexts(
  currency: string,
  currentBalance: Money,
  c: ReturnType<typeof useStepOneController>,
): BillPayProps["text"] {
  const { t } = useTranslation();
  const format = useFormat();
  const feeTotalText = useFeeTotalText(c);
  const projected = projectedBalanceAfter(currentBalance, c.activeCredits);
  const submitLabel = c.isUnknown
    ? t("topup.retry")
    : c.selected
      ? t("statement.bill.topUpModal.paySubmit", { total: format.pay(c.selected.total) })
      : t("topup.submit");
  return {
    title: t("statement.bill.topUpModal.title"),
    subtitle: t("statement.bill.topUpModal.subtitle"),
    currencyPrefix: currencyInputPrefix(currency),
    previewLabel1: t("statement.bill.topUpModal.afterBalance"),
    preview1Value: projected ? format.money(projected) : "—",
    previewLabel2: t("statement.bill.topUpModal.feeAndTotal"),
    preview2Date: feeTotalText,
    preview2Extra: "",
    methodsLabel: t("statement.bill.topUpModal.methodLabel"),
    cancelLabel: t("orders.close"),
    submitLabel,
  };
}

function buildStepOneValues({
  c,
  titleId,
  hasInvalidAmount,
  canSubmit,
  onClose,
  closeLabel,
  amountLabel,
  amountPlaceholder,
}: {
  c: ReturnType<typeof useStepOneController>;
  titleId: string;
  hasInvalidAmount: boolean;
  canSubmit: boolean;
  onClose: () => void;
  closeLabel: string;
  amountLabel: string;
  amountPlaceholder: string;
}): NonNullable<BillPayProps["values"]> {
  const editable = !c.isUnknown && !c.isBlocked;
  return {
    overlayOnly: true,
    overlayStyle: "position: fixed",
    unbuilt: true,
    titleId,
    closeLabel,
    close: onClose,
    amountClass: hasInvalidAmount ? "bad" : "",
    amountLabel,
    amountPlaceholder,
    amountInput: (node) => {
      const el = node as HTMLInputElement | null;
      c.inputRef.current = el;
      if (el) {
        if (el.value !== c.amountText) el.value = c.amountText;
        el.readOnly = !editable;
      }
    },
    amountChanged: (event: FormEvent<Element>) => {
      const el = event.target as HTMLInputElement;
      if (editable) c.applyAmount(el.value);
      else el.value = c.amountText;
    },
    amountKeyDown: (event) => event.key === "Enter" && canSubmit && c.submit(),
    quickClick: (event) => {
      const units = editable ? (event.target as Element).closest("button[data-q]")?.getAttribute("data-q") : null;
      if (units) c.applyAmount(units);
    },
    hideQuick: !editable,
    hideMethods: c.isBlocked || c.methods.length === 0,
    methodClick: (event) => {
      const next = editable ? (event.target as Element).closest("button[data-m]")?.getAttribute("data-m") : null;
      if (next) c.setChosenMethod(next);
    },
    hideSubmit: c.isBlocked,
    submitDisabled: !canSubmit || c.creating,
    submit: c.submit,
  };
}

function StepOneModal(props: {
  currency: string;
  currentBalance: Money;
  initialUnits?: string;
  onClose: () => void;
  onCreated: (order: Order) => void;
}) {
  const { t } = useTranslation();
  const billPayT = useBillPayTranslator();
  const format = useFormat();
  const boardKind = useBoardKind();
  const titleId = useId();
  const c = useStepOneController(props);
  useEffect(() => {
    c.inputRef.current?.focus();
  }, [c.inputRef]);
  const text = useStepOneTexts(props.currency, props.currentBalance, c);
  const hasInvalidAmount = c.amountText.trim().length > 0 && parsePositiveCredits(c.amountText) === null;
  const canSubmit = c.isUnknown || (!c.isBlocked && !c.creating && c.activeCredits !== null && Boolean(c.selected));
  const BillPayRegion = boardKind === "phone" ? BillPayPhone : BillPayDesktop;
  const content = (
    <BillPayRegion
      t={billPayT}
      rows={{
        quick0: buildQuickRows(c.presets, c.amountText, props.currency, format.money),
        methods0: buildMethodRows(c.methods, c.selected?.method ?? null, format.pay, (fee) => t("orders.fee", { fee })),
      }}
      text={text}
      error={
        <AttemptFeedback
          attempt={c.attempt}
          failure={c.failure}
          hasInvalidAmount={hasInvalidAmount}
          onReset={c.reset}
        />
      }
      footerLeft={
        c.isUnknown || c.isBlocked ? (
          <Button variant="ghost" size="sm" onClick={c.reset}>
            {t("topup.new")}
          </Button>
        ) : undefined
      }
      values={buildStepOneValues({
        c,
        titleId,
        hasInvalidAmount,
        canSubmit,
        onClose: props.onClose,
        closeLabel: t("orders.dismiss"),
        amountLabel: t("statement.bill.topUpModal.amountLabel"),
        amountPlaceholder: t("statement.bill.topUpModal.amountPlaceholder"),
      })}
    />
  );
  const frame = typeof document !== "undefined" ? document.querySelector(".fr") : null;
  return frame ? createPortal(content, frame) : content;
}

function AttemptFeedback({
  attempt,
  failure,
  hasInvalidAmount,
  onReset,
}: {
  attempt: Attempt<OrderRequest>;
  failure: unknown;
  hasInvalidAmount: boolean;
  onReset: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  if (attempt.state === "unknown") {
    return (
      <Stack gap={4}>
        <FormError>{t("topup.unknown")}</FormError>
        <span>{t("statement.bill.topUpModal.idempotentNotice")}</span>
      </Stack>
    );
  }
  if (attempt.state === "reused" || attempt.state === "stale") {
    return (
      <Stack gap={4}>
        <FormError>{t("topup.reused")}</FormError>
        <Button variant="secondary" size="sm" onClick={onReset}>
          {t("topup.new")}
        </Button>
      </Stack>
    );
  }
  if (hasInvalidAmount) return <FormError>{t("statement.bill.topUpModal.amountInvalid")}</FormError>;
  const message = failure ? errors.message(failure) : undefined;
  return message ? <FormError>{message}</FormError> : null;
}

type ActiveOrderState = { id: string; created: Order | null };

function StepTwoOrderView({
  order,
  verifyError,
  cancelError,
  queryError,
  verifying,
  cancelling,
  onVerify,
  onCancel,
  onRestart,
}: {
  order: Order;
  verifyError: unknown;
  cancelError: unknown;
  queryError: unknown;
  verifying: boolean;
  cancelling: boolean;
  onVerify: () => void;
  onCancel: () => void;
  onRestart: () => void;
}) {
  const errors = useErrors();
  const actionError = verifyError ?? cancelError ?? queryError;
  return (
    <Stack gap={12}>
      <OrderStatus
        order={order}
        verifying={verifying}
        cancelling={cancelling}
        onVerify={onVerify}
        onCancel={onCancel}
        onRestart={onRestart}
      />
      {actionError ? <FormError>{errors.message(actionError)}</FormError> : null}
    </Stack>
  );
}

function StepTwoDialog({
  active,
  activeOrder,
  onClose,
  onRestart,
}: {
  active: ActiveOrderState;
  activeOrder: ReturnType<typeof useActiveOrder>;
  onClose: () => void;
  onRestart: () => void;
}) {
  const { t } = useTranslation();
  const { verify, cancel } = useOrderActions(active.id);
  const shown = mergeOrder(active.created, activeOrder.liveOrder);
  const handleClose = () => {
    if (shown && shown.state !== "pending" && shown.state !== "review") onRestart();
    onClose();
  };
  return (
    <Dialog title={t("topup.title")} onClose={handleClose}>
      {shown ? (
        <StepTwoOrderView
          order={shown}
          verifyError={verify.error}
          cancelError={cancel.error}
          queryError={activeOrder.query.error}
          verifying={verify.isPending}
          cancelling={cancel.isPending}
          onVerify={() => verify.mutate()}
          onCancel={() => cancel.mutate()}
          onRestart={onRestart}
        />
      ) : (
        <DataState query={activeOrder.query}>
          {(order) => (
            <StepTwoOrderView
              order={order}
              verifyError={verify.error}
              cancelError={cancel.error}
              queryError={null}
              verifying={verify.isPending}
              cancelling={cancel.isPending}
              onVerify={() => verify.mutate()}
              onCancel={() => cancel.mutate()}
              onRestart={onRestart}
            />
          )}
        </DataState>
      )}
    </Dialog>
  );
}

export function BillTopUpModal({
  open,
  currency,
  currentBalance,
  initialUnits,
  onClose,
}: {
  open: boolean;
  currency: string;
  currentBalance: Money;
  initialUnits?: string;
  onClose: () => void;
}) {
  const scope = useBillScope();
  const [active, setActive] = useState<ActiveOrderState | null>(() => {
    const saved = readStoredOrderId(scope);
    return saved ? { id: saved, created: null } : null;
  });
  const activeOrder = useActiveOrder(open ? (active?.id ?? null) : null);
  const selectActive = (next: ActiveOrderState | null) => {
    writeStoredOrderId(scope, next?.id ?? null);
    setActive(next);
  };

  useEffect(() => {
    if (!open || !active) return;
    if (activeOrder.isNotFound) {
      writeStoredOrderId(scope, null);
      setActive(null);
      return;
    }
    const state = activeOrder.liveOrder?.state;
    if (!active.created && initialUnits && state && state !== "pending" && state !== "review") {
      writeStoredOrderId(scope, null);
      setActive(null);
    }
  }, [open, active, initialUnits, activeOrder.isNotFound, activeOrder.liveOrder?.state, scope]);

  if (!open) return null;
  if (active) {
    return (
      <StepTwoDialog active={active} activeOrder={activeOrder} onClose={onClose} onRestart={() => selectActive(null)} />
    );
  }
  return (
    <StepOneModal
      currency={currency}
      currentBalance={currentBalance}
      initialUnits={initialUnits}
      onClose={onClose}
      onCreated={(order) => selectActive({ id: order.id, created: order })}
    />
  );
}
