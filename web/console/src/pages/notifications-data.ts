import { ApiError, type Schemas } from "@axiom/api";
import type DesktopNotify from "@axiom/boards/AxApp/regions/Notify";
import type DesktopProfile from "@axiom/boards/AxApp/regions/Profile";
import type DesktopSecurity from "@axiom/boards/AxApp/regions/Security";
import { moneyInput } from "@axiom/ui";
import type { TFunction } from "i18next";

export type NotificationSettings = {
  lowBalance: boolean;
  threshold: string;
  topUp: boolean;
  factorChanged: boolean;
  spendAnomaly?: boolean;
  webhook: string;
  language: "en" | "zh";
};

export type NotificationSwitchKey = "lowBalance" | "topUp" | "factorChanged" | "spendAnomaly";

// spendAnomalyChange asks to change the reminder only when the node advertised the preference and the value differs
// from the stored one. A node that predates it rejects the unknown field, and an echo would overwrite another tab.
export function spendAnomalyChange(
  settings: NotificationSettings,
  stored: boolean | undefined,
): { spend_anomaly?: boolean } {
  if (settings.spendAnomaly === undefined || settings.spendAnomaly === stored) {
    return {};
  }
  return { spend_anomaly: settings.spendAnomaly };
}

// putPreferences retries without spend_anomaly on 400/422 and reports spendSaved: false if dropped/ignored.
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

export function initialSettings(preferences: Schemas["NotificationPreferences"]): NotificationSettings {
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

type NotifyRows = Parameters<typeof DesktopNotify>[0]["rows"]["notifications0"];
type DeliveryRows = Parameters<typeof DesktopProfile>[0]["rows"]["rows0"];
type SessionRows = Parameters<typeof DesktopSecurity>[0]["rows"]["sessions0"];

export function buildNotificationSwitches(
  t: TFunction,
  settings: NotificationSettings,
): { keys: NotificationSwitchKey[]; rows: NotifyRows } {
  const specs: { key: NotificationSwitchKey; title: string; hint: string; on: boolean }[] = [
    {
      key: "lowBalance",
      title: t("notifications.lowBalance"),
      hint: t("notifications.lowBalanceHint"),
      on: settings.lowBalance,
    },
    { key: "topUp", title: t("notifications.topUp"), hint: t("notifications.topUpHint"), on: settings.topUp },
    {
      key: "factorChanged",
      title: t("notifications.factorChanged"),
      hint: t("notifications.factorChangedHint"),
      on: settings.factorChanged,
    },
  ];
  if (settings.spendAnomaly !== undefined) {
    specs.push({
      key: "spendAnomaly",
      title: t("notifications.spendAnomaly"),
      hint: t("notifications.spendAnomalyHint"),
      on: settings.spendAnomaly,
    });
  }
  const rows: Parameters<typeof DesktopNotify>[0]["rows"]["notifications0"][number][] = specs.map((item) => ({
    key: item.key,
    kind: "0",
    f0: "qnt ",
    f1: item.title,
    f2: item.hint,
    f3: "button",
    f4: item.on ? "sw2 qsw on" : "sw2 qsw ",
    f5: `${item.title} ${t(item.on ? "notifications.switchStateOn" : "notifications.switchStateOff")}`,
    f6: "button",
    f7: "sw2 qsw ",
    f8: "",
  }));
  return { keys: specs.map((item) => item.key), rows };
}

export function buildDeliveryRows(
  t: TFunction,
  settings: NotificationSettings,
  currency: string | undefined,
  invalidThreshold: boolean,
): DeliveryRows {
  return [
    {
      key: "threshold",
      kind: "2",
      f0: "qrow",
      f1: "qrl",
      f2: t("notifications.threshold"),
      f3: invalidThreshold
        ? t("notifications.thresholdInvalid")
        : t("notifications.thresholdHint", { currency: currency ?? t("notifications.siteCurrency") }),
      f4: "qrc",
      f5: "qin",
      f6: "threshold",
      f7: settings.threshold,
      f8: t("notifications.threshold"),
    },
    {
      key: "webhook",
      kind: "2",
      f0: "qrow",
      f1: "qrl",
      f2: t("notifications.webhook"),
      f3: t("notifications.webhookHint"),
      f4: "qrc",
      f5: "qin",
      f6: "webhook",
      f7: settings.webhook,
      f8: t("notifications.webhook"),
    },
  ];
}

export function buildMessageLanguageRows(t: TFunction, selected: "zh" | "en"): SessionRows {
  const langs: readonly ("zh" | "en")[] = ["zh", "en"];
  return langs.map((lang) => {
    const title = lang === "zh" ? t("notifications.languageZh") : t("notifications.languageEn");
    const hint = t("notifications.languageHint");
    return lang === selected
      ? { key: lang, kind: "0", f0: "qses", f1: title, f2: hint, f3: "qtag ok", f4: t("notifications.activeTag") }
      : {
          key: lang,
          kind: "1",
          f0: "qses",
          f1: title,
          f2: hint,
          f3: "button",
          f4: "btn sec sm ",
          f5: t("notifications.choose"),
        };
  });
}
