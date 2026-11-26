import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { PageHead } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { PageBody } from "../parts";
import { logRetentionQuery } from "../policy-queries";
import { probeSettingsQuery, segmentSettingsQuery } from "../queries";
import { type NumberSpec, numberOf, numberPatch, numberText, numberValid } from "../setting-fields";
import { NumberSetting, OptionalView, SettingsCard } from "../setting-rows";
import type { TimeUnit } from "../setting-units";
import { SettingsSubmit, type SettingValue, useSettingDrafts } from "../settings-drafts";

type Segments = AdminSchemas["SegmentSettingsView"];
type Probes = AdminSchemas["ProbeSettingsView"];

// The platform's gateway numbers, each with the bounds of api/admin.yaml in the API's own unit and the unit the design
// writes it in. What is left empty falls back to the default in code; the retention is always set.
const segments = {
  "gateway.outputTokens": { name: "output_tokens", min: 1, max: 1_000_000 },
  "gateway.overshoot": { name: "overshoot_seconds", min: 1, max: 600, unit: "second" },
} as const satisfies Record<string, NumberSpec>;
const probes = {
  "gateway.probeBudget": { name: "hourly_budget", min: 1, max: 100_000 },
  "gateway.probeConcurrency": { name: "concurrency", min: 1, max: 64 },
  "gateway.verifyEvery": { name: "verify_every_seconds", min: 60, max: 2_592_000, unit: "minute" },
  "gateway.verifyMaxAge": { name: "verify_max_age_seconds", min: 60, max: 2_592_000, unit: "hour" },
  "gateway.verifyRetry": { name: "verify_retry_seconds", min: 60, max: 86_400, unit: "minute" },
  "gateway.probeBase": { name: "probe_base_seconds", min: 1, max: 600, unit: "second" },
} as const satisfies Record<string, NumberSpec>;
const retention: NumberSpec = { name: "hot_days", min: 0, max: 3650 };

type SegmentKey = keyof typeof segments;
type ProbeKey = keyof typeof probes;
type Key = SegmentKey | ProbeKey | "gateway.hotDays";

export default function GatewaySettings() {
  const { t } = useTranslation();
  const segmentView = useQuery(segmentSettingsQuery);
  const probeView = useQuery(probeSettingsQuery);
  const retentionView = useQuery(logRetentionQuery);
  return (
    <PageBody>
      <PageHead title={t("shell.nav.gateway")} lede={t("settings.pages.gateway.lede")} />
      <OptionalView query={segmentView}>
        {(segmentsLoaded) => (
          <OptionalView query={probeView}>
            {(probesLoaded) => (
              <OptionalView query={retentionView}>
                {(retentionLoaded) => (
                  <Rows segments={segmentsLoaded} probes={probesLoaded} days={retentionLoaded?.hot_days} />
                )}
              </OptionalView>
            )}
          </OptionalView>
        )}
      </OptionalView>
    </PageBody>
  );
}

type RowsProps = {
  // undefined where the node has no such operation: the card is left out
  segments: Segments | undefined;
  probes: Probes | undefined;
  days: number | undefined;
};

const stored = (view: Segments | Probes | undefined) =>
  ({ ...view?.defaults, ...view?.settings }) as Record<string, number | undefined>;

function savedOf(props: RowsProps): Record<Key, SettingValue> {
  const saved = { "gateway.hotDays": numberText(retention, props.days) } as Record<Key, SettingValue>;
  for (const key of Object.keys(segments) as SegmentKey[]) {
    saved[key] = numberText(segments[key], stored(props.segments)[segments[key].name]);
  }
  for (const key of Object.keys(probes) as ProbeKey[]) {
    saved[key] = numberText(probes[key], stored(props.probes)[probes[key].name]);
  }
  return saved;
}

const specOf = (key: Key): NumberSpec =>
  key === "gateway.hotDays" ? retention : key in segments ? segments[key as SegmentKey] : probes[key as ProbeKey];

function useGatewayDrafts(props: RowsProps) {
  const { t } = useTranslation();
  const saved = savedOf(props);
  return useSettingDrafts<Key>({
    scope: "settings:gateway",
    saved,
    check: (key, value) => numberValid(specOf(key), value, key !== "gateway.hotDays"),
    show: (key, value) => (key !== "gateway.hotDays" && value === "" ? t("drafts.default") : undefined),
    writes: {
      setSegmentSettings: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/segments", {
            params: { header: { "Idempotency-Key": key } },
            body: {
              settings: {
                ...props.segments?.settings,
                ...numberPatch(segments, values, saved),
              } as Segments["settings"],
              reason,
            },
          }),
        ),
      setQualityProbeSettings: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/quality-probes", {
            params: { header: { "Idempotency-Key": key } },
            body: {
              settings: { ...props.probes?.settings, ...numberPatch(probes, values, saved) } as Probes["settings"],
              reason,
            },
          }),
        ),
      setRetention: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/retention", {
            params: { header: { "Idempotency-Key": key } },
            body: { hot_days: numberOf(retention, String(values["gateway.hotDays"])) ?? 0, reason },
          }),
        ),
    },
  });
}

function Rows(props: RowsProps) {
  const { t } = useTranslation();
  const drafts = useGatewayDrafts(props);
  const unit = (key: ProbeKey) =>
    t(`unit.${(probes[key] as { unit?: TimeUnit }).unit ?? (key === "gateway.probeBudget" ? "times" : "count")}`);
  return (
    <>
      {props.segments === undefined ? null : (
        <SettingsCard
          scope="platform"
          title={t("settings.pages.gateway.reserve")}
          lede={t("settings.pages.gateway.reserveLede")}
        >
          <NumberSetting drafts={drafts} setting="gateway.outputTokens" unit={t("unit.tokens")} />
          <NumberSetting drafts={drafts} setting="gateway.overshoot" unit={t("unit.second")} />
        </SettingsCard>
      )}
      {props.probes === undefined ? null : (
        <SettingsCard
          scope="platform"
          title={t("settings.pages.gateway.probes")}
          lede={t("settings.pages.gateway.probesLede")}
        >
          {(Object.keys(probes) as ProbeKey[]).map((key) => (
            <NumberSetting key={key} drafts={drafts} setting={key} unit={unit(key)} />
          ))}
        </SettingsCard>
      )}
      {props.days === undefined ? null : (
        <SettingsCard scope="platform" title={t("settings.pages.gateway.log")}>
          <NumberSetting drafts={drafts} setting="gateway.hotDays" unit={t("unit.day")} />
        </SettingsCard>
      )}
      <SettingsSubmit drafts={drafts} />
    </>
  );
}

export { default as messages } from "../locales/settings";
