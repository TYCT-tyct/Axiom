import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { DataState, moneyInput, parseMoney } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useRef } from "react";
import { useTranslation } from "react-i18next";
import { isProblem } from "../change";
import { MerchantsCard } from "../merchants";
import { formatPercent, parseBufferPercent, readPayments } from "../payment-read";
import { type PaymentAttempt, savePaymentSettings } from "../payment-save";
import { paymentSettingsQuery } from "../queries";
import { NumberSetting, SettingsCard, SwitchSetting } from "../setting-rows";
import { SettingsSubmit, type SettingValue, useSettingDrafts } from "../settings-drafts";
import { SiteContentPage } from "../site-content-page";
import { say } from "../toasts";

type SiteId = AdminSchemas["SiteId"];
type Settings = AdminSchemas["PaymentSettings"];
type Key = "payments.open" | "payments.min" | "payments.max" | "payments.buffer";

export default function PaymentSettings() {
  const { t } = useTranslation();
  return (
    <SiteContentPage title={t("shell.nav.payments")} description={t("settings.pages.payments.lede")}>
      {(siteId, currency) => (
        <>
          <Loaded siteId={siteId} currency={currency} />
          <MerchantsCard siteId={siteId} currency={currency} />
        </>
      )}
    </SiteContentPage>
  );
}

function Loaded({ siteId, currency }: { siteId: SiteId; currency: string }) {
  const query = useQuery(paymentSettingsQuery(siteId));
  return <DataState query={query}>{(view) => <Rows siteId={siteId} currency={currency} view={view} />}</DataState>;
}

const savedOf = (view: Settings): Record<Key, SettingValue> => ({
  "payments.open": view.open === true,
  "payments.min": view.credits_min ? moneyInput(view.credits_min.amount) : "",
  "payments.max": view.credits_max ? moneyInput(view.credits_max.amount) : "",
  "payments.buffer": view.top_up_buffer_ppm === undefined ? "" : formatPercent(view.top_up_buffer_ppm),
});

// What a row's text may be: an amount of up to nine decimals, or nothing for no limit; a percentage the buffer allows.
function valid(key: Key, value: SettingValue): boolean {
  const text = typeof value === "string" ? value.trim() : "";
  if (key === "payments.open") return true;
  if (key === "payments.buffer") return text === "" || parseBufferPercent(text) !== null;
  return text === "" || parseMoney(text) !== null;
}

function Rows({ siteId, currency, view }: { siteId: SiteId; currency: string; view: Settings }) {
  const { t } = useTranslation();
  const bufferKnown = view.top_up_buffer_ppm !== undefined;
  const attempted = useRef(new Map<string, PaymentAttempt>());
  const words = {
    amount: t("settings.payments.invalidAmount", { currency }),
    range: t("settings.payments.invalidRange"),
    buffer: t("settings.payments.invalidBuffer"),
  };
  const drafts = useSettingDrafts<Key>({
    scope: `settings:payments:${siteId}`,
    saved: savedOf(view),
    check: valid,
    show: (key, value) => (key !== "payments.open" && value === "" ? t("drafts.noLimit") : undefined),
    verify: (values) => {
      const read = readPayments(textsOf(values), currency, bufferKnown, words);
      return isProblem(read) ? read.problem : undefined;
    },
    writes: {
      setPaymentSettings: async (values, { reason, key }) => {
        const settings = readPayments(textsOf(values), currency, bufferKnown, words);
        if (isProblem(settings)) throw new Error(settings.problem);
        // A node of the release before refuses a field it does not know; the body without it is sent under the same key.
        const saved = await savePaymentSettings(attempted.current, key, settings, reason, (body, why, again) =>
          unwrapChange(
            adminApi.PUT("/api/admin/v1/sites/{site_id}/system-settings/payments", {
              params: { path: { site_id: siteId }, header: { "Idempotency-Key": again } },
              body: { settings: body, reason: why },
            }),
          ),
        );
        if (saved.skipped) say(t("settings.payments.fieldsSkipped"));
        return saved;
      },
    },
  });
  return (
    <>
      <SettingsCard scope="site" title={t("settings.pages.payments.top")}>
        <SwitchSetting drafts={drafts} setting="payments.open" />
        <NumberSetting drafts={drafts} setting="payments.min" prefix={currency} />
        <NumberSetting drafts={drafts} setting="payments.max" prefix={currency} />
        {bufferKnown ? (
          <NumberSetting drafts={drafts} setting="payments.buffer" unit={t("unit.percent")} decimals={1} />
        ) : null}
      </SettingsCard>
      <SettingsSubmit drafts={drafts} />
    </>
  );
}

// The form's text of each row, as readPayments reads it.
const textsOf = (values: Record<Key, SettingValue>) => ({
  open: String(values["payments.open"] === true),
  credits_min: String(values["payments.min"]),
  credits_max: String(values["payments.max"]),
  top_up_buffer_ppm: String(values["payments.buffer"]),
});

export { default as messages } from "../locales/settings";
