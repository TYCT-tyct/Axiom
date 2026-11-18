import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { claimSession, holdsIdentity, useErrors } from "@axiom/app";
import DesktopNotify from "@axiom/boards/AxApp/regions/Notify";
import DesktopNotifyEn from "@axiom/boards/AxApp/regions/Notify.en";
import DesktopNotifyZh from "@axiom/boards/AxApp/regions/Notify.zh";
import PhoneNotify from "@axiom/boards/AxAppPhone/regions/Notify";
import PhoneNotifyEn from "@axiom/boards/AxAppPhone/regions/Notify.en";
import PhoneNotifyZh from "@axiom/boards/AxAppPhone/regions/Notify.zh";
import { Button, DataState, FormError, Notice, parseMoney, usePageTitle } from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type SyntheticEvent, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { notificationPreferencesQuery, siteQuery } from "../queries";
import { boardPageStyle, clickedRowIndex, makeBoardT, useBoardInputs, useEnglish, useWide } from "./account-board";
import { AccountActionCard, AccountInputsCard } from "./account-cards";
import {
  buildDeliveryRows,
  buildMessageLanguageRows,
  buildNotificationSwitches,
  initialSettings,
  type NotificationSettings,
  putPreferences,
  spendAnomalyChange,
} from "./notifications-data";

export { initialSettings, putPreferences, spendAnomalyChange };

export default function Notifications() {
  const { t } = useTranslation();
  const preferences = useQuery(notificationPreferencesQuery);
  const site = useQuery(siteQuery);
  usePageTitle(t("nav.notifications"));

  return (
    <DataState query={preferences}>
      {(data) => <NotificationsBoard preferences={data} currency={site.data?.currency} />}
    </DataState>
  );
}

function useNotificationForm(preferences: Schemas["NotificationPreferences"]) {
  const client = useQueryClient();
  const [settings, setSettings] = useState<NotificationSettings>(() => initialSettings(preferences));
  const [outcome, setOutcome] = useState<"saved" | "partial" | null>(null);
  const [failure, setFailure] = useState<unknown>(null);
  const draftVersion = useRef(0);
  const amount = parseMoney(settings.threshold);

  const editSettings = (update: (held: NotificationSettings) => NotificationSettings) => {
    draftVersion.current += 1;
    setOutcome(null);
    setSettings(update);
  };

  const inputs = useBoardInputs({
    threshold: { inputMode: "decimal" },
    webhook: { type: "url", maxLength: 2048, autoComplete: "off", placeholder: "https://" },
  });

  useEffect(() => {
    inputs.setValue("threshold", settings.threshold);
    inputs.setValue("webhook", settings.webhook);
  }, [inputs, settings.threshold, settings.webhook]);

  const save = useMutation({
    onMutate: () => ({ claim: claimSession(client), version: draftVersion.current }),
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
    onSuccess: ({ data, spendSaved }, _vars, context) => {
      if (!context?.claim || !holdsIdentity(client, context.claim.identity)) return;
      client.setQueryData(notificationPreferencesQuery.queryKey, data);
      setFailure(null);
      if (context.version === draftVersion.current) setOutcome(spendSaved ? "saved" : "partial");
      if (!spendSaved) setSettings((held) => ({ ...held, spendAnomaly: undefined }));
    },
    onError: (err, _vars, context) => {
      if (!context?.claim || !holdsIdentity(client, context.claim.identity)) return;
      setFailure(err);
      setOutcome(null);
    },
  });

  return { settings, editSettings, amount, inputs, save, outcome, clearOutcome: () => setOutcome(null), failure };
}

function NotificationsBoard({
  preferences,
  currency,
}: {
  preferences: Schemas["NotificationPreferences"];
  currency: string | undefined;
}) {
  const { t } = useTranslation();
  const wide = useWide();
  const english = useEnglish();
  const errors = useErrors();
  const form = useNotificationForm(preferences);
  const NotifyRegion = wide ? DesktopNotify : PhoneNotify;
  const { keys: switchKeys, rows: switchRows } = buildNotificationSwitches(t, form.settings);
  const rowStyle = wide ? "grid-template-columns: minmax(0, 1fr) 64px;" : "grid-template-columns: minmax(0, 1fr) 52px;";

  return (
    <>
      {form.outcome ? (
        <Notice action={<Button onClick={form.clearOutcome}>{t("notifications.dismiss")}</Button>}>
          {t(form.outcome === "partial" ? "notifications.savedPartialSpend" : "notifications.saved")}
        </Notice>
      ) : null}
      {form.amount === null ? <FormError>{t("notifications.thresholdInvalid")}</FormError> : null}
      {form.failure ? <FormError>{errors.message(form.failure)}</FormError> : null}
      <NotifyRegion
        t={makeBoardT(english, wide ? DesktopNotifyEn : PhoneNotifyEn, wide ? DesktopNotifyZh : PhoneNotifyZh, {
          m512b281f73: t("notifications.description"),
          m3d708e8e8b: t("notifications.channelHeader"),
        })}
        values={{
          pageStyle: boardPageStyle(wide, { first: true }),
          singleChannel: true,
          rowStyle,
          toggleDisabled: form.save.isPending,
          onToggle: (event) => {
            if (form.save.isPending) return;
            const key = switchKeys[clickedRowIndex(event.target)];
            if (key) form.editSettings((held) => ({ ...held, [key]: !held[key] }));
          },
        }}
        rows={{ notifications0: switchRows }}
      />
      <NotificationsDelivery wide={wide} english={english} currency={currency} form={form} />
    </>
  );
}

function NotificationsDelivery({
  wide,
  english,
  currency,
  form,
}: {
  wide: boolean;
  english: boolean;
  currency: string | undefined;
  form: ReturnType<typeof useNotificationForm>;
}) {
  const { t } = useTranslation();
  const invalid = form.amount === null;
  const submitSave = () => {
    if (!invalid && !form.save.isPending) form.save.mutate();
  };

  return (
    <>
      <AccountInputsCard
        wide={wide}
        english={english}
        inputRef={form.inputs.inputRef}
        disabled={form.save.isPending}
        onInput={(event: SyntheticEvent) => {
          if (form.save.isPending || !(event.target instanceof HTMLInputElement)) return;
          const { value, dataset } = event.target;
          if (dataset.k === "threshold") form.editSettings((h) => ({ ...h, threshold: value }));
          else if (dataset.k === "webhook") form.editSettings((h) => ({ ...h, webhook: value }));
        }}
        onKeyDown={(event: SyntheticEvent) => {
          if ((event as unknown as KeyboardEvent).key === "Enter") {
            event.preventDefault();
            submitSave();
          }
        }}
        rows={buildDeliveryRows(t, form.settings, currency, invalid)}
      />
      <AccountActionCard
        wide={wide}
        english={english}
        title={t("notifications.language")}
        rowDisabled={form.save.isPending}
        onRowAction={(event) => {
          if (form.save.isPending) return;
          form.editSettings((held) => ({ ...held, language: clickedRowIndex(event.target) === 0 ? "zh" : "en" }));
        }}
        rows={buildMessageLanguageRows(t, form.settings.language)}
      />
      <AccountActionCard
        wide={wide}
        english={english}
        title={t("notifications.saveTitle")}
        last
        rowDisabled={form.save.isPending || invalid}
        onRowAction={submitSave}
        rows={[
          {
            key: "save",
            kind: "1",
            f0: "qses",
            f1: t("notifications.save"),
            f2: t("notifications.saveHint"),
            f3: "button",
            f4: "btn pri sm ",
            f5: form.save.isPending ? t("notifications.saving") : t("notifications.save"),
          },
        ]}
      />
    </>
  );
}
