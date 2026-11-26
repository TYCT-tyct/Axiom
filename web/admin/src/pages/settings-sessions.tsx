import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { DataState } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { siteLoginLimitsQuery, siteSettingsQuery } from "../queries";
import { type NumberSpec, numberPatch, numberText, numberValid } from "../setting-fields";
import { NumberSetting, OptionalView, SettingsCard } from "../setting-rows";
import type { TimeUnit } from "../setting-units";
import { SettingsSubmit, type SettingValue, useSettingDrafts } from "../settings-drafts";
import { SiteContentPage } from "../site-content-page";
import { writeSiteSettings } from "../site-settings-write";

type SiteId = AdminSchemas["SiteId"];

// The times a session lasts, each in the unit the design writes it in and with the bounds of api/admin.yaml
// (SiteSettings), and the attempts a sign-in may make (LoginLimits). What is left empty falls back to the default in code.
const times = {
  "session.consoleIdle": { name: "console_idle_timeout_seconds", min: 300, max: 2_592_000, unit: "day" },
  "session.consoleMax": { name: "console_max_age_seconds", min: 300, max: 7_776_000, unit: "day" },
  "session.adminIdle": { name: "admin_idle_timeout_seconds", min: 300, max: 43_200, unit: "minute" },
  "session.adminMax": { name: "admin_max_age_seconds", min: 300, max: 86_400, unit: "hour" },
} as const satisfies Record<string, NumberSpec>;
const attempts = {
  "session.ipBurst": { name: "ip_attempt_burst", min: 1, max: 10_000 },
  "session.ipPerMinute": { name: "ip_attempts_per_minute", min: 1, max: 10_000 },
  "session.emailFail": { name: "email_sign_in_failures", min: 1, max: 1_000 },
  "session.codeFail": { name: "user_code_failures", min: 1, max: 1_000 },
  "session.challenge": { name: "challenge_code_attempts", min: 1, max: 100 },
} as const satisfies Record<string, NumberSpec>;

type TimeKey = keyof typeof times;
type AttemptKey = keyof typeof attempts;
type Key = TimeKey | AttemptKey;
type Limits = AdminSchemas["LoginLimits"];

export default function SessionSettings() {
  const { t } = useTranslation();
  return (
    <SiteContentPage title={t("shell.nav.sessions")} description={t("settings.pages.sessions.lede")}>
      {(siteId) => <Views siteId={siteId} />}
    </SiteContentPage>
  );
}

function Views({ siteId }: { siteId: SiteId }) {
  const settings = useQuery(siteSettingsQuery(siteId));
  const limits = useQuery(siteLoginLimitsQuery(siteId));
  return (
    <DataState query={settings}>
      {(settingsView) => (
        <OptionalView query={limits}>
          {(limitsView) => <Rows siteId={siteId} settings={settingsView} limits={limitsView} />}
        </OptionalView>
      )}
    </DataState>
  );
}

type RowsProps = {
  siteId: SiteId;
  settings: AdminSchemas["SiteSettingsView"];
  // undefined where the node has no login limits operation
  limits: AdminSchemas["LoginLimitsView"] | undefined;
};

function savedOf({ settings, limits }: RowsProps): Record<Key, SettingValue> {
  const stored = { ...settings.defaults, ...settings.settings } as Record<string, number | undefined>;
  const tried = { ...limits?.defaults, ...limits?.limits } as Record<string, number | undefined>;
  const saved = {} as Record<Key, SettingValue>;
  for (const key of Object.keys(times) as TimeKey[]) saved[key] = numberText(times[key], stored[times[key].name]);
  for (const key of Object.keys(attempts) as AttemptKey[])
    saved[key] = numberText(attempts[key], tried[attempts[key].name]);
  return saved;
}

function Rows(props: RowsProps) {
  const { t } = useTranslation();
  const { siteId, settings, limits } = props;
  const saved = savedOf(props);
  const drafts = useSettingDrafts<Key>({
    scope: `settings:sessions:${siteId}`,
    saved,
    check: (key, value) => numberValid(key in times ? times[key as TimeKey] : attempts[key as AttemptKey], value, true),
    show: (_, value) => (value === "" ? t("drafts.default") : undefined),
    writes: {
      updateSiteSettings: (values, context) =>
        writeSiteSettings(siteId, settings, numberPatch(times, values, saved), context),
      updateSiteLoginLimits: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/login-limits", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { limits: { ...limits?.limits, ...numberPatch(attempts, values, saved) } as Limits, reason },
          }),
        ),
    },
  });
  return (
    <>
      <SettingsCard scope="site" title={t("settings.pages.sessions.keep")}>
        {(Object.keys(times) as TimeKey[]).map((key) => (
          <NumberSetting
            key={key}
            drafts={drafts}
            setting={key}
            unit={t(`unit.${times[key].unit as TimeUnit}`)}
            decimals={0}
          />
        ))}
      </SettingsCard>
      {limits === undefined ? null : (
        <SettingsCard
          scope="site"
          title={t("settings.pages.sessions.attempts")}
          lede={t("settings.pages.sessions.attemptsLede")}
        >
          {(Object.keys(attempts) as AttemptKey[]).map((key) => (
            <NumberSetting key={key} drafts={drafts} setting={key} unit={t("unit.times")} />
          ))}
        </SettingsCard>
      )}
      <SettingsSubmit drafts={drafts} />
    </>
  );
}

export { default as messages } from "../locales/settings";
