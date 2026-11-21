import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Field, moneyInput, parseMoney, Select, Text } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, isProblem, type Problem, useSnapshot } from "./change";
import { paymentCurrencies } from "./credit-read";

type CapPolicy = AdminSchemas["OperationsCapPolicy"];
type CapCategory = AdminSchemas["OperationsCapCategory"];
type CapLimits = AdminSchemas["OperationsCapLimits"];

const categories: readonly CapCategory[] = ["money.top_up", "money.credit", "money.refund"];
const amountFields = [
  ["single_max", "singleMax"],
  ["daily_max", "dailyMax"],
  ["weekly_max", "weeklyMax"],
  ["object_daily_max", "objectDailyMax"],
  ["hard_single_max", "hardSingleMax"],
  ["hard_daily_max", "hardDailyMax"],
  ["hard_weekly_max", "hardWeeklyMax"],
] as const;

type CapPayload = { scope: string; category: CapCategory; currency: string; limits: CapLimits; rev: number };
type CapWords = { amountInvalid: string; countInvalid: string; hardBelowAuto: string };

type CapAmounts = Record<(typeof amountFields)[number][0], string>;

function readAmounts(values: Record<string, string>, invalid: string): CapAmounts | Problem {
  const out = {} as CapAmounts;
  for (const [name] of amountFields) {
    const parsed = parseMoney(values[name] ?? "", 18);
    if (parsed === null) {
      return { problem: invalid };
    }
    out[name] = parsed;
  }
  return out;
}

const hardPairs: readonly [keyof CapAmounts, keyof CapAmounts][] = [
  ["hard_single_max", "single_max"],
  ["hard_daily_max", "daily_max"],
  ["hard_weekly_max", "weekly_max"],
];

function readCapLimits(values: Record<string, string>, words: CapWords): CapLimits | Problem {
  const amounts = readAmounts(values, words.amountInvalid);
  if (isProblem(amounts)) {
    return amounts;
  }
  const rawCount = (values.daily_count_max ?? "").trim();
  const count = Number(rawCount);
  if (!/^\d+$/.test(rawCount) || count < 0 || count > 1_000_000) {
    return { problem: words.countInvalid };
  }
  if (hardPairs.some(([hard, auto]) => BigInt(amounts[hard]) < BigInt(amounts[auto]))) {
    return { problem: words.hardBelowAuto };
  }
  return { ...amounts, daily_count_max: count };
}

function readCapPolicy(
  values: Record<string, string>,
  existing: CapPolicy | undefined,
  words: CapWords,
): CapPayload | Problem {
  const limits = readCapLimits(values, words);
  if (isProblem(limits)) {
    return limits;
  }
  return {
    scope: existing?.scope ?? (values.scope || "platform"),
    category: (existing?.category ?? (values.category as CapCategory)) || "money.top_up",
    currency: existing?.currency ?? (values.currency || "USD"),
    limits,
    rev: existing?.rev ?? 0,
  };
}

export function CapPolicyDialog({
  policy,
  siteId,
  currency,
  onClose,
}: {
  policy?: CapPolicy;
  siteId: AdminSchemas["SiteId"];
  currency: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [read, reload] = useSnapshot(policy);
  const current = read;
  const scopeLabel = (scope: string) => (scope === "platform" ? t("operations.limits.cap.platform") : scope);
  const words: CapWords = {
    amountInvalid: t("operations.limits.cap.amountInvalid"),
    countInvalid: t("operations.limits.cap.countInvalid"),
    hardBelowAuto: t("operations.limits.cap.hardBelowAuto"),
  };
  return (
    <ChangeDialog
      operation={current ? `ops-cap:${current.scope}:${current.category}` : "ops-cap:new"}
      title={
        current
          ? t("operations.limits.cap.editTitle", {
              scope: scopeLabel(current.scope),
              category: t(`operations.limits.cap.category.${current.category}`),
            })
          : t("operations.limits.cap.createTitle")
      }
      intro={<p>{t("operations.limits.cap.dialogIntro")}</p>}
      action={t("operations.limits.cap.saveAction")}
      reload={policy ? reload : undefined}
      fields={(error) => <CapFields policy={current} siteId={siteId} defaultCurrency={currency} error={error} />}
      read={(values) => readCapPolicy(values, current, words)}
      send={(payload, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/operations/cap-policies/{scope}/{category}", {
            params: {
              path: { scope: payload.scope, category: payload.category },
              header: { "Idempotency-Key": key },
            },
            body: { currency: payload.currency, limits: payload.limits, rev: payload.rev, reason },
          }),
        )
      }
      done={(saved) => (
        <Text>
          {t("operations.limits.cap.saved", {
            scope: scopeLabel(saved.scope),
            category: t(`operations.limits.cap.category.${saved.category}`),
            rev: saved.rev,
          })}
        </Text>
      )}
      onClose={onClose}
    />
  );
}

function CapFields({
  policy,
  siteId,
  defaultCurrency,
  error,
}: {
  policy?: CapPolicy;
  siteId: AdminSchemas["SiteId"];
  defaultCurrency: string;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [currency, setCurrency] = useState(policy?.currency ?? defaultCurrency);
  const shownCurrency = policy?.currency ?? currency;
  return (
    <>
      {policy ? null : (
        <>
          <Select
            label={t("operations.limits.cap.scope")}
            name="scope"
            defaultValue="platform"
            options={[
              { value: "platform", label: t("operations.limits.cap.scopePlatform") },
              { value: siteId, label: t("operations.limits.cap.scopeSite", { siteId }) },
            ]}
          />
          <Select
            label={t("operations.limits.cap.categoryLabel")}
            name="category"
            defaultValue="money.top_up"
            options={categories.map((cat) => ({ value: cat, label: t(`operations.limits.cap.category.${cat}`) }))}
          />
          <Select
            label={t("operations.limits.cap.currency")}
            name="currency"
            value={currency}
            onChange={(event) => setCurrency(event.target.value)}
            options={paymentCurrencies.map((code) => ({ value: code, label: code }))}
          />
        </>
      )}
      {amountFields.map(([name, key]) => (
        <Field
          key={name}
          label={t(`operations.limits.cap.${key}`, { currency: shownCurrency })}
          name={name}
          defaultValue={policy ? moneyInput(policy.limits[name]) : "0"}
          inputMode="decimal"
          autoComplete="off"
          error={errors.field(error, `limits/${name}`)}
        />
      ))}
      <Field
        label={t("operations.limits.cap.dailyCountMax")}
        name="daily_count_max"
        defaultValue={policy ? String(policy.limits.daily_count_max) : "10"}
        inputMode="numeric"
        autoComplete="off"
        error={errors.field(error, "limits/daily_count_max")}
      />
    </>
  );
}

export function RepairCapDialog({ policy, onClose }: { policy: CapPolicy; onClose: () => void }) {
  const { t } = useTranslation();
  const scope = policy.scope === "platform" ? t("operations.limits.cap.platform") : policy.scope;
  const category = t(`operations.limits.cap.category.${policy.category}`);
  return (
    <ChangeDialog
      operation={`ops-cap-repair:${policy.scope}:${policy.category}`}
      title={t("operations.limits.cap.repairTitle", { scope, category })}
      intro={<p>{t("operations.limits.cap.repairIntro")}</p>}
      action={t("operations.limits.cap.repairAction")}
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/operations/cap-policies/{scope}/{category}/repair", {
            params: { path: { scope: policy.scope, category: policy.category }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={() => <Text>{t("operations.limits.cap.repaired", { scope, category })}</Text>}
      onClose={onClose}
    />
  );
}
