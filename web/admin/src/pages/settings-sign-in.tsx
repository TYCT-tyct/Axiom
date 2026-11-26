import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { DataState } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { siteSignInQuery } from "../queries";
import { SettingsCard, SwitchSetting, TextSetting } from "../setting-rows";
import { SettingsSubmit, type SettingValue, useSettingDrafts } from "../settings-drafts";
import { Providers } from "../sign-in";
import { SiteContentPage } from "../site-content-page";

type SiteId = AdminSchemas["SiteId"];
type View = AdminSchemas["SiteSignIn"];
type Key = "signin.domain" | "signin.passkeys";

export default function SignInSettings() {
  const { t } = useTranslation();
  return (
    <SiteContentPage title={t("shell.nav.signIn")} description={t("settings.pages.signIn.lede")}>
      {(siteId) => <Loaded siteId={siteId} />}
    </SiteContentPage>
  );
}

function Loaded({ siteId }: { siteId: SiteId }) {
  const query = useQuery(siteSignInQuery(siteId));
  return <DataState query={query}>{(view) => <Rows siteId={siteId} view={view} />}</DataState>;
}

const savedOf = (view: View): Record<Key, SettingValue> => ({
  "signin.domain": view.login_domain ?? "",
  "signin.passkeys": view.passkeys,
});

function Rows({ siteId, view }: { siteId: SiteId; view: View }) {
  const { t } = useTranslation();
  const drafts = useSettingDrafts<Key>({
    scope: `settings:signin:${siteId}`,
    saved: savedOf(view),
    show: (key, value) => (key === "signin.domain" && value === "" ? t("settings.signIn.domainNone") : undefined),
    // A moved domain strands every passkey bound to the old one (design: adA_submit).
    warn: (values) =>
      values["signin.domain"] === savedOf(view)["signin.domain"] ? undefined : t("settings.signIn.domainWarn"),
    writes: {
      updateSiteSignIn: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/sign-in", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: {
              login_domain: String(values["signin.domain"]).trim().toLowerCase(),
              passkeys: values["signin.passkeys"] === true,
              reason,
            },
          }),
        ),
    },
  });
  return (
    <>
      <SettingsCard scope="site" title={t("settings.pages.signIn.domain")}>
        <TextSetting drafts={drafts} setting="signin.domain" width={240} mono />
        <SwitchSetting drafts={drafts} setting="signin.passkeys" />
      </SettingsCard>
      <Providers siteId={siteId} providers={view.providers} />
      <SettingsSubmit drafts={drafts} />
    </>
  );
}

export { default as messages } from "../locales/settings";
