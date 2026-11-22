import i18next, { type i18n as I18nInstance } from "i18next";
import { useTranslation } from "react-i18next";
import { en, zh } from "./locales";

// Register both languages under the package's own "login" namespace before rendering sign-in copy. Idempotent so
// hosts can call it eagerly or let components register on first render without overwriting already loaded bundles,
// and still merges the full bundle if auth.linkFailed was registered first on its own.
export function registerLoginMessages(instance: I18nInstance = i18next) {
  if (!instance.exists("legal.terms", { lng: "zh", ns: "login" })) {
    instance.addResourceBundle("zh", "login", zh, true, false);
  }
  if (!instance.exists("legal.terms", { lng: "en", ns: "login" })) {
    instance.addResourceBundle("en", "login", en, true, false);
  }
}

export function useLoginMessages() {
  const { i18n } = useTranslation();
  registerLoginMessages(i18n);
}
