import type { AdminOperation } from "@axiom/api";
import type { ParseKeys, TFunction } from "i18next";

// The settings pages of the admin (adSetDef in admin_core.js), by the name a registry entry and the palette use for
// them; which route each one is, is the shell's.
type SettingsPage =
  | "site"
  | "signin"
  | "registration"
  | "session"
  | "limits"
  | "rate"
  | "payments"
  | "mail"
  | "gateway"
  | "alerts"
  | "agent";

type Words = ParseKeys<"admin">;

type SettingEntry = {
  page: SettingsPage;
  // The admin API operation whose write saves this setting; the settings of one page that share it go out together.
  operation: AdminOperation;
  label: Words;
  detail?: Words;
};

// Every setting edited in place on a settings page is declared once here (adF in admin_set.js): the page draws its row
// from it, the palette finds it by name, and the submit dialog names it in the before and after and sends it by its
// operation. A page registers its rows here and passes their current values and writes to useSettingDrafts. The order
// is the order of the rows on their page, and the order the operations of one submit go out in.
export const settingsRegistry = {
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
