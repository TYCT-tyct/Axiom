import type { AdminSchemas } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, CheckField, Field, Fieldset, Grid, moneyInput, Select, Stack, Text } from "@axiom/ui";
import type React from "react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { paymentCurrencies } from "./credit-read";
import { MerchantSecrets } from "./merchant-secrets";
import { defaultMethodRows, formatPercent, methodFallback } from "./payment-read";

type Merchant = AdminSchemas["Merchant"];
type Draft = {
  key: string;
  method: string;
  upstream: string;
  percent: string;
  fixed: string;
  min: string;
  max: string;
  daily: string;
  enabled: boolean;
};
type Update = (key: string, change: Partial<Draft>) => void;

function draftOf(method: AdminSchemas["MerchantMethod"] | undefined, kind: string): Draft {
  const fallback = methodFallback(kind);
  return {
    key: crypto.randomUUID(),
    method: method?.method ?? fallback.method,
    upstream: method?.upstream ?? fallback.upstream,
    percent: formatPercent(method?.fee_ppm ?? 30_000),
    fixed: method?.fee_fixed ? moneyInput(method.fee_fixed.amount) : "0",
    min: method?.single_min ? moneyInput(method.single_min.amount) : "",
    max: method?.single_max ? moneyInput(method.single_max.amount) : "",
    daily: method?.daily_limit ? moneyInput(method.daily_limit.amount) : "",
    enabled: method?.enabled ?? true,
  };
}

function merchantCurrency(currency: string): string {
  return (paymentCurrencies as readonly string[]).includes(currency) ? currency : "USD";
}

// MerchantFields is the whole form: the account's own fields, its write-only secrets and the method rows, whose keys
// carry the server's JSON pointer for each row.
export function MerchantFields({
  merchant,
  currency,
  error,
}: {
  merchant: Merchant | undefined;
  currency: string;
  error: unknown;
}) {
  const [kind, setKind] = useState(merchant?.kind ?? "easypay");
  const [rows, setRows] = useState<Draft[]>(() =>
    merchant?.methods?.length
      ? merchant.methods.map((method) => draftOf(method, merchant?.kind ?? "easypay"))
      : [draftOf(undefined, merchant?.kind ?? "easypay")],
  );
  const [paidIn, setPaidIn] = useState(merchant?.currency ?? merchantCurrency(currency));
  const changeKind = (next: string) => {
    setKind(next);
    setRows((current) => defaultMethodRows(current, kind, next));
  };
  const update: Update = (key, change) =>
    setRows((current) => current.map((row) => (row.key === key ? { ...row, ...change } : row)));
  return (
    <>
      <MerchantBasics
        merchant={merchant}
        kind={kind}
        setKind={changeKind}
        paidIn={paidIn}
        setPaidIn={setPaidIn}
        error={error}
      />
      <MerchantFlags merchant={merchant} />
      <MerchantSecrets key={kind} merchant={merchant} kind={kind} error={error} />
      <MethodsEditor rows={rows} setRows={setRows} update={update} paidIn={paidIn} kind={kind} error={error} />
    </>
  );
}

function MerchantBasics({
  merchant,
  kind,
  setKind,
  paidIn,
  setPaidIn,
  error,
}: {
  merchant: Merchant | undefined;
  kind: string;
  setKind: (kind: string) => void;
  paidIn: string;
  setPaidIn: (currency: string) => void;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Select
        label={t("settings.payments.kind")}
        hint={t("settings.payments.kindHint")}
        name="kind"
        value={kind}
        onChange={(event) => setKind(event.target.value)}
        disabled={merchant !== undefined}
        error={errors.field(error, "merchant/kind")}
      >
        <option value="easypay">{t("settings.payments.kinds.easypay")}</option>
        <option value="alipay">{t("settings.payments.kinds.alipay")}</option>
        <option value="stripe">{t("settings.payments.kinds.stripe")}</option>
        <option value="wechatpay">{t("settings.payments.kinds.wechatpay")}</option>
      </Select>
      <Field
        label={t("settings.payments.name")}
        name="name"
        autoComplete="off"
        maxLength={100}
        defaultValue={merchant?.name ?? ""}
        error={errors.field(error, "merchant/name")}
      />
      <Select
        label={t("settings.payments.currency")}
        hint={t("settings.payments.currencyHint")}
        name="currency"
        value={paidIn}
        onChange={(event) => setPaidIn(event.target.value)}
        error={errors.field(error, "merchant/currency")}
      >
        {paymentCurrencies.map((code) => (
          <option key={code} value={code}>
            {code}
          </option>
        ))}
      </Select>
      <Field
        label={t("settings.payments.ttl")}
        hint={t("settings.payments.ttlHint")}
        name="ttl_seconds"
        inputMode="numeric"
        autoComplete="off"
        defaultValue={merchant?.ttl_seconds ?? 1800}
        error={errors.field(error, "merchant/ttl_seconds")}
      />
      <Field
        label={t("settings.payments.sort")}
        hint={t("settings.payments.sortHint")}
        name="sort"
        inputMode="numeric"
        autoComplete="off"
        defaultValue={merchant?.sort ?? 0}
        error={errors.field(error, "merchant/sort")}
      />
    </>
  );
}

function MerchantFlags({ merchant }: { merchant: Merchant | undefined }) {
  const { t } = useTranslation();
  return (
    <>
      <CheckField
        label={t("settings.payments.enabledLabel")}
        hint={t("settings.payments.enabledHint")}
        name="enabled"
        value="true"
        defaultChecked={merchant?.enabled ?? true}
      />
      <CheckField
        label={t("settings.payments.refundEnabled")}
        name="refund_enabled"
        value="true"
        defaultChecked={merchant?.refund_enabled ?? false}
      />
      <CheckField
        label={t("settings.payments.userRefund")}
        name="user_refund"
        value="true"
        defaultChecked={merchant?.user_refund ?? false}
      />
    </>
  );
}

function MethodsEditor({
  rows,
  setRows,
  update,
  paidIn,
  kind,
  error,
}: {
  rows: Draft[];
  setRows: React.Dispatch<React.SetStateAction<Draft[]>>;
  update: Update;
  paidIn: string;
  kind: string;
  error: unknown;
}) {
  const { t } = useTranslation();
  return (
    <Stack gap={12}>
      <Text medium tone="muted">
        {t("settings.payments.methods")}
      </Text>
      {rows.map((row, index) => (
        <MethodRow
          key={row.key}
          row={row}
          index={index}
          update={update}
          paidIn={paidIn}
          error={error}
          removable={rows.length > 1}
          onRemove={() => setRows((current) => current.filter((entry) => entry.key !== row.key))}
        />
      ))}
      <ActionButton size="sm" onClick={() => setRows((current) => [...current, draftOf(undefined, kind)])} icon="plus">
        {t("settings.payments.addMethod")}
      </ActionButton>
    </Stack>
  );
}

function MethodRow({
  row,
  index,
  update,
  paidIn,
  error,
  removable,
  onRemove,
}: {
  row: Draft;
  index: number;
  update: Update;
  paidIn: string;
  error: unknown;
  removable: boolean;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Fieldset legend={t("settings.payments.method", { number: index + 1 })}>
      <Stack gap={12}>
        <MethodCodes row={row} index={index} update={update} error={error} />
        <MethodMoneyFields row={row} index={index} update={update} paidIn={paidIn} error={error} />
        <CheckField
          label={t("settings.payments.methodEnabled")}
          name={`method-${index}-enabled`}
          value="true"
          checked={row.enabled}
          onChange={(event) => update(row.key, { enabled: event.target.checked })}
        />
        {removable ? (
          <ActionButton size="sm" variant="gho" onClick={onRemove} icon="trash">
            {t("settings.payments.removeMethod")}
          </ActionButton>
        ) : null}
      </Stack>
    </Fieldset>
  );
}

function MethodCodes({ row, index, update, error }: { row: Draft; index: number; update: Update; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <Grid min="16rem" gap={12}>
      <Field
        label={t("settings.payments.methodCode")}
        name={`method-${index}-method`}
        autoComplete="off"
        value={row.method}
        onChange={(event) => update(row.key, { method: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/method`)}
      />
      <Field
        label={t("settings.payments.upstream")}
        name={`method-${index}-upstream`}
        autoComplete="off"
        value={row.upstream}
        onChange={(event) => update(row.key, { upstream: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/upstream`)}
      />
    </Grid>
  );
}

function MethodMoneyFields({
  row,
  index,
  update,
  paidIn,
  error,
}: {
  row: Draft;
  index: number;
  update: Update;
  paidIn: string;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <Grid min="16rem" gap={12}>
      <Field
        label={t("settings.payments.fee")}
        hint={t("settings.payments.feeHint")}
        name={`method-${index}-percent`}
        inputMode="decimal"
        autoComplete="off"
        value={row.percent}
        onChange={(event) => update(row.key, { percent: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/fee_ppm`)}
      />
      <Field
        label={t("settings.payments.fixed")}
        hint={t("settings.payments.fixedHint", { currency: paidIn })}
        name={`method-${index}-fixed`}
        inputMode="decimal"
        autoComplete="off"
        value={row.fixed}
        onChange={(event) => update(row.key, { fixed: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/fee_fixed`)}
      />
      <Field
        label={t("settings.payments.min")}
        name={`method-${index}-min`}
        inputMode="decimal"
        autoComplete="off"
        value={row.min}
        onChange={(event) => update(row.key, { min: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/single_min`)}
      />
      <Field
        label={t("settings.payments.max")}
        name={`method-${index}-max`}
        inputMode="decimal"
        autoComplete="off"
        value={row.max}
        onChange={(event) => update(row.key, { max: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/single_max`)}
      />
      <Field
        label={t("settings.payments.dailyLimit")}
        name={`method-${index}-daily`}
        inputMode="decimal"
        autoComplete="off"
        value={row.daily}
        onChange={(event) => update(row.key, { daily: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/daily_limit`)}
      />
    </Grid>
  );
}
