import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useDuration } from "./durations";
import { probeSettingsQuery } from "./queries";
import { type ValueField, ValueSettings } from "./value-settings";

// ProbeSettings is the platform's recovery probe budget, its fingerprint check schedule, and the first probe delay.
export function ProbeSettings() {
  const { t } = useTranslation();
  const format = useFormat();
  const duration = useDuration();
  const query = useQuery(probeSettingsQuery);
  const fields: ValueField[] = [
    {
      key: "hourly_budget",
      label: t("settings.probes.budget"),
      hint: t("settings.probes.countHint", { min: format.number(1), max: format.number(100_000) }),
      min: 1,
      max: 100_000,
      format: (n) => format.number(n),
    },
    {
      key: "concurrency",
      label: t("settings.probes.concurrency"),
      hint: t("settings.probes.countHint", { min: format.number(1), max: format.number(64) }),
      min: 1,
      max: 64,
      format: (n) => format.number(n),
    },
    {
      key: "verify_every_seconds",
      label: t("settings.probes.every"),
      hint: t("settings.probes.timeHint", { min: format.number(60), max: format.number(2_592_000) }),
      min: 60,
      max: 2_592_000,
      format: duration,
    },
    {
      key: "verify_max_age_seconds",
      label: t("settings.probes.maxAge"),
      hint: t("settings.probes.timeHint", { min: format.number(60), max: format.number(2_592_000) }),
      min: 60,
      max: 2_592_000,
      format: duration,
    },
    {
      key: "verify_retry_seconds",
      label: t("settings.probes.retry"),
      hint: t("settings.probes.timeHint", { min: format.number(60), max: format.number(86_400) }),
      min: 60,
      max: 86_400,
      format: duration,
    },
    {
      key: "probe_base_seconds",
      label: t("settings.probes.base"),
      hint: t("settings.probes.timeHint", { min: format.number(1), max: format.number(600) }),
      min: 1,
      max: 600,
      format: duration,
    },
  ];
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
