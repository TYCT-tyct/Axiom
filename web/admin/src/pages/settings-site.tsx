import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { CopyButton, DataState, Mono, SettingRow } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { modelPricesChangeUnconfirmed } from "../model-prices";
import { siteBrandQuery, siteSettingsQuery, systemSettingsQuery } from "../queries";
import { ChoiceSetting, OptionalView, SettingsCard, SwitchSetting, TextSetting } from "../setting-rows";
import { SettingsSubmit, type SettingValue, useSettingDrafts } from "../settings-drafts";
import { BrandPreviewRow, brandValid } from "../site-brand";
import { SiteContentPage } from "../site-content-page";
import { LogoRow } from "../site-logo";
import { nameEditable, siteSettingsInput, writeSiteSettings } from "../site-settings-write";
import { say } from "../toasts";

type SiteId = AdminSchemas["SiteId"];
type Billing = NonNullable<AdminSchemas["SiteSettings"]["billing_preference"]>;
type Key = "site.name" | "site.color" | "site.pricesPublic" | "site.billing";

const billings: readonly Billing[] = ["subscription_first", "wallet_first", "subscription_only", "wallet_only"];

export default function SiteSettingsPage() {
  const { t } = useTranslation();
  return (
    <SiteContentPage title={t("shell.nav.site")} description={t("settings.pages.site.lede")}>
      {(siteId) => <Views siteId={siteId} />}
    </SiteContentPage>
  );
}

function Views({ siteId }: { siteId: SiteId }) {
  const system = useQuery(systemSettingsQuery(siteId));
  const settings = useQuery(siteSettingsQuery(siteId));
  const brand = useQuery(siteBrandQuery(siteId));
  return (
    <DataState query={system}>
      {(systemView) => (
        <DataState query={settings}>
          {(settingsView) => (
            <OptionalView query={brand}>
              {(brandView) => (
                <Rows siteId={siteId} system={systemView} settings={settingsView} brand={brandView?.brand_color} />
              )}
            </OptionalView>
          )}
        </DataState>
      )}
    </DataState>
  );
}

type RowsProps = {
  siteId: SiteId;
  system: AdminSchemas["SystemSettings"];
  settings: AdminSchemas["SiteSettingsView"];
  // undefined where the node has no brand operation
  brand: string | undefined;
};

function savedOf({ system, settings, brand }: RowsProps): Record<Key, SettingValue> {
  const { settings: own, defaults } = settings;
  return {
    "site.name": system.name ?? "",
    "site.color": brand ?? "",
    "site.pricesPublic": own.model_prices_public ?? defaults.model_prices_public ?? true,
    "site.billing": own.billing_preference ?? defaults.billing_preference ?? "subscription_first",
  };
}

function useSiteDrafts(props: RowsProps) {
  const { t } = useTranslation();
  const { siteId, settings } = props;
  const saved = savedOf(props);
  return useSettingDrafts<Key>({
    scope: `settings:site:${siteId}`,
    saved,
    check: (key, value) =>
      key === "site.name"
        ? typeof value === "string" && value.trim() !== "" && value.trim().length <= 100
        : key !== "site.color" || brandValid(String(value)),
    show: (key, value) => {
      if (key === "site.billing") return t(`settings.billing.values.${value as Billing}`);
      return key === "site.color" && value === "" ? t("drafts.default") : undefined;
    },
    writes: {
      setSystemSettings: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/system-settings", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { settings: siteSettingsInput(String(values["site.name"]).trim()), reason },
          }),
        ),
      updateSiteBrand: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/brand", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { brand_color: String(values["site.color"]).trim().toLowerCase(), reason },
          }),
        ),
      updateSiteSettings: async (values, context) => {
        const patch: AdminSchemas["SiteSettings"] = {};
        if (values["site.pricesPublic"] !== saved["site.pricesPublic"]) {
          patch.model_prices_public = values["site.pricesPublic"] === true;
        }
        if (values["site.billing"] !== saved["site.billing"]) {
          patch.billing_preference = values["site.billing"] as Billing;
        }
        const sent = await writeSiteSettings(siteId, settings, patch, context);
        // A node of the release before drops the switch and answers 200 without it: the row then shows what is stored,
        // and this says why it went back.
        if (modelPricesChangeUnconfirmed(settings, patch, sent.data)) {
          say(t("settings.publicPrices.notSaved"), { kind: "bad" });
        }
        return sent;
      },
    },
  });
}

function Rows(props: RowsProps) {
  const { t } = useTranslation();
  const drafts = useSiteDrafts(props);
  const { system, settings, brand, siteId } = props;
  return (
    <>
      <SettingsCard scope="site" title={t("settings.pages.site.brand")} lede={t("settings.pages.site.brandLede")}>
        <TextSetting drafts={drafts} setting="site.name" width={240} disabled={!nameEditable(system)} />
        <LogoRow siteId={siteId} />
        {brand === undefined ? null : (
          <>
            <TextSetting drafts={drafts} setting="site.color" width={128} mono swatch maxLength={7} />
            <BrandPreviewRow color={String(drafts.value("site.color"))} />
          </>
        )}
      </SettingsCard>
      <SettingsCard scope="site" title={t("settings.pages.site.billing")}>
        <SwitchSetting
          drafts={drafts}
          setting="site.pricesPublic"
          disabled={settings.defaults.model_prices_public === undefined}
        />
        <ChoiceSetting
          drafts={drafts}
          setting="site.billing"
          options={billings.map((value) => ({ value, label: t(`settings.billing.values.${value}`) }))}
        />
      </SettingsCard>
      <SettingsCard scope="site" title={t("settings.pages.site.about")}>
        <SettingRow title={t("settings.pages.site.id")} detail={t("settings.pages.site.idDetail")}>
          <Mono>{siteId}</Mono>
          <CopyButton text={siteId} label={t("settings.pages.site.copyId")} />
        </SettingRow>
      </SettingsCard>
      <SettingsSubmit drafts={drafts} />
    </>
  );
}

export { default as messages } from "../locales/settings";
