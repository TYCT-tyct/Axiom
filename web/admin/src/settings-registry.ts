import type { AdminOperation } from "@axiom/api";
import type { ParseKeys, TFunction } from "i18next";

// The settings pages of the admin (adSetDef in admin_core.js), by the name a registry entry and the palette use for
// them; which route each one is, is nav.ts's.
export type SettingsPage =
  | "site"
  | "signin"
  | "registration"
  | "session"
  | "limits"
  | "payments"
  | "mail"
  | "gateway"
  | "alerts"
  | "agent";

type Words = ParseKeys<"admin">;

export type SettingEntry = {
  page: SettingsPage;
  // The admin API operation whose write saves this setting; the settings of one page that share it go out together.
  operation: AdminOperation;
  label: Words;
  detail?: Words;
};

// Every setting edited in place on a settings page is declared once here (adF in admin_set.js): the page draws its row
// from it, the palette finds it by name, and the submit dialog names it in the before and after and sends it by its
// operation. A page registers its rows here and passes their current values and writes to useSettingDrafts. The order
// is the order of the rows on their page, and the order the operations of one submit go out in. What is not a plain
// value stays out and keeps its own dialog: the logo, a login provider's secret, the merchants, the alert rules and
// channels, a credential, a limit looked up by its target.
export const settingsRegistry = {
  "site.name": {
    page: "site",
    operation: "setSystemSettings",
    label: "setting.site.name",
    detail: "setting.site.nameDetail",
  },
  "site.color": {
    page: "site",
    operation: "updateSiteBrand",
    label: "setting.site.color",
    detail: "setting.site.colorDetail",
  },
  "site.pricesPublic": {
    page: "site",
    operation: "updateSiteSettings",
    label: "setting.site.pricesPublic",
    detail: "setting.site.pricesPublicDetail",
  },
  "site.billing": {
    page: "site",
    operation: "updateSiteSettings",
    label: "setting.site.billing",
    detail: "setting.site.billingDetail",
  },
  "signin.domain": {
    page: "signin",
    operation: "updateSiteSignIn",
    label: "setting.signin.domain",
    detail: "setting.signin.domainDetail",
  },
  "signin.passkeys": {
    page: "signin",
    operation: "updateSiteSignIn",
    label: "setting.signin.passkeys",
    detail: "setting.signin.passkeysDetail",
  },
  "registration.mode": {
    page: "registration",
    operation: "updateSiteRegistration",
    label: "setting.registration.mode",
    detail: "setting.registration.modeDetail",
  },
  "registration.humanCheck": {
    page: "registration",
    operation: "updateSiteRegistration",
    label: "setting.registration.humanCheck",
    detail: "setting.registration.humanCheckDetail",
  },
  "registration.bits": {
    page: "registration",
    operation: "updateSiteRegistration",
    label: "setting.registration.bits",
    detail: "setting.registration.bitsDetail",
  },
  "registration.burst": {
    page: "registration",
    operation: "updateSiteRegistration",
    label: "setting.registration.burst",
    detail: "setting.registration.burstDetail",
  },
  "registration.perHour": {
    page: "registration",
    operation: "updateSiteRegistration",
    label: "setting.registration.perHour",
    detail: "setting.registration.perHourDetail",
  },
  "registration.allow": {
    page: "registration",
    operation: "updateSiteRegistration",
    label: "setting.registration.allow",
    detail: "setting.registration.allowDetail",
  },
  "registration.deny": {
    page: "registration",
    operation: "updateSiteRegistration",
    label: "setting.registration.deny",
    detail: "setting.registration.denyDetail",
  },
  "session.consoleIdle": {
    page: "session",
    operation: "updateSiteSettings",
    label: "setting.session.consoleIdle",
  },
  "session.consoleMax": {
    page: "session",
    operation: "updateSiteSettings",
    label: "setting.session.consoleMax",
    detail: "setting.session.consoleMaxDetail",
  },
  "session.adminIdle": {
    page: "session",
    operation: "updateSiteSettings",
    label: "setting.session.adminIdle",
  },
  "session.adminMax": {
    page: "session",
    operation: "updateSiteSettings",
    label: "setting.session.adminMax",
  },
  "session.ipBurst": {
    page: "session",
    operation: "updateSiteLoginLimits",
    label: "setting.session.ipBurst",
  },
  "session.ipPerMinute": {
    page: "session",
    operation: "updateSiteLoginLimits",
    label: "setting.session.ipPerMinute",
  },
  "session.emailFail": {
    page: "session",
    operation: "updateSiteLoginLimits",
    label: "setting.session.emailFail",
    detail: "setting.session.emailFailDetail",
  },
  "session.codeFail": {
    page: "session",
    operation: "updateSiteLoginLimits",
    label: "setting.session.codeFail",
  },
  "session.challenge": {
    page: "session",
    operation: "updateSiteLoginLimits",
    label: "setting.session.challenge",
    detail: "setting.session.challengeDetail",
  },
  "limits.maxKeys": {
    page: "limits",
    operation: "updateSiteKeyLimits",
    label: "setting.limits.maxKeys",
  },
  "limits.perHour": {
    page: "limits",
    operation: "updateSiteKeyLimits",
    label: "setting.limits.perHour",
  },
  "limits.newKeyDaily": {
    page: "limits",
    operation: "updateSiteSettings",
    label: "setting.limits.newKeyDaily",
    detail: "setting.limits.newKeyDailyDetail",
  },
  "limits.maxWorkspaces": {
    page: "limits",
    operation: "updateSiteWorkspaceLimit",
    label: "setting.limits.maxWorkspaces",
  },
  "payments.open": {
    page: "payments",
    operation: "setPaymentSettings",
    label: "setting.payments.open",
    detail: "setting.payments.openDetail",
  },
  "payments.min": {
    page: "payments",
    operation: "setPaymentSettings",
    label: "setting.payments.min",
    detail: "setting.payments.amountDetail",
  },
  "payments.max": {
    page: "payments",
    operation: "setPaymentSettings",
    label: "setting.payments.max",
    detail: "setting.payments.amountDetail",
  },
  "payments.buffer": {
    page: "payments",
    operation: "setPaymentSettings",
    label: "setting.payments.buffer",
    detail: "setting.payments.bufferDetail",
  },
  "mail.enabled": {
    page: "mail",
    operation: "setMailSettings",
    label: "setting.mail.enabled",
    detail: "setting.mail.enabledDetail",
  },
  "mail.host": {
    page: "mail",
    operation: "setMailSettings",
    label: "setting.mail.host",
  },
  "mail.port": {
    page: "mail",
    operation: "setMailSettings",
    label: "setting.mail.port",
    detail: "setting.mail.portDetail",
  },
  "mail.username": {
    page: "mail",
    operation: "setMailSettings",
    label: "setting.mail.username",
  },
  "mail.password": {
    page: "mail",
    operation: "setMailSettings",
    label: "setting.mail.password",
    detail: "setting.mail.passwordDetail",
  },
  "mail.from": {
    page: "mail",
    operation: "setMailSettings",
    label: "setting.mail.from",
    detail: "setting.mail.fromDetail",
  },
  "gateway.outputTokens": {
    page: "gateway",
    operation: "setSegmentSettings",
    label: "setting.gateway.outputTokens",
    detail: "setting.gateway.outputTokensDetail",
  },
  "gateway.overshoot": {
    page: "gateway",
    operation: "setSegmentSettings",
    label: "setting.gateway.overshoot",
    detail: "setting.gateway.overshootDetail",
  },
  "gateway.probeBudget": {
    page: "gateway",
    operation: "setQualityProbeSettings",
    label: "setting.gateway.probeBudget",
    detail: "setting.gateway.probeBudgetDetail",
  },
  "gateway.probeConcurrency": {
    page: "gateway",
    operation: "setQualityProbeSettings",
    label: "setting.gateway.probeConcurrency",
  },
  "gateway.verifyEvery": {
    page: "gateway",
    operation: "setQualityProbeSettings",
    label: "setting.gateway.verifyEvery",
  },
  "gateway.verifyMaxAge": {
    page: "gateway",
    operation: "setQualityProbeSettings",
    label: "setting.gateway.verifyMaxAge",
    detail: "setting.gateway.verifyMaxAgeDetail",
  },
  "gateway.verifyRetry": {
    page: "gateway",
    operation: "setQualityProbeSettings",
    label: "setting.gateway.verifyRetry",
  },
  "gateway.probeBase": {
    page: "gateway",
    operation: "setQualityProbeSettings",
    label: "setting.gateway.probeBase",
  },
  "gateway.hotDays": {
    page: "gateway",
    operation: "setRetention",
    label: "setting.gateway.hotDays",
    detail: "setting.gateway.hotDaysDetail",
  },
  "alerts.spendOn": {
    page: "alerts",
    operation: "setAlertConfiguration",
    label: "setting.alerts.spendOn",
    detail: "setting.alerts.spendOnDetail",
  },
  "alerts.spendNotify": {
    page: "alerts",
    operation: "setAlertConfiguration",
    label: "setting.alerts.spendNotify",
    detail: "setting.alerts.spendNotifyDetail",
  },
  "alerts.spendWindow": {
    page: "alerts",
    operation: "setAlertConfiguration",
    label: "setting.alerts.spendWindow",
  },
  "alerts.spendBaseline": {
    page: "alerts",
    operation: "setAlertConfiguration",
    label: "setting.alerts.spendBaseline",
  },
  "alerts.spendRatio": {
    page: "alerts",
    operation: "setAlertConfiguration",
    label: "setting.alerts.spendRatio",
  },
  "alerts.spendMin": {
    page: "alerts",
    operation: "setAlertConfiguration",
    label: "setting.alerts.spendMin",
    detail: "setting.alerts.spendMinDetail",
  },
  "alerts.spendRegion": {
    page: "alerts",
    operation: "setAlertConfiguration",
    label: "setting.alerts.spendRegion",
  },
  "alerts.spendModel": {
    page: "alerts",
    operation: "setAlertConfiguration",
    label: "setting.alerts.spendModel",
  },
  "agent.requests": {
    page: "agent",
    operation: "setOperationsAgentLimits",
    label: "setting.agent.requests",
  },
  "agent.moneyMinute": {
    page: "agent",
    operation: "setOperationsAgentLimits",
    label: "setting.agent.moneyMinute",
    detail: "setting.agent.moneyMinuteDetail",
  },
  "agent.moneyHour": {
    page: "agent",
    operation: "setOperationsAgentLimits",
    label: "setting.agent.moneyHour",
  },
  "agent.alertsHour": {
    page: "agent",
    operation: "setOperationsAgentLimits",
    label: "setting.agent.alertsHour",
  },
  "agent.margin": {
    page: "agent",
    operation: "setOperationsPriceEnvelope",
    label: "setting.agent.margin",
  },
  "agent.dailyChange": {
    page: "agent",
    operation: "setOperationsPriceEnvelope",
    label: "setting.agent.dailyChange",
  },
  "agent.dailyChanges": {
    page: "agent",
    operation: "setOperationsPriceEnvelope",
    label: "setting.agent.dailyChanges",
  },
  "agent.dailyModels": {
    page: "agent",
    operation: "setOperationsPriceEnvelope",
    label: "setting.agent.dailyModels",
  },
} as const satisfies Record<string, SettingEntry>;

export type SettingKey = keyof typeof settingsRegistry;
export type OperationOf<K extends SettingKey> = (typeof settingsRegistry)[K]["operation"];

export const settingKeys = Object.keys(settingsRegistry) as SettingKey[];

// What the palette lists for a setting: its words, and where it goes — the page, with the row named so it is flashed.
export type SettingSearchItem = {
  key: SettingKey;
  label: string;
  detail?: string;
  page: SettingsPage;
  search: { setting: SettingKey };
};

export function settingSearchItems(t: TFunction<"admin">): SettingSearchItem[] {
  return settingKeys.map((key) => {
    const entry: SettingEntry = settingsRegistry[key];
    return {
      key,
      label: t(entry.label),
      detail: entry.detail ? t(entry.detail) : undefined,
      page: entry.page,
      search: { setting: key },
    };
  });
}
