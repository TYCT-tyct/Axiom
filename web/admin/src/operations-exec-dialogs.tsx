import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Field, Text, useFormat } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem, useSnapshot } from "./change";

type AgentLimits = AdminSchemas["OperationsAgentLimits"];
type AgentLimitsBody = Omit<AdminSchemas["OperationsAgentLimitsUpdate"], "reason">;
type Envelope = AdminSchemas["OperationsPriceEnvelopeSetting"];
type EnvelopeLimits = AdminSchemas["OperationsPriceEnvelope"];

const siteIdPattern = /^site_[0-9a-hjkmnp-tv-z]{26}$/;

function parseBoundedInt(raw: string | undefined, min: number, max: number): number | null {
  const trimmed = (raw ?? "").trim();
  if (!/^\d+$/.test(trimmed)) {
    return null;
  }
  const value = Number(trimmed);
  return Number.isInteger(value) && value >= min && value <= max ? value : null;
}

const agentLimitSpec = [
  { name: "requests_per_minute", maxKey: "requests_per_minute_max", fallbackMax: 600, label: "requestsPerMinute" },
  {
    name: "money_writes_per_minute",
    maxKey: "money_writes_per_minute_max",
    fallbackMax: 30,
    label: "moneyWritesPerMinute",
  },
  { name: "money_writes_per_hour", maxKey: "money_writes_per_hour_max", fallbackMax: 300, label: "moneyWritesPerHour" },
  { name: "alerts_per_hour", maxKey: "alerts_per_hour_max", fallbackMax: 60, label: "alertsPerHour" },
] as const;

function readAgentLimits(
  values: Record<string, string>,
  view: AgentLimits,
  invalid: string,
): AgentLimitsBody | Problem {
  const alerts = parseBoundedInt(values.alerts_per_hour, 1, view.alerts_per_hour_max ?? 60);
  if (alerts === null) {
    return { problem: invalid };
  }
  const out: AgentLimitsBody = { alerts_per_hour: alerts, rev: view.rev ?? 0 };
  for (const field of agentLimitSpec) {
    if (field.name === "alerts_per_hour" || view[field.name] === undefined) {
      continue;
    }
    const max = view[field.maxKey] ?? field.fallbackMax;
    const parsed = parseBoundedInt(values[field.name], 1, max);
    if (parsed === null) {
      return { problem: invalid };
    }
    out[field.name] = parsed;
  }
  return out;
}

export function AgentLimitsDialog({ view, onClose }: { view: AgentLimits; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [read, reload] = useSnapshot(view);
  return (
    <ChangeDialog
      operation="ops-agent-limits"
      title={t("operations.limits.agent.editTitle")}
      intro={<p>{t("operations.limits.agent.editIntro")}</p>}
      action={t("operations.limits.agent.saveAction")}
      reload={reload}
      fields={(error) => (
        <>
          {agentLimitSpec.map((field) => {
            if (field.name !== "alerts_per_hour" && read[field.name] === undefined) {
              return null;
            }
            const max = read[field.maxKey] ?? field.fallbackMax;
            return (
              <Field
                key={field.name}
                label={t(`operations.limits.agent.${field.label}`)}
                hint={t("operations.limits.agent.rangeHint", { max })}
                name={field.name}
                defaultValue={String(read[field.name] ?? max)}
                inputMode="numeric"
                autoComplete="off"
                error={errors.field(error, field.name)}
              />
            );
          })}
        </>
      )}
      read={(values) => readAgentLimits(values, read, t("operations.limits.agent.invalid"))}
      send={(body, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/operations/agent-limits", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...body, reason },
          }),
        )
      }
      done={(saved) => <Text>{t("operations.limits.agent.saved", { rev: saved.rev ?? 0 })}</Text>}
      onClose={onClose}
    />
  );
}

type EnvelopePayload = { siteId: string; limits: EnvelopeLimits; rev: number };

function readEnvelope(
  values: Record<string, string>,
  existing: Envelope | undefined,
  words: { siteInvalid: string; invalid: string },
): EnvelopePayload | Problem {
  const siteId = existing?.site_id ?? (values.site_id ?? "").trim();
  if (!siteIdPattern.test(siteId)) {
    return { problem: words.siteInvalid };
  }
  const min_margin_bps = parseBoundedInt(values.min_margin_bps, 0, 10_000);
  const model_daily_change_bps = parseBoundedInt(values.model_daily_change_bps, 0, 100_000_000);
  const daily_changes = parseBoundedInt(values.daily_changes, 0, 1_000_000);
  const daily_models = parseBoundedInt(values.daily_models, 0, 1_000_000);
  if (min_margin_bps === null || model_daily_change_bps === null || daily_changes === null || daily_models === null) {
    return { problem: words.invalid };
  }
  return {
    siteId,
    limits: { min_margin_bps, model_daily_change_bps, daily_changes, daily_models },
    rev: existing?.rev ?? 0,
  };
}

export function PriceEnvelopeDialog({
  envelope,
  defaultSiteId,
  onClose,
}: {
  envelope?: Envelope;
  defaultSiteId: AdminSchemas["SiteId"];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [read, reload] = useSnapshot(envelope);
  const current = read;
  const words = {
    siteInvalid: t("operations.limits.envelope.siteIdInvalid"),
    invalid: t("operations.limits.envelope.invalid"),
  };
  return (
    <ChangeDialog
      operation={current ? `ops-envelope:${current.site_id}` : "ops-envelope:new"}
      title={
        current
          ? t("operations.limits.envelope.editTitle", { siteId: current.site_id })
          : t("operations.limits.envelope.createTitle")
      }
      intro={<p>{t("operations.limits.envelope.dialogIntro")}</p>}
      action={t("operations.limits.envelope.saveAction")}
      reload={envelope ? reload : undefined}
      fields={(error) => <EnvelopeFields envelope={current} defaultSiteId={defaultSiteId} error={error} />}
      read={(values) => readEnvelope(values, current, words)}
      send={(payload, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/operations/price-envelopes/{site_id}", {
            params: { path: { site_id: payload.siteId }, header: { "Idempotency-Key": key } },
            body: { limits: payload.limits, rev: payload.rev, reason },
          }),
        )
      }
      done={(saved) => <Text>{t("operations.limits.envelope.saved", { siteId: saved.site_id, rev: saved.rev })}</Text>}
      onClose={onClose}
    />
  );
}

function EnvelopeFields({
  envelope,
  defaultSiteId,
  error,
}: {
  envelope?: Envelope;
  defaultSiteId: AdminSchemas["SiteId"];
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      {envelope ? null : (
        <Field
          label={t("operations.limits.envelope.siteId")}
          name="site_id"
          defaultValue={defaultSiteId}
          autoComplete="off"
          error={errors.field(error, "site_id")}
        />
      )}
      <Field
        label={t("operations.limits.envelope.minMarginBps")}
        hint={t("operations.limits.envelope.minMarginHint")}
        name="min_margin_bps"
        defaultValue={envelope ? String(envelope.limits.min_margin_bps) : "1500"}
        inputMode="numeric"
        autoComplete="off"
        error={errors.field(error, "limits/min_margin_bps")}
      />
      <Field
        label={t("operations.limits.envelope.modelDailyBps")}
        hint={t("operations.limits.envelope.modelDailyHint")}
        name="model_daily_change_bps"
        defaultValue={envelope ? String(envelope.limits.model_daily_change_bps) : "1000"}
        inputMode="numeric"
        autoComplete="off"
        error={errors.field(error, "limits/model_daily_change_bps")}
      />
      <Field
        label={t("operations.limits.envelope.dailyChanges")}
        hint={t("operations.limits.envelope.countHint")}
        name="daily_changes"
        defaultValue={envelope ? String(envelope.limits.daily_changes) : "20"}
        inputMode="numeric"
        autoComplete="off"
        error={errors.field(error, "limits/daily_changes")}
      />
      <Field
        label={t("operations.limits.envelope.dailyModels")}
        hint={t("operations.limits.envelope.countHint")}
        name="daily_models"
        defaultValue={envelope ? String(envelope.limits.daily_models) : "5"}
        inputMode="numeric"
        autoComplete="off"
        error={errors.field(error, "limits/daily_models")}
      />
    </>
  );
}

export function StopAgentDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <ChangeDialog
      operation="ops-agent-stop"
      title={t("operations.limits.stop.dialogTitle")}
      intro={<p>{t("operations.limits.stop.dialogIntro")}</p>}
      action={t("operations.limits.stop.action")}
      danger
      confirm={t("operations.limits.stop.confirmWord")}
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/operations/agent/stop", {
            params: { header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={(stopped) => (
        <Text>
          {stopped.canceled === undefined
            ? t("operations.limits.stop.done", { revoked: format.number(stopped.revoked) })
            : t("operations.limits.stop.doneWithCanceled", {
                revoked: format.number(stopped.revoked),
                canceled: format.number(stopped.canceled),
              })}
        </Text>
      )}
      onClose={onClose}
    />
  );
}
