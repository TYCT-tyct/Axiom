import type { AdminSchemas } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Button, CheckField, Icon, moneyInput, SelectField, TextAreaField, TextField } from "@axiom/ui";
import type React from "react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { paymentCurrencies } from "./credit-read";
import { formatPercent } from "./payment-read";

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

function draftOf(method: AdminSchemas["MerchantMethod"] | undefined): Draft {
  return {
    key: crypto.randomUUID(),
    method: method?.method ?? "alipay",
    upstream: method?.upstream ?? "alipay",
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
  const [rows, setRows] = useState<Draft[]>(() =>
    merchant?.methods?.length ? merchant.methods.map((method) => draftOf(method)) : [draftOf(undefined)],
  );
  const [kind, setKind] = useState(merchant?.kind ?? "easypay");
  const [paidIn, setPaidIn] = useState(merchant?.currency ?? merchantCurrency(currency));
  const update: Update = (key, change) =>
    setRows((current) => current.map((row) => (row.key === key ? { ...row, ...change } : row)));
  return (
    <>
      <MerchantBasics
        merchant={merchant}
        kind={kind}
        setKind={setKind}
        paidIn={paidIn}
        setPaidIn={setPaidIn}
        error={error}
      />
      <MerchantFlags merchant={merchant} />
      <MerchantSecrets key={kind} merchant={merchant} kind={kind} error={error} />
      <MethodsEditor rows={rows} setRows={setRows} update={update} paidIn={paidIn} error={error} />
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
      <SelectField
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
      </SelectField>
      <TextField
        label={t("settings.payments.name")}
        name="name"
        autoComplete="off"
        maxLength={100}
        defaultValue={merchant?.name ?? ""}
        error={errors.field(error, "merchant/name")}
      />
      <SelectField
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
      </SelectField>
      <TextField
        label={t("settings.payments.ttl")}
        hint={t("settings.payments.ttlHint")}
        name="ttl_seconds"
        inputMode="numeric"
        autoComplete="off"
        defaultValue={merchant?.ttl_seconds ?? 1800}
        error={errors.field(error, "merchant/ttl_seconds")}
      />
      <TextField
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

function MerchantSecrets({ merchant, kind, error }: { merchant: Merchant | undefined; kind: string; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const defaults =
    kind === "alipay"
      ? { app_id: "", seller_id: "", gateway: "https://openapi.alipay.com/gateway.do" }
      : { gateway: "https://", pid: "", mode: "redirect" };
  return (
    <>
      <TextAreaField
        label={t("settings.payments.settings")}
        hint={t("settings.payments.settingsHint")}
        name="settings"
        className="min-h-40"
        defaultValue={JSON.stringify(merchant?.settings ?? defaults, null, 2)}
        error={errors.field(error, "merchant/settings")}
      />
      {kind === "alipay" ? (
        <AlipaySecrets merchant={merchant} error={error} />
      ) : (
        <EasyPaySecrets merchant={merchant} error={error} />
      )}
    </>
  );
}

function AlipaySecrets({ merchant, error }: { merchant: Merchant | undefined; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextAreaField
        label={t("settings.payments.privateKey")}
        hint={merchant ? t("settings.payments.privateKeyKeep") : t("settings.payments.privateKeyNew")}
        name="private_key"
        className="min-h-32"
        autoComplete="off"
        spellCheck={false}
        error={errors.field(error, "merchant/secrets")}
      />
      <TextAreaField
        label={t("settings.payments.publicKey")}
        hint={t("settings.payments.publicKeyHint")}
        name="public_key"
        autoComplete="off"
        spellCheck={false}
      />
      <TextAreaField
        label={t("settings.payments.oldPublicKey")}
        hint={t("settings.payments.oldPublicKeyHint")}
        name="old_public_key"
        autoComplete="off"
        spellCheck={false}
      />
    </>
  );
}

function EasyPaySecrets({ merchant, error }: { merchant: Merchant | undefined; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextField
        label={t("settings.payments.key")}
        hint={merchant ? t("settings.payments.keyKeep") : t("settings.payments.keyNew")}
        name="key"
        type="password"
        autoComplete="new-password"
        error={errors.field(error, "merchant/secrets")}
      />
      <TextField
        label={t("settings.payments.oldKey")}
        hint={t("settings.payments.oldKeyHint")}
        name="old_key"
        type="password"
        autoComplete="new-password"
      />
    </>
  );
}
function MethodsEditor({
  rows,
  setRows,
  update,
  paidIn,
  error,
}: {
  rows: Draft[];
  setRows: React.Dispatch<React.SetStateAction<Draft[]>>;
  update: Update;
  paidIn: string;
  error: unknown;
}) {
  const { t } = useTranslation();
  return (
    <div className="space-y-3">
      <p className="text-ui font-medium text-t2">{t("settings.payments.methods")}</p>
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
      <Button size="sm" onClick={() => setRows((current) => [...current, draftOf(undefined)])}>
        <Icon name="plus" />
        {t("settings.payments.addMethod")}
      </Button>
    </div>
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
    <fieldset className="space-y-3 rounded-card border border-b1 p-3">
      <legend className="px-1 text-label text-t3">{t("settings.payments.method", { number: index + 1 })}</legend>
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
        <Button variant="ghost" size="sm" onClick={onRemove}>
          <Icon name="trash" />
          {t("settings.payments.removeMethod")}
        </Button>
      ) : null}
    </fieldset>
  );
}

function MethodCodes({ row, index, update, error }: { row: Draft; index: number; update: Update; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <TextField
        label={t("settings.payments.methodCode")}
        name={`method-${index}-method`}
        autoComplete="off"
        value={row.method}
        onChange={(event) => update(row.key, { method: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/method`)}
      />
      <TextField
        label={t("settings.payments.upstream")}
        name={`method-${index}-upstream`}
        autoComplete="off"
        value={row.upstream}
        onChange={(event) => update(row.key, { upstream: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/upstream`)}
      />
    </div>
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
    <div className="grid gap-3 sm:grid-cols-2">
      <TextField
        label={t("settings.payments.fee")}
        hint={t("settings.payments.feeHint")}
        name={`method-${index}-percent`}
        inputMode="decimal"
        autoComplete="off"
        value={row.percent}
        onChange={(event) => update(row.key, { percent: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/fee_ppm`)}
      />
      <TextField
        label={t("settings.payments.fixed")}
        hint={t("settings.payments.fixedHint", { currency: paidIn })}
        name={`method-${index}-fixed`}
        inputMode="decimal"
        autoComplete="off"
        value={row.fixed}
        onChange={(event) => update(row.key, { fixed: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/fee_fixed`)}
      />
      <TextField
        label={t("settings.payments.min")}
        name={`method-${index}-min`}
        inputMode="decimal"
        autoComplete="off"
        value={row.min}
        onChange={(event) => update(row.key, { min: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/single_min`)}
      />
      <TextField
        label={t("settings.payments.max")}
        name={`method-${index}-max`}
        inputMode="decimal"
        autoComplete="off"
        value={row.max}
        onChange={(event) => update(row.key, { max: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/single_max`)}
      />
      <TextField
        label={t("settings.payments.dailyLimit")}
        name={`method-${index}-daily`}
        inputMode="decimal"
        autoComplete="off"
        value={row.daily}
        onChange={(event) => update(row.key, { daily: event.target.value })}
        error={errors.field(error, `merchant/methods/${index}/daily_limit`)}
      />
    </div>
  );
}
