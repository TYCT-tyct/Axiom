import i18next, { type i18n as I18nInstance } from "i18next";
import { useTranslation } from "react-i18next";
import { linkFailed } from "./locales/link-failed";

// Registers only auth.linkFailed in the "login" namespace so a host's route error component can render it offline
// without bundling the rest of the login messages into its first screen.
export function registerLoginLinkFailed(instance: I18nInstance = i18next) {
  if (!instance.exists("auth.linkFailed", { lng: "zh", ns: "login" })) {
    instance.addResource("zh", "login", "auth.linkFailed", linkFailed.zh);
  }
  if (!instance.exists("auth.linkFailed", { lng: "en", ns: "login" })) {
    instance.addResource("en", "login", "auth.linkFailed", linkFailed.en);
  }
}

export function useLoginLinkFailed() {
  const { i18n } = useTranslation();
  registerLoginLinkFailed(i18n);
}
