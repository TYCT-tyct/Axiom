import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { useErrors } from "@axiom/app";
import {
  Button,
  CheckField,
  DataState,
  FormError,
  moneyInput,
  Notice,
  PageHeader,
  Panel,
  parseMoney,
  SelectField,
  SubmitButton,
  TextField,
} from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { notificationPreferencesQuery, siteQuery } from "../queries";

// Notifications is where the account says when it wants to hear from Axiom: a balance that is running out, a finished
// top-up, security changes and unusual account activity, by email and to its own webhook. The language here is the
// messages' language, not the console's.
export default function Notifications() {
  const { t } = useTranslation();
  const preferences = useQuery(notificationPreferencesQuery);
  const site = useQuery(siteQuery);
  return (
    <>
      <PageHeader title={t("nav.notifications")} description={t("notifications.description")} />
      <div className="space-y-4 p-4 sm:p-6">
        <Panel title={t("notifications.title")}>
          <DataState query={preferences}>
            {(data) => <PreferencesForm preferences={data} currency={site.data?.currency} />}
          </DataState>
        </Panel>
      </div>
    </>
  );
}

// Settings is the form's state while it is being edited, one field for each preference.
type Settings = {
  lowBalance: boolean;
  threshold: string;
  topUp: boolean;
  factorChanged: boolean;
  spendAnomaly?: boolean; // undefined: the node predates the preference and did not advertise it
  webhook: string;
  language: "en" | "zh";
};

type SetSetting = <K extends keyof Settings>(key: K, value: Settings[K]) => void;

// spendAnomalyChange asks to change the reminder only when the node advertised the preference and the value differs
// from the stored one. A node that predates it rejects the unknown field (its request schema is closed), and a value
// the page merely echoed would overwrite a change made in another tab between the read and the save.
export function spendAnomalyChange(settings: Settings, stored: boolean | undefined): { spend_anomaly?: boolean } {
  if (settings.spendAnomaly === undefined || settings.spendAnomaly === stored) {
    return {};
  }
  return { spend_anomaly: settings.spendAnomaly };
}

// putPreferences saves the form and says whether the reminder was stored. A node that handles the save may be older
// than the one that served the read: with its closed request schema it either rejects the unknown field or (as today's
// decoder does) ignores it. In both cases the other settings are saved without the reminder, and the page must say so
// instead of reporting a complete save.
export async function putPreferences(
  body: Schemas["NotificationPreferencesRequest"],
  send: (body: Schemas["NotificationPreferencesRequest"]) => Promise<Schemas["NotificationPreferences"]>,
): Promise<{ data: Schemas["NotificationPreferences"]; spendSaved: boolean }> {
  try {
    const data = await send(body);
    return { data, spendSaved: body.spend_anomaly === undefined || data.spend_anomaly !== undefined };
  } catch (error) {
    if (body.spend_anomaly === undefined || !(error instanceof ApiError)) {
      throw error;
    }
    if (error.status !== 400 && error.status !== 422) {
      throw error;
    }
    const rest = { ...body };
    delete rest.spend_anomaly;
    const data = await send(rest);
    return { data, spendSaved: false };
  }
}

// initialSettings is what the form starts from: the stored preference, or the default the site sends when it is absent.
export function initialSettings(preferences: Schemas["NotificationPreferences"]): Settings {
  return {
    lowBalance: preferences.low_balance ?? true,
    threshold: moneyInput(preferences.threshold ?? "0"),
    topUp: preferences.top_up ?? true,
    factorChanged: preferences.factor_changed ?? true,
    spendAnomaly: preferences.spend_anomaly,
    webhook: preferences.webhook_url ?? "",
    language: preferences.language ?? "en",
  };
}

function PreferencesForm({
  preferences,
  currency,
}: {
  preferences: Schemas["NotificationPreferences"];
  currency: string | undefined;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const client = useQueryClient();
  const [settings, setSettings] = useState<Settings>(() => initialSettings(preferences));
  const [outcome, setOutcome] = useState<"saved" | "partial" | null>(null);
  const [failure, setFailure] = useState<unknown>(null);
  const amount = parseMoney(settings.threshold);
  const set: SetSetting = (key, value) => setSettings((held) => ({ ...held, [key]: value }));
  const save = useMutation({
    mutationFn: () =>
      putPreferences(
        {
          low_balance: settings.lowBalance,
          top_up: settings.topUp,
          factor_changed: settings.factorChanged,
          ...spendAnomalyChange(settings, preferences.spend_anomaly),
          threshold: amount ?? "0",
          webhook_url: settings.webhook.trim(),
          language: settings.language,
        },
        (body) => unwrap(consoleApi.PUT("/api/console/v1/notification-preferences", { body })),
      ),
    onSuccess: ({ data, spendSaved }) => {
      client.setQueryData(notificationPreferencesQuery.queryKey, data);
      setFailure(null);
      setOutcome(spendSaved ? "saved" : "partial");
      if (!spendSaved) {
        // This node cannot store the reminder; stop showing a switch whose value it would discard again.
        setSettings((held) => ({ ...held, spendAnomaly: undefined }));
      }
    },
    onError: (error) => {
      setFailure(error);
      setOutcome(null);
    },
  });
  return (
    <form
      className="max-w-xl space-y-5 p-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (amount !== null) {
          save.mutate();
        }
      }}
    >
      {outcome ? (
        <Notice action={<Button onClick={() => setOutcome(null)}>{t("notifications.dismiss")}</Button>}>
          {t(outcome === "partial" ? "notifications.savedPartialSpend" : "notifications.saved")}
        </Notice>
      ) : null}
      <BalanceAlerts currency={currency} settings={settings} set={set} invalid={amount === null} />
      <EventAlerts settings={settings} set={set} />
      <Delivery settings={settings} set={set} />
      {failure ? <FormError>{errors.message(failure)}</FormError> : null}
      <SubmitButton pending={save.isPending}>{t("notifications.save")}</SubmitButton>
    </form>
  );
}

// BalanceAlerts is when a balance that is running out is worth a message, and below what amount.
function BalanceAlerts({
  currency,
  settings,
  set,
  invalid,
}: {
  currency: string | undefined;
  settings: Settings;
  set: SetSetting;
  invalid: boolean;
}) {
  const { t } = useTranslation();
  return (
    <fieldset className="space-y-3">
      <legend className="text-ui font-medium text-t1">{t("notifications.balance.title")}</legend>
      <p className="max-w-prose text-ui text-t3">{t("notifications.balance.body")}</p>
      <CheckField
        label={t("notifications.lowBalance")}
        hint={t("notifications.lowBalanceHint")}
        checked={settings.lowBalance}
        onChange={(event) => set("lowBalance", event.target.checked)}
      />
      <TextField
        label={t("notifications.threshold")}
        hint={t("notifications.thresholdHint", { currency: currency ?? t("notifications.siteCurrency") })}
        inputMode="decimal"
        value={settings.threshold}
        onChange={(event) => set("threshold", event.target.value)}
        error={invalid ? t("notifications.thresholdInvalid") : undefined}
      />
    </fieldset>
  );
}

// EventAlerts is which events of the account are worth a message besides the balance.
function EventAlerts({ settings, set }: { settings: Settings; set: SetSetting }) {
  const { t } = useTranslation();
  return (
    <fieldset className="space-y-3">
      <legend className="text-ui font-medium text-t1">{t("notifications.events.title")}</legend>
      <CheckField
        label={t("notifications.topUp")}
        hint={t("notifications.topUpHint")}
        checked={settings.topUp}
        onChange={(event) => set("topUp", event.target.checked)}
      />
      <CheckField
        label={t("notifications.factorChanged")}
        hint={t("notifications.factorChangedHint")}
        checked={settings.factorChanged}
        onChange={(event) => set("factorChanged", event.target.checked)}
      />
      {settings.spendAnomaly === undefined ? null : (
        <CheckField
          label={t("notifications.spendAnomaly")}
          hint={t("notifications.spendAnomalyHint")}
          checked={settings.spendAnomaly}
          onChange={(event) => set("spendAnomaly", event.target.checked)}
        />
      )}
    </fieldset>
  );
}

// Delivery is where the messages go and in which language: the person's webhook, or the account's email.
function Delivery({ settings, set }: { settings: Settings; set: SetSetting }) {
  const { t } = useTranslation();
  return (
    <fieldset className="space-y-3">
      <legend className="text-ui font-medium text-t1">{t("notifications.delivery.title")}</legend>
      <TextField
        label={t("notifications.webhook")}
        hint={t("notifications.webhookHint")}
        type="url"
        maxLength={2048}
        autoComplete="off"
        placeholder="https://"
        value={settings.webhook}
        onChange={(event) => set("webhook", event.target.value)}
      />
      <SelectField
        label={t("notifications.language")}
        hint={t("notifications.languageHint")}
        value={settings.language}
        onChange={(event) => set("language", event.target.value as "en" | "zh")}
      >
        <option value="en">{t("notifications.languageEn")}</option>
        <option value="zh">{t("notifications.languageZh")}</option>
      </SelectField>
    </fieldset>
  );
}
