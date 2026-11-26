import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { DataState, moneyInput, parseMoney } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { siteKeyLimitsQuery, siteSettingsQuery, siteWorkspaceLimitQuery } from "../queries";
import { type NumberSpec, numberOf, numberPatch, numberText, numberValid } from "../setting-fields";
import { NumberSetting, OptionalView, SettingsCard } from "../setting-rows";
import { SettingsSubmit, type SettingValue, useSettingDrafts } from "../settings-drafts";
import { SiteContentPage } from "../site-content-page";
import { writeSiteSettings } from "../site-settings-write";

type SiteId = AdminSchemas["SiteId"];
type KeyLimits = AdminSchemas["KeyLimits"];

// The limits on the keys of an account (KeyLimits) and the workspaces of a team (WorkspaceLimit), with the bounds of
// api/admin.yaml; what is left empty falls back to the default in code.
const keyLimits = {
  "limits.maxKeys": { name: "max_keys", min: 1, max: 10_000 },
  "limits.perHour": { name: "creations_per_hour", min: 1, max: 10_000 },
} as const satisfies Record<string, NumberSpec>;
const workspaces: NumberSpec = { name: "max_workspaces", min: 1, max: 1_000 };
// 1,000,000 units in billionths: the most a new key's daily limit may be.
const dailyMax = 1_000_000_000_000_000n;

type Key = keyof typeof keyLimits | "limits.newKeyDaily" | "limits.maxWorkspaces";

export default function KeyAndWorkspaceLimits() {
  const { t } = useTranslation();
  return (
    <SiteContentPage title={t("shell.nav.keysAndWorkspaces")} description={t("settings.pages.limits.lede")}>
      {(siteId, currency) => <Views siteId={siteId} currency={currency} />}
    </SiteContentPage>
  );
}

function Views({ siteId, currency }: { siteId: SiteId; currency: string }) {
  const settings = useQuery(siteSettingsQuery(siteId));
  const keys = useQuery(siteKeyLimitsQuery(siteId));
  const workspace = useQuery(siteWorkspaceLimitQuery(siteId));
  return (
    <DataState query={settings}>
      {(settingsView) => (
        <OptionalView query={keys}>
          {(keysView) => (
            <OptionalView query={workspace}>
              {(workspaceView) => (
                <Rows
                  siteId={siteId}
                  currency={currency}
                  settings={settingsView}
                  keys={keysView}
                  workspace={workspaceView}
                />
              )}
            </OptionalView>
          )}
        </OptionalView>
      )}
    </DataState>
  );
}

type RowsProps = {
  siteId: SiteId;
  currency: string;
  settings: AdminSchemas["SiteSettingsView"];
  // undefined where the node has no such operation: the card is left out
  keys: AdminSchemas["KeyLimitsView"] | undefined;
  workspace: AdminSchemas["WorkspaceLimitView"] | undefined;
};

function savedOf({ settings, keys, workspace }: RowsProps): Record<Key, SettingValue> {
  const daily = settings.settings.new_key_daily_limit ?? settings.defaults.new_key_daily_limit;
  return {
    "limits.maxKeys": numberText(keyLimits["limits.maxKeys"], keys?.effective.max_keys),
    "limits.perHour": numberText(keyLimits["limits.perHour"], keys?.effective.creations_per_hour),
    "limits.newKeyDaily": daily ? moneyInput(daily.amount) : "",
    "limits.maxWorkspaces": numberText(workspaces, workspace?.effective),
  };
}

function validDaily(text: string): boolean {
  const amount = parseMoney(text);
  return text.trim() === "" || (amount !== null && BigInt(amount) <= dailyMax);
}

function Rows(props: RowsProps) {
  const { t } = useTranslation();
  const { siteId, settings, keys, workspace } = props;
  const saved = savedOf(props);
  const currency = settings.defaults.new_key_daily_limit?.currency ?? props.currency;
  const drafts = useSettingDrafts<Key>({
    scope: `settings:limits:${siteId}`,
    saved,
    check: (key, value) => {
      if (key === "limits.newKeyDaily") return typeof value === "string" && validDaily(value);
      return numberValid(key === "limits.maxWorkspaces" ? workspaces : keyLimits[key], value, true);
    },
    show: (_, value) => (value === "" ? t("drafts.default") : undefined),
    writes: {
      updateSiteSettings: (values, context) => {
        const text = String(values["limits.newKeyDaily"]).trim();
        const amount = parseMoney(text);
        return writeSiteSettings(
          siteId,
          settings,
          { new_key_daily_limit: amount === null ? undefined : { amount, currency } },
          context,
        );
      },
      updateSiteKeyLimits: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/key-limits", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { limits: { ...keys?.limits, ...numberPatch(keyLimits, values, saved) } as KeyLimits, reason },
          }),
        ),
      updateSiteWorkspaceLimit: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/workspace-limit", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { max_workspaces: numberOf(workspaces, String(values["limits.maxWorkspaces"])), reason },
          }),
        ),
    },
  });
  return (
    <>
      <SettingsCard scope="site" title={t("settings.pages.limits.keys")}>
        {keys === undefined ? null : (
          <>
            <NumberSetting drafts={drafts} setting="limits.maxKeys" unit={t("unit.count")} />
            <NumberSetting drafts={drafts} setting="limits.perHour" unit={t("unit.count")} />
          </>
        )}
        <NumberSetting drafts={drafts} setting="limits.newKeyDaily" prefix={currency} />
      </SettingsCard>
      {workspace === undefined ? null : (
        <SettingsCard scope="site" title={t("settings.pages.limits.workspaces")}>
          <NumberSetting drafts={drafts} setting="limits.maxWorkspaces" unit={t("unit.count")} />
        </SettingsCard>
      )}
      <SettingsSubmit drafts={drafts} />
    </>
  );
}

export { default as messages } from "../locales/settings";
