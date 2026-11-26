import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { DataState } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Facts } from "./parts";
import { siteRegistrationQuery } from "./queries";
import { ChipSetting, ChoiceSetting, NumberSetting, SettingsCard, SwitchSetting } from "./setting-rows";
import { type SettingDrafts, SettingsSubmit, type SettingValue, useSettingDrafts } from "./settings-drafts";

type Policy = AdminSchemas["RegistrationPolicy"];
type View = AdminSchemas["RegistrationPolicyView"];
type Mode = Policy["mode"];
type Key =
  | "registration.mode"
  | "registration.humanCheck"
  | "registration.bits"
  | "registration.burst"
  | "registration.perHour"
  | "registration.allow"
  | "registration.deny";
type NumberKey = "registration.bits" | "registration.burst" | "registration.perHour";

const modes: readonly Mode[] = ["invite", "closed", "open"];
// The bounds the contract gives each number (api/admin.yaml RegistrationPolicy).
const bounds: Record<NumberKey, { min: number; max: number }> = {
  "registration.bits": { min: 12, max: 24 },
  "registration.burst": { min: 1, max: 10_000 },
  "registration.perHour": { min: 1, max: 100_000 },
};

// RegistrationUnavailable stands in for the card where an older node has no registration operation: the rest of the
// settings still work.
function RegistrationUnavailable() {
  const { t } = useTranslation();
  return (
    <Facts
      title={t("settings.registration.title")}
      rows={[[t("settings.registration.unavailableLabel"), t("settings.registration.unavailable")]]}
    />
  );
}

export function SiteRegistration({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const query = useQuery(siteRegistrationQuery(siteId));
  // Only a 404 says this node has no registration operation. A timeout, a 503 or a permission error is a current
  // failure and belongs in DataState, with its reason and retry.
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <RegistrationUnavailable />;
  }
  return <DataState query={query}>{(view) => <RegistrationRows siteId={siteId} view={view} />}</DataState>;
}

function savedOf({ policy }: View): Record<Key, SettingValue> {
  return {
    "registration.mode": policy.mode,
    "registration.humanCheck": policy.human_check,
    "registration.bits": String(policy.human_check_bits),
    "registration.burst": String(policy.domain_burst),
    "registration.perHour": String(policy.domain_per_hour),
    "registration.allow": policy.domain_allow,
    "registration.deny": policy.domain_deny,
  };
}

const isNumberKey = (key: Key): key is NumberKey => key in bounds;

// A number is a whole number in its bounds, typed as digits only.
function validRegistrationValue(key: Key, value: SettingValue): boolean {
  if (!isNumberKey(key)) return true;
  const { min, max } = bounds[key];
  return typeof value === "string" && /^\d+$/.test(value.trim()) && Number(value) >= min && Number(value) <= max;
}

function policyOf(values: Record<Key, SettingValue>): Policy {
  return {
    mode: values["registration.mode"] as Mode,
    human_check: values["registration.humanCheck"] === true,
    human_check_bits: Number(values["registration.bits"]),
    domain_burst: Number(values["registration.burst"]),
    domain_per_hour: Number(values["registration.perHour"]),
    domain_allow: [...(values["registration.allow"] as readonly string[])],
    domain_deny: [...(values["registration.deny"] as readonly string[])],
  };
}
function RegistrationRows({ siteId, view }: { siteId: AdminSchemas["SiteId"]; view: View }) {
  const { t } = useTranslation();
  const drafts = useSettingDrafts<Key>({
    scope: `settings:registration:${siteId}`,
    saved: savedOf(view),
    check: validRegistrationValue,
    show: (key, value) => (key === "registration.mode" ? t(`settings.registration.modes.${value as Mode}`) : undefined),
    warn: (values) =>
      values["registration.mode"] === "open" && values["registration.humanCheck"] !== true
        ? t("settings.registration.openWarn")
        : undefined,
    writes: {
      updateSiteRegistration: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/registration", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { policy: policyOf(values), reason },
          }),
        ),
    },
  });
  return (
    <>
      <SettingsCard scope="site" title={t("settings.pages.registration.entry")}>
        <ChoiceSetting
          drafts={drafts}
          setting="registration.mode"
          options={modes.map((mode) => ({ value: mode, label: t(`settings.registration.modes.${mode}`) }))}
        />
        <SwitchSetting drafts={drafts} setting="registration.humanCheck" />
        <RegistrationNumber drafts={drafts} setting="registration.bits" unit={t("unit.bits")} />
      </SettingsCard>
      <SettingsCard
        scope="site"
        title={t("settings.pages.registration.domains")}
        lede={t("settings.pages.registration.domainsLede")}
      >
        <RegistrationNumber drafts={drafts} setting="registration.burst" unit={t("unit.count")} />
        <RegistrationNumber drafts={drafts} setting="registration.perHour" unit={t("unit.count")} />
        <ChipSetting
          drafts={drafts}
          setting="registration.allow"
          placeholder={t("settings.registration.domainPlaceholder")}
        />
        <ChipSetting
          drafts={drafts}
          setting="registration.deny"
          placeholder={t("settings.registration.denyPlaceholder")}
        />
      </SettingsCard>
      <SettingsSubmit drafts={drafts} />
    </>
  );
}

function RegistrationNumber({
  drafts,
  setting,
  unit,
}: {
  drafts: SettingDrafts<Key>;
  setting: NumberKey;
  unit: string;
}) {
  return (
    <NumberSetting
      drafts={drafts}
      setting={setting}
      unit={unit}
      min={bounds[setting].min}
      max={bounds[setting].max}
      disabled={setting === "registration.bits" && drafts.value("registration.humanCheck") !== true}
    />
  );
}
