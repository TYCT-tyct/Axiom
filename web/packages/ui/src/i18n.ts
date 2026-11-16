import i18next from "i18next";
import { initReactI18next } from "react-i18next";
import type { zh as design } from "./locales/design";
import en from "./locales/en";
import zh from "./locales/zh";
import { readStored, writeStored } from "./storage";

export const languages = ["zh", "en"] as const;
export type Language = (typeof languages)[number];

// The tag each language goes by in <html lang> and Intl: Simplified Chinese as used in mainland China, US English.
export const locales: Record<Language, string> = { zh: "zh-CN", en: "en-US" };

// Each language is offered under its own name, which is not translated.
export const languageNames: Record<Language, string> = { zh: "中文", en: "English" };

const languageKey = "axiom.language";

// A Chinese-reading browser gets Chinese, any other that lists English gets English, and the rest Chinese, the
// product's first language (docs/frontend.md「基线」). A choice the user made wins over all of it.
export function initialLanguage(stored: string | null, preferred: readonly string[]): Language {
  const chosen = languages.find((language) => language === stored);
  if (chosen) {
    return chosen;
  }
  for (const tag of preferred) {
    const primary = tag.toLowerCase().split("-")[0];
    const match = languages.find((language) => language === primary);
    if (match) {
      return match;
    }
  }
  return "zh";
}

// The shared messages' type, which each app declares as its namespace "common" (i18next.d.ts). It includes the
// redesign's component messages, which join the namespace at run time only where DesignProvider is.
export type CommonMessages = typeof zh & typeof design;

// A namespace's messages: the Chinese ones are the source, and the English ones must have exactly the same keys.
export type Messages<T> = { [K in keyof T]: T[K] extends string ? string : Messages<T[K]> };

// startI18n starts translation with the shared messages (namespace "common") and the app's own (namespace ns), which is
// the default namespace in the app's code. Every visible string comes from these files.
export function startI18n<T extends object>(ns: string, messages: { zh: T; en: Messages<T> }) {
  const language = initialLanguage(readStored(languageKey), navigator.languages);
  i18next.use(initReactI18next).init({
    lng: language,
    fallbackLng: false,
    resources: {
      zh: { common: zh, [ns]: messages.zh },
      en: { common: en, [ns]: messages.en },
    },
    ns: ["common", ns],
    defaultNS: ns,
    interpolation: { escapeValue: false }, // React escapes
    initAsync: false,
  });
  document.documentElement.lang = locales[language];
}

export function chooseLanguage(language: Language) {
  writeStored(languageKey, language);
  document.documentElement.lang = locales[language];
  void i18next.changeLanguage(language);
}
