import { type Attempt, useErrors } from "@axiom/app";
import BillPayDesktop, { type Props as BillPayProps } from "@axiom/boards/AxApp/regions/BillPay";
import BillPayEn from "@axiom/boards/AxApp/regions/BillPay.en";
import BillPayZh from "@axiom/boards/AxApp/regions/BillPay.zh";
import BillPayPhone from "@axiom/boards/AxAppPhone/regions/BillPay";
import { BoardDialog, Button, DataState, Dialog, FormError, Stack, useFormat } from "@axiom/ui";
import type { UseQueryResult } from "@tanstack/react-query";
import { type FormEvent, useEffect, useId, useRef } from "react";
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
  type ActiveOrderState,
  type Order,
  type OrderRequest,
  parsePositiveCredits,
  type useBillTopUpSession,
  useStepOneController,
} from "./bill-topup-state";

type TopUpSession = ReturnType<typeof useBillTopUpSession>;

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
  if (c.attempt.state === "unknown") {
    return t("topup.retrySummary", {
      credits: format.money(c.attempt.request.credits),
      method: c.attempt.request.method,
    });
  }
  if (c.activeCredits === null) return "—";
  if (c.methodsQuery.error) {
    return t("statement.bill.topUpModal.methodsError", { reason: errors.message(c.methodsQuery.error) });
  }
  if (c.methodsQuery.isPending) return t("statement.bill.topUpModal.checkingMethods");
  if (!c.selected) {
    return c.methods.length > 0 ? t("statement.bill.topUpModal.chooseMethod") : t("topup.noMethods");
  }
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
  currentBalance: Money | undefined,
  c: ReturnType<typeof useStepOneController>,
): BillPayProps["text"] {
  const { t } = useTranslation();
  const format = useFormat();
  const feeTotalText = useFeeTotalText(c);
  const creditsForPreview = c.attempt.state === "unknown" ? c.attempt.request.credits.amount : c.activeCredits;
  const projected = projectedBalanceAfter(currentBalance, creditsForPreview);
  const preview1Value = !currentBalance ? t("orders.unavailable") : projected ? format.money(projected) : "—";
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
    preview1Value,
    previewLabel2: t("statement.bill.topUpModal.feeAndTotal"),
    preview2Date: feeTotalText,
    preview2Extra: "",
    methodsLabel: t("statement.bill.topUpModal.methodLabel"),
    cancelLabel: t("orders.close"),
    submitLabel,
  };
}

function clearInnerDialogRole(node: Element | null) {
  if (!node) return;
  node.removeAttribute("role");
  node.removeAttribute("aria-modal");
  node.removeAttribute("aria-labelledby");
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
}): BillPayProps["values"] {
  return {
    overlayOnly: true,
    unbuilt: true,
    panelRef: clearInnerDialogRole,
    titleId,
    closeLabel,
    amountLabel,
    amountPlaceholder,
    amountClass: hasInvalidAmount ? "bad" : "",
    amountInput: c.attachInput,
    amountChanged: (event: FormEvent<Element>) => {
      if (c.locked) {
        (event.target as HTMLInputElement).value = c.amountText;
        return;
      }
      c.applyAmount((event.target as HTMLInputElement).value);
    },
    amountKeyDown: (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        if (canSubmit) c.submit();
      }
    },
    quickClick: (event) => {
      if (c.locked) return;
      const units = (event.target as Element).closest("button[data-q]")?.getAttribute("data-q");
      if (units) c.applyAmount(units);
    },
    methodClick: (event) => {
      if (c.locked) return;
      const method = (event.target as Element).closest("button[data-m]")?.getAttribute("data-m");
      if (method) c.setChosenMethod(method);
    },
    close: onClose,
    hideQuick: c.locked,
    hideMethods: c.methods.length === 0,
    hideSubmit: c.isBlocked,
    submitDisabled: !canSubmit,
    submit: () => {
      if (canSubmit) c.submit();
    },
  };
}

function StepOneModal(props: {
  currency: string;
  currentBalance: Money | undefined;
  initialUnits?: string;
  onClose: () => void;
  placement: TopUpSession["placement"];
}) {
  const { t } = useTranslation();
  const billPayT = useBillPayTranslator();
  const format = useFormat();
  const boardKind = useBoardKind();
  const titleId = useId();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const c = useStepOneController(props.currency, props.initialUnits, props.placement);
  const text = useStepOneTexts(props.currency, props.currentBalance, c);
  const hasInvalidAmount = c.amountText.trim().length > 0 && parsePositiveCredits(c.amountText) === null;
  const canSubmit = !c.creating && (c.isUnknown || (!c.isBlocked && c.activeCredits !== null && Boolean(c.selected)));
  const BillPayRegion = boardKind === "phone" ? BillPayPhone : BillPayDesktop;
  const requestClose = () => {
    if (dialogRef.current?.open) dialogRef.current.close();
    else props.onClose();
  };
  return (
    <BoardDialog labelId={titleId} dialogRef={dialogRef} initialFocusRef={c.inputRef} onClose={props.onClose}>
      <BillPayRegion
        t={billPayT}
        rows={{
          quick0: buildQuickRows(c.presets, c.amountText, props.currency, format.money),
          methods0: buildMethodRows(c.methods, c.selected?.method ?? null, format.pay, (fee) =>
            t("orders.fee", { fee }),
          ),
        }}
        text={text}
        error={
          <AttemptFeedback
            attempt={c.attempt}
            failure={c.failure}
            hasInvalidAmount={hasInvalidAmount}
            creating={c.creating}
            onReset={c.reset}
          />
        }
        values={buildStepOneValues({
          c,
          titleId,
          hasInvalidAmount,
          canSubmit,
          onClose: requestClose,
          closeLabel: t("orders.dismiss"),
          amountLabel: t("statement.bill.topUpModal.amountLabel"),
          amountPlaceholder: t("statement.bill.topUpModal.amountPlaceholder"),
        })}
      />
    </BoardDialog>
  );
}

function AttemptFeedback({
  attempt,
  failure,
  hasInvalidAmount,
  creating,
  onReset,
}: {
  attempt: Attempt<OrderRequest>;
  failure: unknown;
  hasInvalidAmount: boolean;
  creating: boolean;
  onReset: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  if (creating) return <span role="status">{t("statement.bill.topUpModal.creating")}</span>;
  const unknown = attempt.state === "unknown";
  if (unknown || attempt.state === "reused" || attempt.state === "stale") {
    return (
      <Stack gap={4}>
        <FormError>{t(unknown ? "topup.unknown" : "topup.reused")}</FormError>
        {unknown ? <span>{t("statement.bill.topUpModal.idempotentNotice")}</span> : null}
        <Button variant="ghost" size="sm" onClick={onReset}>
          {t("topup.new")}
        </Button>
      </Stack>
    );
  }
  if (hasInvalidAmount) return <FormError>{t("statement.bill.topUpModal.amountInvalid")}</FormError>;
  const message = failure ? errors.message(failure) : undefined;
  return message ? <FormError>{message}</FormError> : null;
}

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
  activeOrder: TopUpSession["activeOrder"];
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
  currencyQuery,
  currentBalance,
  initialUnits,
  session,
  onClose,
}: {
  open: boolean;
  currency: string | undefined;
  currencyQuery?: UseQueryResult<unknown, unknown>;
  currentBalance: Money | undefined;
  initialUnits?: string;
  session: TopUpSession;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const { active, activeOrder, selectActive, placement } = session;

  useEffect(() => {
    if (!open || !active) return;
    const state = activeOrder.liveOrder?.state;
    if (!active.created && initialUnits && state && state !== "pending" && state !== "review") {
      selectActive(null);
    }
  }, [open, active, initialUnits, activeOrder.liveOrder?.state, selectActive]);

  if (!open) return null;
  if (active) {
    return (
      <StepTwoDialog active={active} activeOrder={activeOrder} onClose={onClose} onRestart={() => selectActive(null)} />
    );
  }
  if (!currency) {
    return (
      <Dialog title={t("topup.title")} onClose={onClose}>
        {currencyQuery ? <DataState query={currencyQuery}>{() => null}</DataState> : null}
      </Dialog>
    );
  }
  return (
    <StepOneModal
      currency={currency}
      currentBalance={currentBalance}
      initialUnits={initialUnits}
      onClose={onClose}
      placement={placement}
    />
  );
}
