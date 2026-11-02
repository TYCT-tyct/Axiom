import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useDuration } from "./durations";
import { segmentSettingsQuery } from "./queries";
import { type ValueField, ValueSettings } from "./value-settings";

// SegmentSettings is the platform's segment size and overshoot allowance, which routing and billing read together.
export function SegmentSettings() {
  const { t } = useTranslation();
  const format = useFormat();
  const duration = useDuration();
  const query = useQuery(segmentSettingsQuery);
  const fields: ValueField[] = [
    {
      key: "output_tokens",
      label: t("settings.segments.output"),
      hint: t("settings.segments.outputHint", { min: format.number(1), max: format.number(1_000_000) }),
      min: 1,
      max: 1_000_000,
      format: (n) => t("settings.segments.tokens", { count: n }),
    },
    {
      key: "overshoot_seconds",
      label: t("settings.segments.overshoot"),
      hint: t("settings.segments.overshootHint", { min: format.number(1), max: format.number(600) }),
      min: 1,
      max: 600,
      format: duration,
    },
  ];
  return (
    <ValueSettings
      query={query}
      operation="segment-settings"
      title={t("settings.segments.title")}
      unavailableLabel={t("settings.segments.unavailableLabel")}
      unavailable={t("settings.segments.unavailable")}
      edit={t("settings.segments.edit")}
      intro={t("settings.segments.intro")}
      save={t("settings.segments.save")}
      saved={t("settings.segments.saved")}
      invalid={t("settings.segments.invalid")}
      fields={fields}
      send={(settings, reason, key) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/segments", {
            params: { header: { "Idempotency-Key": key } },
            body: { settings: settings as AdminSchemas["SegmentSettings"], reason },
          }),
        )
      }
    />
  );
}
