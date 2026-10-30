import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import {
  Button,
  CheckField,
  DataState,
  EmptyState,
  Icon,
  moneyInput,
  Panel,
  SelectField,
  TextAreaField,
  TextField,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, useSnapshot } from "./change";
import { paymentCurrencies } from "./credit-read";
import { Facts, NewButton, State } from "./parts";
import { formatPercent, readMerchant, readPayments } from "./payment-read";
import { merchantsQuery, paymentSettingsQuery } from "./queries";

type Merchant = AdminSchemas["Merchant"];
type Settings = AdminSchemas["PaymentSettings"];

export function PaymentsSettings({ siteId, currency }: { siteId: AdminSchemas["SiteId"]; currency: string }) {
  return (
    <>
      <PaymentCard siteId={siteId} currency={currency} />
      <MerchantsCard siteId={siteId} currency={currency} />
    </>
  );
}

function PaymentCard({ siteId, currency }: { siteId: AdminSchemas["SiteId"]; currency: string }) {
  const query = useQuery(paymentSettingsQuery(siteId));
  return (
    <DataState query={query}>{(view) => <PaymentFacts siteId={siteId} currency={currency} view={view} />}</DataState>
  );
}

function PaymentFacts({
  siteId,
  currency,
  view,
}: {
  siteId: AdminSchemas["SiteId"];
  currency: string;
  view: Settings;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const [open, setOpen] = useState(false);
  const bufferKnown = view.top_up_buffer_ppm !== undefined;
  return (
    <>
      <Facts
        title={t("settings.payments.title")}
        rows={[
          [
            t("settings.payments.collection"),
            <State key="open" tone={view.open ? "good" : "neutral"}>
              {t(view.open ? "settings.payments.opened" : "settings.payments.closed")}
            </State>,
          ],
          [t("settings.payments.min"), view.credits_min ? format.money(view.credits_min) : t("settings.payments.none")],
          [t("settings.payments.max"), view.credits_max ? format.money(view.credits_max) : t("settings.payments.none")],
          [
            t("settings.payments.buffer"),
            bufferKnown ? `${formatPercent(view.top_up_buffer_ppm ?? 0)}%` : t("settings.payments.bufferUnavailable"),
          ],
        ]}
        action={
          <Button size="md" onClick={() => setOpen(true)}>
            <Icon name="pencil" />
            {t("settings.payments.edit")}
          </Button>
        }
      />
      {open ? <PaymentDialog siteId={siteId} currency={currency} view={view} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function PaymentDialog({
  siteId,
  currency,
  view,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  currency: string;
  view: Settings;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const bufferKnown = view.top_up_buffer_ppm !== undefined;
  return (
    <ChangeDialog
      operation={`payment-settings:${siteId}`}
      title={t("settings.payments.edit")}
      intro={<p>{t("settings.payments.intro")}</p>}
      action={t("settings.payments.save")}
      fields={(error) => (
        <>
          <CheckField
            label={t("settings.payments.open")}
            hint={t("settings.payments.openHint")}
            name="open"
            value="true"
            defaultChecked={view.open === true}
          />
          <TextField
            label={t("settings.payments.min")}
            hint={t("settings.payments.amountHint", { currency })}
            name="credits_min"
            inputMode="decimal"
            autoComplete="off"
            defaultValue={view.credits_min ? moneyInput(view.credits_min.amount) : ""}
            error={errors.field(error, "settings/credits_min")}
          />
          <TextField
            label={t("settings.payments.max")}
            hint={t("settings.payments.amountHint", { currency })}
            name="credits_max"
            inputMode="decimal"
            autoComplete="off"
            defaultValue={view.credits_max ? moneyInput(view.credits_max.amount) : ""}
            error={errors.field(error, "settings/credits_max")}
          />
          {bufferKnown ? (
            <TextField
              label={t("settings.payments.buffer")}
              hint={t("settings.payments.bufferHint")}
              name="top_up_buffer_ppm"
              inputMode="decimal"
              autoComplete="off"
              defaultValue={formatPercent(view.top_up_buffer_ppm ?? 0)}
              error={errors.field(error, "settings/top_up_buffer_ppm")}
            />
          ) : null}
        </>
      )}
      read={(values) =>
        readPayments(values, currency, bufferKnown, {
          amount: t("settings.payments.invalidAmount", { currency: values.currency ?? currency }),
          range: t("settings.payments.invalidRange"),
          buffer: t("settings.payments.invalidBuffer"),
        })
      }
      send={(settings, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/system-settings/payments", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { settings, reason },
          }),
        )
      }
      done={() => <p>{t("settings.payments.saved")}</p>}
      onClose={onClose}
    />
  );
}

// MerchantsUnavailable is what an older node, which has no merchant list operation, shows: the rest of the page works.
function MerchantsUnavailable() {
  const { t } = useTranslation();
  return (
    <Facts
      title={t("settings.payments.merchants")}
      rows={[[t("settings.payments.merchantsUnavailableLabel"), t("settings.payments.merchantsUnavailable")]]}
    />
  );
}

function MerchantsCard({ siteId, currency }: { siteId: AdminSchemas["SiteId"]; currency: string }) {
  const { t } = useTranslation();
  const query = useQuery(merchantsQuery(siteId));
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <MerchantsUnavailable />;
  }
  return (
    <Panel
      title={t("settings.payments.merchants")}
      action={
        <NewButton permission="sites.manage" label={t("settings.payments.add")}>
          {(close) => <MerchantDialog siteId={siteId} currency={currency} onClose={close} />}
        </NewButton>
      }
    >
      <DataState
        query={query}
        isEmpty={(list) => (list.data ?? []).length === 0}
        empty={
          <EmptyState compact title={t("settings.payments.empty")}>
            <p>{t("settings.payments.emptyBody")}</p>
          </EmptyState>
        }
      >
        {(list) => (
          <div className="divide-y divide-b1">
            {(list.data ?? []).map((merchant) => (
              <MerchantRow key={merchant.id} merchant={merchant} siteId={siteId} currency={currency} />
            ))}
          </div>
        )}
      </DataState>
    </Panel>
  );
}

function MerchantRow({
  merchant,
  siteId,
  currency,
}: {
  merchant: Merchant;
  siteId: AdminSchemas["SiteId"];
  currency: string;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const [read, reload] = useSnapshot(merchant);
  const [edit, setEdit] = useState(false);
  const [test, setTest] = useState(false);
  const [disable, setDisable] = useState(false);
  const money = (value: AdminSchemas["Money"] | undefined) =>
    value ? format.money(value) : t("settings.payments.none");
  return (
    <div className="space-y-2 px-4 py-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="font-medium text-t1 [overflow-wrap:anywhere]">{merchant.name ?? merchant.id}</span>
          <State tone={merchant.enabled ? "good" : "neutral"}>
            {t(merchant.enabled ? "settings.payments.enabled" : "settings.payments.disabled")}
          </State>
          <span className="font-mono text-label text-t3">{merchant.kind}</span>
          <span className="text-label text-t3">{merchant.currency}</span>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" onClick={() => setTest(true)}>
            {t("settings.payments.test")}
          </Button>
          <Button
            size="sm"
            onClick={() => {
              // Take the current revision when the dialog opens, not the one from when this row first rendered.
              reload();
              setEdit(true);
            }}
          >
            <Icon name="pencil" />
            {t("settings.payments.editMerchant")}
          </Button>
          <Button size="sm" variant="danger" disabled={!merchant.enabled} onClick={() => setDisable(true)}>
            {t("settings.payments.disable")}
          </Button>
        </div>
      </div>
      <dl className="grid gap-x-4 gap-y-1 text-label sm:grid-cols-[10rem_minmax(0,1fr)]">
        <dt className="text-t4">{t("settings.payments.methods")}</dt>
        <dd className="min-w-0 space-y-1 text-t2">
          {(merchant.methods ?? []).length === 0 ? (
            <span>{t("settings.payments.noMethods")}</span>
          ) : (
            (merchant.methods ?? []).map((method) => (
              <div key={method.method} className="flex flex-wrap gap-x-3 gap-y-0.5">
                <span className="font-mono">{method.method}</span>
                <span className="text-t4">{t("settings.payments.upstreamValue", { code: method.upstream })}</span>
                <span>
                  {t("settings.payments.feeValue", {
                    percent: formatPercent(method.fee_ppm ?? 0),
                    fixed: money(method.fee_fixed),
                  })}
                </span>
                <span className="text-t3">
                  {t("settings.payments.methodLimits", {
                    min: money(method.single_min),
                    max: money(method.single_max),
                    daily: t("settings.payments.daily", { amount: money(method.daily_limit) }),
                  })}
                </span>
                {method.enabled === false ? <span className="text-t4">{t("settings.payments.methodOff")}</span> : null}
              </div>
            ))
          )}
        </dd>
        <dt className="text-t4">{t("settings.payments.ttl")}</dt>
        <dd className="text-t2">
          {t("settings.payments.ttlValue", { seconds: format.number(merchant.ttl_seconds ?? 0) })}
        </dd>
        <dt className="text-t4">{t("settings.payments.refunds")}</dt>
        <dd className="text-t2">
          {t("settings.payments.refundValue", {
            automatic: merchant.refund_enabled ? t("settings.payments.yes") : t("settings.payments.no"),
            user: merchant.user_refund ? t("settings.payments.yes") : t("settings.payments.no"),
          })}
        </dd>
        <dt className="text-t4">{t("settings.payments.revision")}</dt>
        <dd className="font-mono text-t2">{merchant.rev ?? "—"}</dd>
      </dl>
      {edit ? (
        <MerchantDialog
          siteId={siteId}
          currency={currency}
          merchant={read}
          reload={reload}
          onClose={() => setEdit(false)}
        />
      ) : null}
      {test ? <TestDialog merchant={merchant} onClose={() => setTest(false)} /> : null}
      {disable ? <DisableDialog merchant={merchant} onClose={() => setDisable(false)} /> : null}
    </div>
  );
}

function TestDialog({ merchant, onClose }: { merchant: Merchant; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`merchant-test:${merchant.id}`}
      title={t("settings.payments.testTitle", { name: merchant.name ?? merchant.id })}
      intro={<p>{t("settings.payments.testIntro")}</p>}
      action={t("settings.payments.runTest")}
      verdict
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/payments/merchants/{merchant_id}/tests", {
            params: { path: { merchant_id: merchant.id ?? "" }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={(result) => <p>{t(`settings.payments.status.${result.status ?? "failed"}`)}</p>}
      onClose={onClose}
    />
  );
}

function DisableDialog({ merchant, onClose }: { merchant: Merchant; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`merchant-disable:${merchant.id}`}
      title={t("settings.payments.disable")}
      intro={<p>{t("settings.payments.disableIntro", { name: merchant.name ?? merchant.id })}</p>}
      action={t("settings.payments.disable")}
      danger
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/payments/merchants/{merchant_id}/disable", {
            params: { path: { merchant_id: merchant.id ?? "" }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={() => <p>{t("settings.payments.disabledNow")}</p>}
      onClose={onClose}
    />
  );
}

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

function MerchantFields({
  merchant,
  currency,
  error,
}: {
  merchant: Merchant | undefined;
  currency: string;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [rows, setRows] = useState<Draft[]>(() =>
    merchant?.methods?.length ? merchant.methods.map((method) => draftOf(method)) : [draftOf(undefined)],
  );
  const [paidIn, setPaidIn] = useState(merchant?.currency ?? merchantCurrency(currency));
  const update = (key: string, change: Partial<Draft>) =>
    setRows((current) => current.map((row) => (row.key === key ? { ...row, ...change } : row)));
  return (
    <>
      <SelectField
        label={t("settings.payments.kind")}
        hint={t("settings.payments.kindHint")}
        name="kind"
        defaultValue={merchant?.kind ?? "easypay"}
        error={errors.field(error, "merchant/kind")}
      >
        <option value="easypay">{t("settings.payments.kinds.easypay")}</option>
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
      <TextAreaField
        label={t("settings.payments.settings")}
        hint={t("settings.payments.settingsHint")}
        name="settings"
        className="min-h-40"
        defaultValue={JSON.stringify(merchant?.settings ?? { gateway: "https://", pid: "", mode: "redirect" }, null, 2)}
        error={errors.field(error, "merchant/settings")}
      />
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
      <div className="space-y-3">
        <p className="text-ui font-medium text-t2">{t("settings.payments.methods")}</p>
        {rows.map((row, index) => (
          <fieldset key={row.key} className="space-y-3 rounded-card border border-b1 p-3">
            <legend className="px-1 text-label text-t3">{t("settings.payments.method", { number: index + 1 })}</legend>
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
            <CheckField
              label={t("settings.payments.methodEnabled")}
              name={`method-${index}-enabled`}
              value="true"
              checked={row.enabled}
              onChange={(event) => update(row.key, { enabled: event.target.checked })}
            />
            {rows.length > 1 ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setRows((current) => current.filter((entry) => entry.key !== row.key))}
              >
                <Icon name="trash" />
                {t("settings.payments.removeMethod")}
              </Button>
            ) : null}
          </fieldset>
        ))}
        <Button size="sm" onClick={() => setRows((current) => [...current, draftOf(undefined)])}>
          <Icon name="plus" />
          {t("settings.payments.addMethod")}
        </Button>
      </div>
    </>
  );
}

function MerchantDialog({
  siteId,
  currency,
  merchant,
  reload,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  currency: string;
  merchant?: Merchant;
  reload?: () => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={merchant ? `merchant:${merchant.id}` : `merchant-new:${siteId}`}
      title={t(merchant ? "settings.payments.editMerchant" : "settings.payments.add")}
      intro={<p>{t("settings.payments.merchantIntro")}</p>}
      action={t("settings.payments.saveMerchant")}
      reload={reload}
      fields={(error) => <MerchantFields merchant={merchant} currency={currency} error={error} />}
      read={(values) =>
        readMerchant(values, merchant, {
          name: t("settings.payments.invalidName"),
          number: t("settings.payments.invalidNumber"),
          settings: t("settings.payments.invalidSettings"),
          secret: t("settings.payments.invalidSecret"),
          amount: t("settings.payments.invalidAmount", { currency: values.currency ?? currency }),
          fee: t("settings.payments.invalidFee"),
          range: t("settings.payments.invalidRange"),
          methods: t("settings.payments.invalidMethods"),
        })
      }
      send={(input, { reason, key }) =>
        unwrapChange(
          merchant
            ? adminApi.PUT("/api/admin/v1/payments/merchants/{merchant_id}", {
                params: { path: { merchant_id: merchant.id ?? "" }, header: { "Idempotency-Key": key } },
                body: { merchant: input, reason },
              })
            : adminApi.POST("/api/admin/v1/payments/merchants", {
                params: { header: { "Idempotency-Key": key } },
                body: { site_id: siteId, merchant: input, reason },
              }),
        )
      }
      done={(saved) => <p>{t("settings.payments.merchantSaved", { name: saved.name ?? "" })}</p>}
      onClose={onClose}
    />
  );
}
