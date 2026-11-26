import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { Card, ChipField, DataState, NumberField, SegmentedControl, Switch } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Facts } from "./parts";
import { siteRegistrationQuery } from "./queries";
import { DraftRow, type SettingDrafts, SettingsSubmit, type SettingValue, useSettingDrafts } from "./settings-drafts";

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
      <Card title={t("settings.registration.title")} lede={t("settings.registration.intro")} layout="rows">
        <DraftRow drafts={drafts} setting="registration.mode">
          <SegmentedControl
            label={drafts.label("registration.mode")}
            value={drafts.value("registration.mode") as Mode}
            options={modes.map((mode) => ({ value: mode, label: t(`settings.registration.modes.${mode}`) }))}
            onChange={(mode) => drafts.set("registration.mode", mode)}
          />
        </DraftRow>
        <DraftRow drafts={drafts} setting="registration.humanCheck">
          <Switch
            label={drafts.label("registration.humanCheck")}
            checked={drafts.value("registration.humanCheck") === true}
            onChange={(on) => drafts.set("registration.humanCheck", on)}
          />
        </DraftRow>
        <NumberRow
          drafts={drafts}
          setting="registration.bits"
          disabled={drafts.value("registration.humanCheck") !== true}
        />
        <NumberRow drafts={drafts} setting="registration.burst" />
        <NumberRow drafts={drafts} setting="registration.perHour" />
        <DomainRow drafts={drafts} setting="registration.allow" />
        <DomainRow drafts={drafts} setting="registration.deny" />
      </Card>
      <SettingsSubmit drafts={drafts} />
    </>
  );
}

function NumberRow({
  drafts,
  setting,
  disabled,
}: {
  drafts: SettingDrafts<Key>;
  setting: NumberKey;
  disabled?: boolean;
}) {
  return (
    <DraftRow drafts={drafts} setting={setting}>
      <NumberField
        label={drafts.label(setting)}
        value={drafts.value(setting) as string}
        onChange={(value) => drafts.set(setting, value)}
        min={bounds[setting].min}
        max={bounds[setting].max}
        invalid={drafts.invalid(setting)}
        disabled={disabled}
      />
    </DraftRow>
  );
}

function DomainRow({
  drafts,
  setting,
}: {
  drafts: SettingDrafts<Key>;
  setting: "registration.allow" | "registration.deny";
}) {
  const { t } = useTranslation();
  return (
    <DraftRow drafts={drafts} setting={setting}>
      <ChipField
        label={drafts.label(setting)}
        values={drafts.value(setting) as readonly string[]}
        onChange={(values) => drafts.set(setting, values)}
        removeLabel={(value) => t("drafts.remove", { value })}
        placeholder={t("settings.registration.domainPlaceholder")}
        mono
      />
    </DraftRow>
  );
}
