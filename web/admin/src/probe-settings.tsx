import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useDuration } from "./durations";
import { probeSettingsQuery } from "./queries";
import { type ValueField, ValueSettings } from "./value-settings";

// The numbers of the probe record, with the bounds the contract states. The unit names the hint in the locale
// (`countHint` or `timeHint`) and how the value reads: a count as a number, seconds as a duration.
const probes = [
  { key: "hourly_budget", label: "budget", unit: "count", min: 1, max: 100_000 },
  { key: "concurrency", label: "concurrency", unit: "count", min: 1, max: 64 },
  { key: "verify_every_seconds", label: "every", unit: "time", min: 60, max: 2_592_000 },
  { key: "verify_max_age_seconds", label: "maxAge", unit: "time", min: 60, max: 2_592_000 },
  { key: "verify_retry_seconds", label: "retry", unit: "time", min: 60, max: 86_400 },
  { key: "probe_base_seconds", label: "base", unit: "time", min: 1, max: 600 },
] as const;

// ProbeSettings is the platform's recovery probe budget, its fingerprint check schedule, and the first probe delay.
export function ProbeSettings() {
  const { t } = useTranslation();
  const format = useFormat();
  const duration = useDuration();
  const query = useQuery(probeSettingsQuery);
  const fields: ValueField[] = probes.map((probe) => ({
    key: probe.key,
    label: t(`settings.probes.${probe.label}`),
    hint: t(`settings.probes.${probe.unit}Hint`, {
      min: format.number(probe.min),
      max: format.number(probe.max),
    }),
    min: probe.min,
    max: probe.max,
    format: probe.unit === "count" ? (n) => format.number(n) : duration,
  }));
  return (
    <ValueSettings
      query={query}
      operation="probe-settings"
      title={t("settings.probes.title")}
      unavailableLabel={t("settings.probes.unavailableLabel")}
      unavailable={t("settings.probes.unavailable")}
      edit={t("settings.probes.edit")}
      intro={t("settings.probes.intro")}
      save={t("settings.probes.save")}
      saved={t("settings.probes.saved")}
      invalid={t("settings.probes.invalid")}
      fields={fields}
      send={(settings, reason, key) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/quality-probes", {
            params: { header: { "Idempotency-Key": key } },
            body: { settings: settings as AdminSchemas["ProbeSettings"], reason },
          }),
        )
      }
    />
  );
}
