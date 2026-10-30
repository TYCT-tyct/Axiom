import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Button, CheckField, DataState, Icon, moneyInput, TextField, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { Facts, State } from "./parts";
import { formatPercent, readPayments } from "./payment-read";
import { type PaymentAttempt, savePaymentSettings } from "./payment-save";
import { paymentSettingsQuery } from "./queries";

type Settings = AdminSchemas["PaymentSettings"];
type Update = AdminSchemas["PaymentSettingsUpdate"]["settings"];

export function PaymentCard({ siteId, currency }: { siteId: AdminSchemas["SiteId"]; currency: string }) {
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

function PaymentFields({
  view,
  currency,
  bufferKnown,
  error,
}: {
  view: Settings;
  currency: string;
  bufferKnown: boolean;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
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
  const bufferKnown = view.top_up_buffer_ppm !== undefined;
  const skipped = useRef(false);
  const attempted = useRef(new Map<string, PaymentAttempt>());
  const put = (settings: Update, reason: string, key: string) =>
    unwrapChange(
      adminApi.PUT("/api/admin/v1/sites/{site_id}/system-settings/payments", {
        params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
        body: { settings, reason },
      }),
    );
  // A page loaded from a new node can still reach an old node for the write during a rolling release. The old contract
  // refuses the whole settings object when it carries top_up_buffer_ppm; the same key then sends the body without it, and
  // every later resend of that key uses the stored body. A carried resend after the dialog was closed still runs this
  // send function and keeps its ref, so the downgrade survives the dialog.
  const send = async (settings: Update, { reason, key }: { reason: string; key: string }) => {
    const saved = await savePaymentSettings(attempted.current, key, settings, reason, put);
    skipped.current = saved.skipped;
    return saved;
  };
  return (
    <ChangeDialog
      operation={`payment-settings:${siteId}`}
      title={t("settings.payments.edit")}
      intro={<p>{t("settings.payments.intro")}</p>}
      action={t("settings.payments.save")}
      fields={(error) => <PaymentFields view={view} currency={currency} bufferKnown={bufferKnown} error={error} />}
      read={(values) =>
        readPayments(values, currency, bufferKnown, {
          amount: t("settings.payments.invalidAmount", { currency }),
          range: t("settings.payments.invalidRange"),
          buffer: t("settings.payments.invalidBuffer"),
        })
      }
      send={send}
      done={() => (
        <>
          <p>{t("settings.payments.saved")}</p>
          {skipped.current ? <p>{t("settings.payments.bufferSkipped")}</p> : null}
        </>
      )}
      onClose={onClose}
    />
  );
}
