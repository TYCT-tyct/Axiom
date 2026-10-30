import { consoleApi, type Schemas, unwrap } from "@axiom/api";
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
// top-up, and security changes, by email and to its own webhook. The language here is the messages' language, not the
// console's.
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
  const [lowBalance, setLowBalance] = useState(preferences.low_balance ?? true);
  const [topUp, setTopUp] = useState(preferences.top_up ?? true);
  const [factorChanged, setFactorChanged] = useState(preferences.factor_changed ?? true);
  const [threshold, setThreshold] = useState(moneyInput(preferences.threshold ?? "0"));
  const [webhook, setWebhook] = useState(preferences.webhook_url ?? "");
  const [language, setLanguage] = useState(preferences.language ?? "en");
  const [saved, setSaved] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  const amount = parseMoney(threshold);
  const valid = amount !== null;
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        consoleApi.PUT("/api/console/v1/notification-preferences", {
          body: {
            low_balance: lowBalance,
            top_up: topUp,
            factor_changed: factorChanged,
            threshold: amount ?? "0",
            webhook_url: webhook.trim(),
            language,
          },
        }),
      ),
    onSuccess: (data) => {
      client.setQueryData(notificationPreferencesQuery.queryKey, data);
      setFailure(null);
      setSaved(true);
    },
    onError: (error) => {
      setFailure(error);
      setSaved(false);
    },
  });
  return (
    <form
      className="space-y-5 p-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (valid) {
          save.mutate();
        }
      }}
    >
      {saved ? (
        <Notice action={<Button onClick={() => setSaved(false)}>{t("notifications.dismiss")}</Button>}>
          {t("notifications.saved")}
        </Notice>
      ) : null}
      <fieldset className="space-y-3">
        <legend className="text-ui font-medium text-t1">{t("notifications.balance.title")}</legend>
        <p className="max-w-prose text-ui text-t3">{t("notifications.balance.body")}</p>
        <CheckField
          label={t("notifications.lowBalance")}
          hint={t("notifications.lowBalanceHint")}
          checked={lowBalance}
          onChange={(event) => setLowBalance(event.target.checked)}
        />
        <TextField
          label={t("notifications.threshold")}
          hint={t("notifications.thresholdHint", { currency: currency ?? t("notifications.siteCurrency") })}
          inputMode="decimal"
          value={threshold}
          onChange={(event) => setThreshold(event.target.value)}
          error={valid ? undefined : t("notifications.thresholdInvalid")}
        />
      </fieldset>
      <fieldset className="space-y-3">
        <legend className="text-ui font-medium text-t1">{t("notifications.events.title")}</legend>
        <CheckField
          label={t("notifications.topUp")}
          hint={t("notifications.topUpHint")}
          checked={topUp}
          onChange={(event) => setTopUp(event.target.checked)}
        />
        <CheckField
          label={t("notifications.factorChanged")}
          hint={t("notifications.factorChangedHint")}
          checked={factorChanged}
          onChange={(event) => setFactorChanged(event.target.checked)}
        />
      </fieldset>
      <fieldset className="space-y-3">
        <legend className="text-ui font-medium text-t1">{t("notifications.delivery.title")}</legend>
        <TextField
          label={t("notifications.webhook")}
          hint={t("notifications.webhookHint")}
          type="url"
          maxLength={2048}
          autoComplete="off"
          placeholder="https://"
          value={webhook}
          onChange={(event) => setWebhook(event.target.value)}
        />
        <SelectField
          label={t("notifications.language")}
          hint={t("notifications.languageHint")}
          value={language}
          onChange={(event) => setLanguage(event.target.value as "en" | "zh")}
        >
          <option value="en">{t("notifications.languageEn")}</option>
          <option value="zh">{t("notifications.languageZh")}</option>
        </SelectField>
      </fieldset>
      {failure ? <FormError>{errors.message(failure)}</FormError> : null}
      <SubmitButton pending={save.isPending}>{t("notifications.save")}</SubmitButton>
    </form>
  );
}
