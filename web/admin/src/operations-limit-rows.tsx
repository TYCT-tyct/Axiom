import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { ActionButton, Card, Text } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { PriceEnvelopeDialog } from "./operations-exec-dialogs";
import { type NumberSpec, numberText, numberValid } from "./setting-fields";
import { NumberSetting, SettingsCard } from "./setting-rows";
import { countOf, scaledOf, scaledText, within } from "./setting-units";
import { SettingsSubmit, type SettingValue, useSettingDrafts } from "./settings-drafts";

type SiteId = AdminSchemas["SiteId"];
type Limits = AdminSchemas["OperationsAgentLimits"];
type Envelope = AdminSchemas["OperationsPriceEnvelopeSetting"];

type LimitKey = "agent.requests" | "agent.moneyMinute" | "agent.moneyHour" | "agent.alertsHour";
type EnvelopeKey = "agent.margin" | "agent.dailyChange" | "agent.dailyChanges" | "agent.dailyModels";
type Key = LimitKey | EnvelopeKey;

// The agent's execution limits, each held to its own ceiling (what the node reports, else the contract's), and the
// envelope its price changes keep to, in basis points on the wire and written as percentages.
const limits: Record<LimitKey, { field: keyof Limits & string; ceiling: keyof Limits & string; fallback: number }> = {
  "agent.requests": { field: "requests_per_minute", ceiling: "requests_per_minute_max", fallback: 600 },
  "agent.moneyMinute": { field: "money_writes_per_minute", ceiling: "money_writes_per_minute_max", fallback: 30 },
  "agent.moneyHour": { field: "money_writes_per_hour", ceiling: "money_writes_per_hour_max", fallback: 300 },
  "agent.alertsHour": { field: "alerts_per_hour", ceiling: "alerts_per_hour_max", fallback: 60 },
};
const scale = 100;
const envelope: Record<EnvelopeKey, { field: keyof Envelope["limits"]; max: number; percent: boolean }> = {
  "agent.margin": { field: "min_margin_bps", max: 10_000, percent: true },
  "agent.dailyChange": { field: "model_daily_change_bps", max: 100_000_000, percent: true },
  "agent.dailyChanges": { field: "daily_changes", max: 1_000_000, percent: false },
  "agent.dailyModels": { field: "daily_models", max: 1_000_000, percent: false },
};

const number = (view: Limits, key: LimitKey) => view[limits[key].field] as number | undefined;
const specOf = (view: Limits, key: LimitKey): NumberSpec => ({
  name: limits[key].field,
  min: 1,
  max: (view[limits[key].ceiling] as number | undefined) ?? limits[key].fallback,
});

function savedOf(view: Limits, held: Envelope | undefined): Record<Key, SettingValue> {
  const saved = {} as Record<Key, SettingValue>;
  for (const key of Object.keys(limits) as LimitKey[]) saved[key] = numberText(specOf(view, key), number(view, key));
  for (const key of Object.keys(envelope) as EnvelopeKey[]) {
    const { field, percent } = envelope[key];
    const stored = held?.limits[field];
    saved[key] = stored === undefined ? "" : percent ? scaledText(stored, scale) : String(stored);
  }
  return saved;
}

// envelopeOf is what an envelope row's text stands for on the wire; undefined when it is not a number within bounds.
function envelopeOf(key: EnvelopeKey, text: string): number | undefined {
  const { max, percent } = envelope[key];
  const value = percent ? scaledOf(text, scale) : countOf(text);
  return within(value, 0, max) ? value : undefined;
}

type Values = Record<Key, SettingValue>;

// limitsBody is the agent limits as the rows say them; a limit the node does not report stays out, and so keeps what it is.
function limitsBody(view: Limits, values: Values): Omit<AdminSchemas["OperationsAgentLimitsUpdate"], "reason"> {
  const body: Omit<AdminSchemas["OperationsAgentLimitsUpdate"], "reason"> = {
    alerts_per_hour: Number(values["agent.alertsHour"]),
    rev: view.rev ?? 0,
  };
  if (view.requests_per_minute !== undefined) body.requests_per_minute = Number(values["agent.requests"]);
  if (view.money_writes_per_minute !== undefined) body.money_writes_per_minute = Number(values["agent.moneyMinute"]);
  if (view.money_writes_per_hour !== undefined) body.money_writes_per_hour = Number(values["agent.moneyHour"]);
  return body;
}

function envelopeBody(values: Values, rev: number): Omit<AdminSchemas["OperationsPriceEnvelopeUpdate"], "reason"> {
  const read = (key: EnvelopeKey) => envelopeOf(key, String(values[key])) ?? 0;
  return {
    limits: {
      min_margin_bps: read("agent.margin"),
      model_daily_change_bps: read("agent.dailyChange"),
      daily_changes: read("agent.dailyChanges"),
      daily_models: read("agent.dailyModels"),
    },
    rev,
  };
}

function useAgentDrafts(siteId: SiteId, view: Limits, held: Envelope | undefined) {
  return useSettingDrafts<Key>({
    scope: `settings:agent:${siteId}`,
    saved: savedOf(view, held),
    check: (key, value) =>
      key in limits
        ? numberValid(specOf(view, key as LimitKey), value, false)
        : typeof value === "string" && envelopeOf(key as EnvelopeKey, value) !== undefined,
    writes: {
      setOperationsAgentLimits: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/operations/agent-limits", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...limitsBody(view, values), reason },
          }),
        ),
      setOperationsPriceEnvelope: (values, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/operations/price-envelopes/{site_id}", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { ...envelopeBody(values, held?.rev ?? 0), reason },
          }),
        ),
    },
  });
}

export function AgentLimitsRows({
  siteId,
  limits: view,
  envelope: held,
  envelopesKnown,
}: {
  siteId: SiteId;
  limits: Limits | undefined;
  envelope: Envelope | undefined;
  // whether the node answers for envelopes at all, so that none for this site can be set
  envelopesKnown: boolean;
}) {
  const { t } = useTranslation();
  const [creating, setCreating] = useState(false);
  const shown = view ?? {};
  const drafts = useAgentDrafts(siteId, shown, held);
  return (
    <>
      {view === undefined ? null : (
        <SettingsCard scope="platform" title={t("settings.pages.agent.execution")}>
          {view.requests_per_minute === undefined ? null : (
            <NumberSetting drafts={drafts} setting="agent.requests" unit={t("unit.times")} />
          )}
          {view.money_writes_per_minute === undefined ? null : (
            <NumberSetting drafts={drafts} setting="agent.moneyMinute" unit={t("unit.times")} />
          )}
          {view.money_writes_per_hour === undefined ? null : (
            <NumberSetting drafts={drafts} setting="agent.moneyHour" unit={t("unit.times")} />
          )}
          <NumberSetting drafts={drafts} setting="agent.alertsHour" unit={t("unit.items")} />
        </SettingsCard>
      )}
      {held ? (
        <SettingsCard scope="site" title={t("settings.pages.agent.range")} lede={t("settings.pages.agent.rangeLede")}>
          <NumberSetting drafts={drafts} setting="agent.margin" unit={t("unit.percent")} decimals={2} />
          <NumberSetting drafts={drafts} setting="agent.dailyChange" unit={t("unit.percent")} decimals={2} />
          <NumberSetting drafts={drafts} setting="agent.dailyChanges" unit={t("unit.times")} />
          <NumberSetting drafts={drafts} setting="agent.dailyModels" unit={t("unit.count")} />
        </SettingsCard>
      ) : null}
      {held || !envelopesKnown ? null : (
        <Card
          title={t("settings.pages.agent.range")}
          lede={t("settings.pages.agent.rangeLede")}
          scope={{ label: t("scope.site"), tip: t("scope.siteTip") }}
          actions={
            <ActionButton size="sm" icon="plus" onClick={() => setCreating(true)}>
              {t("operations.limits.envelope.new")}
            </ActionButton>
          }
        >
          <Text tone="muted">{t("operations.limits.envelope.emptyBody")}</Text>
        </Card>
      )}
      {creating ? <PriceEnvelopeDialog defaultSiteId={siteId} onClose={() => setCreating(false)} /> : null}
      <SettingsSubmit drafts={drafts} />
    </>
  );
}
