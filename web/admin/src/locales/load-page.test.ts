import i18next from "i18next";
import { expect, it } from "vitest";
import en, { initial as initialEn } from "./en";
import { loadTranslatedPage } from "./load-page";
import operations from "./operations";
import routing from "./routing";
import settings from "./settings";
import subscriptions from "./subscriptions";
import suppliers from "./suppliers";
import zh, { initial as initialZh } from "./zh";

it("loads complete deferred copy before returning a page and preserves both languages", async () => {
  await i18next.init({
    lng: "en",
    defaultNS: "admin",
    resources: { en: { admin: initialEn }, zh: { admin: initialZh } },
  });
  expect(i18next.exists("settings.pages.site.brand")).toBe(false);
  const page = { default: () => null, messages: settings };
  const supplierPage = { default: () => null, messages: suppliers };
  const subscriptionPage = { default: () => null, messages: subscriptions };
  const opsPage = { default: () => null, messages: operations };
  const routingPage = { default: () => null, messages: routing };
  expect(await loadTranslatedPage(Promise.resolve(page))).toBe(page);
  expect(i18next.exists("suppliers.title")).toBe(false);
  expect(await loadTranslatedPage(Promise.resolve(supplierPage))).toBe(supplierPage);
  expect(i18next.exists("subscriptions.title")).toBe(false);
  expect(await loadTranslatedPage(Promise.resolve(subscriptionPage))).toBe(subscriptionPage);
  expect(i18next.exists("operations.credentials.title")).toBe(false);
  expect(await loadTranslatedPage(Promise.resolve(opsPage))).toBe(opsPage);
  expect(i18next.exists("routingPresets.title")).toBe(false);
  expect(await loadTranslatedPage(Promise.resolve(routingPage))).toBe(routingPage);
  for (const page of [
    import("../pages/payment-discrepancies"),
    import("../pages/account"),
    import("../pages/pricing"),
    import("../pages/alert-events"),
    import("../pages/alert-configuration"),
    import("../pages/announcements"),
    import("../pages/legal-documents"),
    import("../pages/settings-site"),
    import("../pages/rate-limits"),
    import("../pages/calls"),
    import("../pages/audit"),
    import("../pages/plugins"),
    import("../pages/me"),
  ]) {
    await loadTranslatedPage<{ messages: { zh: object; en: object } }>(page);
  }
  expect(i18next.getResourceBundle("en", "admin")).toEqual(en);
  expect(i18next.getResourceBundle("zh", "admin")).toEqual(zh);
  await i18next.changeLanguage("zh");
  expect(i18next.t("settings.pages.site.brand")).toBe(zh.settings.pages.site.brand);
  expect(await loadTranslatedPage(Promise.resolve(page))).toBe(page);
  expect(i18next.getResourceBundle("zh", "admin")).toEqual(zh);
  // Loading every page of the admin cold takes longer than the default five seconds on a busy machine.
}, 30_000);
