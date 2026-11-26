import { useTranslation } from "react-i18next";
import { SiteRegistration } from "../registration";
import { SiteContentPage } from "../site-content-page";

export default function RegistrationSettings() {
  const { t } = useTranslation();
  return (
    <SiteContentPage title={t("shell.nav.registration")} description={t("settings.pages.registration.lede")}>
      {(siteId) => <SiteRegistration siteId={siteId} />}
    </SiteContentPage>
  );
}

export { default as messages } from "../locales/settings";
