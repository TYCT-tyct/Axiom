import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, Cluster, DataState, Dialog, Field, MonthField, moneyInput, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, useSnapshot } from "./change";
import { targetsQuery } from "./queries";
import { formatTargetRate, legacyTargetMetrics, readTargets, type TargetMetric, targetMonth } from "./targets";

// TargetsAction is the month picker and the button that opens the editor. The month is a UTC calendar month, the
// period a target covers; the dashboard prorates it over any window that touches it. The picker says both: its month
// and year read in the page's language, and its hint names the zone the page's other times are in.
export function TargetsAction({ anchor, currency }: { anchor: Date; currency: string }) {
  const { t } = useTranslation();
  const format = useFormat();
  const [month, setMonth] = useState(() => targetMonth(anchor));
  const [open, setOpen] = useState(false);
  return (
    <Cluster>
      <MonthField
        label={t("overview.targets.month")}
        hint={t("overview.targets.monthHint", { zone: format.zone() })}
        value={month}
        onChange={setMonth}
      />
      <ActionButton onClick={() => setOpen(true)} icon="pencil">
        {t("overview.targets.edit")}
      </ActionButton>
      {open ? <TargetsDialog month={month} currency={currency} onClose={() => setOpen(false)} /> : null}
    </Cluster>
  );
}

// TargetsDialog waits for the month's stored targets before it can fill the form. Loading and failure are dialogs of
// their own, so the page never shows a form over data it has not read.
function TargetsDialog({ month, currency, onClose }: { month: string; currency: string; onClose: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const targets = useQuery(targetsQuery(month));
  if (targets.status === "success") {
    return <TargetsForm month={month} currency={currency} view={targets.data} onClose={onClose} />;
  }
  return (
    <Dialog title={t("overview.targets.title", { month: format.monthLabel(month) })} onClose={onClose}>
      <DataState query={targets}>{() => null}</DataState>
    </Dialog>
  );
}

function TargetsForm({
  month,
  currency,
  view,
  onClose,
}: {
  month: string;
  currency: string;
  view: AdminSchemas["OperationsTargets"];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  // Keep the values the form was opened with, and its revision, while another editor may change the month. A stale
  // write is refused; reload then fills both from the query that the refusal has refreshed.
  const [read, reload] = useSnapshot(view);
  const metrics = read.metrics ?? legacyTargetMetrics;
  const full = read.metrics?.includes("paid_users") ?? false;
  return (
    <ChangeDialog
      operation={`operations-targets:${month}`}
      title={t("overview.targets.title", { month: format.monthLabel(month) })}
      intro={<p>{t("overview.targets.intro")}</p>}
      action={t("overview.targets.save")}
      fields={(error) => <TargetFields view={read} metrics={metrics} currency={currency} error={error} />}
      read={(values) =>
        readTargets(
          values,
          currency,
          {
            count: t("overview.targets.countInvalid"),
            money: t("overview.targets.moneyInvalid"),
            rate: t("overview.targets.rateInvalid"),
          },
          metrics,
        )
      }
      reload={reload}
      send={(targets, { reason, key }) => {
        const body = { ...targets, rev: read.rev, reason };
        if (!full) {
          return unwrapChange(
            adminApi.PUT("/api/admin/v1/reports/targets/{month}", {
              params: { path: { month }, header: { "Idempotency-Key": key } },
              body,
            }),
          );
        }
        return unwrapChange(
          adminApi.PUT("/api/admin/v1/reports/targets/{month}/all", {
            params: {
              path: { month },
              header: { "Idempotency-Key": key, "X-Axiom-Target-Metrics": "all" },
            },
            body: { ...body, all_metrics: true },
          }),
        );
      }}
      done={() => <p>{t("overview.targets.saved")}</p>}
      onClose={onClose}
    />
  );
}

type TargetFieldProps = {
  view: AdminSchemas["OperationsTargets"];
  currency: string;
  error: unknown;
};

function TargetFields({ view, metrics, currency, error }: TargetFieldProps & { metrics: readonly TargetMetric[] }) {
  return (
    <>
      <LegacyTargetFields view={view} currency={currency} error={error} />
      {metrics.includes("paid_users") ? <CommercialTargetFields view={view} currency={currency} error={error} /> : null}
    </>
  );
}

function LegacyTargetFields({ view, currency, error }: TargetFieldProps) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Field
        name="calls"
        label={t("overview.targets.calls")}
        hint={t("overview.targets.callsHint")}
        inputMode="numeric"
        autoComplete="off"
        defaultValue={view.calls}
        error={errors.field(error, "calls")}
      />
      <Field
        name="errors"
        label={t("overview.targets.errors")}
        hint={t("overview.targets.errorsHint")}
        inputMode="numeric"
        autoComplete="off"
        defaultValue={view.errors}
        error={errors.field(error, "errors")}
      />
      <Field
        name="revenue"
        label={t("overview.targets.revenue", { currency })}
        hint={t("overview.targets.revenueHint")}
        inputMode="decimal"
        autoComplete="off"
        defaultValue={view.revenue ? moneyInput(view.revenue.amount) : undefined}
        error={errors.field(error, "revenue")}
      />
      <Field
        name="cost"
        label={t("overview.targets.cost", { currency })}
        hint={t("overview.targets.costHint")}
        inputMode="decimal"
        autoComplete="off"
        defaultValue={view.cost ? moneyInput(view.cost.amount) : undefined}
        error={errors.field(error, "cost")}
      />
    </>
  );
}

function CommercialTargetFields({ view, currency, error }: TargetFieldProps) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Field
        name="paid_users"
        label={t("overview.targets.paidUsers")}
        hint={t("overview.targets.paidUsersHint")}
        inputMode="numeric"
        autoComplete="off"
        defaultValue={view.paid_users}
        error={errors.field(error, "paid_users")}
      />
      <Field
        name="monthly_revenue"
        label={t("overview.targets.monthlyRevenue", { currency })}
        hint={t("overview.targets.monthlyRevenueHint")}
        inputMode="decimal"
        autoComplete="off"
        defaultValue={view.monthly_revenue ? moneyInput(view.monthly_revenue.amount) : undefined}
        error={errors.field(error, "monthly_revenue")}
      />
      <Field
        name="gross_margin"
        label={t("overview.targets.grossMargin")}
        hint={t("overview.targets.grossMarginHint")}
        inputMode="decimal"
        autoComplete="off"
        defaultValue={view.gross_margin === undefined ? undefined : formatTargetRate(view.gross_margin).slice(0, -1)}
        error={errors.field(error, "gross_margin")}
      />
      <Field
        name="retention"
        label={t("overview.targets.retention")}
        hint={t("overview.targets.retentionHint")}
        inputMode="decimal"
        autoComplete="off"
        defaultValue={view.retention === undefined ? undefined : formatTargetRate(view.retention).slice(0, -1)}
        error={errors.field(error, "retention")}
      />
    </>
  );
}
