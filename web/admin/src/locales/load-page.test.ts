import i18next from "i18next";
import { expect, it } from "vitest";
import en, { initial as initialEn } from "./en";
import { loadTranslatedPage } from "./load-page";
import operations from "./operations";
import settings from "./settings";
import suppliers from "./suppliers";
import zh, { initial as initialZh } from "./zh";

it("loads complete deferred copy before returning a page and preserves both languages", async () => {
  await i18next.init({
    lng: "en",
    defaultNS: "admin",
    resources: { en: { admin: initialEn }, zh: { admin: initialZh } },
  });
  expect(i18next.exists("settings.title")).toBe(false);
  const page = { default: () => null, messages: settings };
  const supplierPage = { default: () => null, messages: suppliers };
  const opsPage = { default: () => null, messages: operations };
  expect(await loadTranslatedPage(Promise.resolve(page))).toBe(page);
  expect(i18next.exists("suppliers.title")).toBe(false);
  expect(await loadTranslatedPage(Promise.resolve(supplierPage))).toBe(supplierPage);
  expect(i18next.exists("operations.credentials.title")).toBe(false);
  expect(await loadTranslatedPage(Promise.resolve(opsPage))).toBe(opsPage);
  for (const page of [
    import("../pages/alert-events"),
    import("../pages/alert-configuration"),
    import("../pages/announcements"),
    import("../pages/legal-documents"),
    import("../pages/retention"),
    import("../pages/rate-limits"),
  ]) {
    await loadTranslatedPage<{ messages: { zh: object; en: object } }>(page);
  }
  expect(i18next.getResourceBundle("en", "admin")).toEqual(en);
  expect(i18next.getResourceBundle("zh", "admin")).toEqual(zh);
  await i18next.changeLanguage("zh");
  expect(i18next.t("settings.title")).toBe(zh.settings.title);
  expect(await loadTranslatedPage(Promise.resolve(page))).toBe(page);
  expect(i18next.getResourceBundle("zh", "admin")).toEqual(zh);
});
