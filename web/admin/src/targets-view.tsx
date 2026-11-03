import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Button, DataState, Dialog, Icon, moneyInput, TextField, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { targetsQuery } from "./queries";
import {
  type CountComparison,
  type MoneyComparison,
  readTargets,
  type TargetMetric,
  type TargetTone,
  targetMonth,
  targetTone,
} from "./targets";

// TargetsAction is the month picker and the button that opens the editor. The month is a UTC calendar month, the
// period a target covers; the dashboard prorates it over any window that touches it.
export function TargetsAction({ anchor, currency }: { anchor: Date; currency: string }) {
  const { t } = useTranslation();
  const [month, setMonth] = useState(() => targetMonth(anchor));
  const [open, setOpen] = useState(false);
  return (
    <div className="flex flex-wrap items-end gap-2">
      <TextField
        label={t("overview.targets.month")}
        name="month"
        type="month"
        value={month}
        onChange={(event) => setMonth(event.target.value)}
        className="w-44"
      />
      <Button size="md" onClick={() => setOpen(true)} disabled={month === ""}>
        <Icon name="pencil" />
        {t("overview.targets.edit")}
      </Button>
      {open ? <TargetsDialog month={month} currency={currency} onClose={() => setOpen(false)} /> : null}
    </div>
  );
}

// TargetsDialog waits for the month's stored targets before it can fill the form. Loading and failure are dialogs of
// their own, so the page never shows a form over data it has not read.
function TargetsDialog({ month, currency, onClose }: { month: string; currency: string; onClose: () => void }) {
  const { t } = useTranslation();
  const targets = useQuery(targetsQuery(month));
  if (targets.status === "success") {
    return <TargetsForm month={month} currency={currency} view={targets.data} onClose={onClose} />;
  }
  return (
    <Dialog title={t("overview.targets.title", { month })} onClose={onClose}>
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
  return (
    <ChangeDialog
      operation={`operations-targets:${month}`}
      title={t("overview.targets.title", { month })}
      intro={<p>{t("overview.targets.intro")}</p>}
      action={t("overview.targets.save")}
      fields={(error) => <TargetFields view={view} currency={currency} error={error} />}
      read={(values) =>
        readTargets(values, currency, {
          count: t("overview.targets.countInvalid"),
          money: t("overview.targets.moneyInvalid"),
        })
      }
      send={(targets, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/reports/targets/{month}", {
            params: { path: { month }, header: { "Idempotency-Key": key } },
            body: { ...targets, reason },
          }),
        )
      }
      done={() => <p>{t("overview.targets.saved")}</p>}
      onClose={onClose}
    />
  );
}

function TargetFields({
  view,
  currency,
  error,
}: {
  view: AdminSchemas["OperationsTargets"];
  currency: string;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextField
        name="calls"
        label={t("overview.targets.calls")}
        hint={t("overview.targets.callsHint")}
        inputMode="numeric"
        autoComplete="off"
        defaultValue={view.calls}
        error={errors.field(error, "calls")}
      />
      <TextField
        name="errors"
        label={t("overview.targets.errors")}
        hint={t("overview.targets.errorsHint")}
        inputMode="numeric"
        autoComplete="off"
        defaultValue={view.errors}
        error={errors.field(error, "errors")}
      />
      <TextField
        name="revenue"
        label={t("overview.targets.revenue", { currency })}
        hint={t("overview.targets.revenueHint")}
        inputMode="decimal"
        autoComplete="off"
        defaultValue={view.revenue ? moneyInput(view.revenue.amount) : undefined}
        error={errors.field(error, "revenue")}
      />
      <TextField
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

// CountTargetNote and MoneyTargetNote show one figure's target and gap under its value. An absent comparison is a
// metric without a target; an absent gap is a comparison the node withholds because the call log is incomplete.
export function CountTargetNote({ metric, comparison }: { metric: "calls" | "errors"; comparison?: CountComparison }) {
  const { t } = useTranslation();
  if (comparison === undefined) {
    return <span className="text-t4">{t("overview.targets.none")}</span>;
  }
  return (
    <TargetLine
      metric={metric}
      target={String(comparison.target)}
      gap={comparison.gap === undefined ? undefined : String(comparison.gap)}
    />
  );
}

export function MoneyTargetNote({ metric, comparison }: { metric: "revenue" | "cost"; comparison?: MoneyComparison }) {
  const { t } = useTranslation();
  if (comparison === undefined) {
    return <span className="text-t4">{t("overview.targets.none")}</span>;
  }
  return (
    <TargetLine
      metric={metric}
      target={comparison.target.amount}
      gap={comparison.gap?.amount}
      currency={comparison.target.currency}
    />
  );
}

function TargetLine({
  metric,
  target,
  gap,
  currency,
}: {
  metric: TargetMetric;
  target: string;
  gap?: string;
  currency?: string;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const money = metric === "revenue" || metric === "cost";
  const value = money ? format.money({ amount: target, currency: currency ?? "" }) : format.number(Number(target));
  if (gap === undefined) {
    return (
      <span>
        {t("overview.targets.target", { value })}
        {" · "}
        <span className="text-t4">{t("overview.targets.incomplete")}</span>
      </span>
    );
  }
  const sign = gap.startsWith("-") ? -1 : gap === "0" ? 0 : 1;
  const tone = targetTone(metric, sign);
  const absolute = gap.startsWith("-") ? gap.slice(1) : gap;
  const amount = money ? format.money({ amount: absolute, currency: currency ?? "" }) : format.number(Number(absolute));
  const toneClass = toneClassOf(tone);
  return (
    <span>
      {t("overview.targets.target", { value })}
      {" · "}
      <span className={toneClass}>
        {tone === "at" ? t("overview.targets.at") : t(`overview.targets.${tone}`, { amount })}
      </span>
    </span>
  );
}

function toneClassOf(tone: TargetTone): string {
  if (tone === "ahead" || tone === "under") {
    return "text-ok";
  }
  return tone === "at" ? "text-t3" : "text-bad";
}
