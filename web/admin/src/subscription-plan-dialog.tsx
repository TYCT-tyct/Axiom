import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { CheckField, Field, moneyInput, parseMoney, Text, useFormat } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem, useSnapshot } from "./change";

type Plan = AdminSchemas["SubscriptionPlan"];
type PlanPayload = Omit<AdminSchemas["SubscriptionPlanInput"], "reason">;

const minWindow = 3600;
const maxWindows = 400;

function parseWhole(text: string): number | null {
  const trimmed = text.trim();
  if (!/^\d+$/.test(trimmed)) {
    return null;
  }
  const value = Number(trimmed);
  return Number.isSafeInteger(value) ? value : null;
}

type PlanWords = {
  nameInvalid: string;
  priceInvalid: string;
  quotaInvalid: string;
  windowInvalid: string;
  termInvalid: string;
};

function readDurations(
  values: Record<string, string>,
  words: Pick<PlanWords, "windowInvalid" | "termInvalid">,
): { window_seconds: number; term_seconds: number } | Problem {
  const window_seconds = parseWhole(values.window_seconds ?? "");
  if (window_seconds === null || window_seconds < minWindow) {
    return { problem: words.windowInvalid };
  }
  const term_seconds = parseWhole(values.term_seconds ?? "");
  const invalidTerm =
    term_seconds === null || term_seconds < window_seconds || Math.ceil(term_seconds / window_seconds) > maxWindows;
  return invalidTerm ? { problem: words.termInvalid } : { window_seconds, term_seconds };
}

function readPlan(values: Record<string, string>, currency: string, words: PlanWords): PlanPayload | Problem {
  const name = (values.name ?? "").trim();
  if (!name || name.length > 200) {
    return { problem: words.nameInvalid };
  }
  const price = parseMoney(values.price ?? "");
  if (price === null) {
    return { problem: words.priceInvalid };
  }
  const quota = parseMoney(values.quota ?? "");
  if (quota === null || /^0+$/.test(quota)) {
    return { problem: words.quotaInvalid };
  }
  const durations = readDurations(values, words);
  if ("problem" in durations) {
    return durations;
  }
  return { name, price: { amount: price, currency }, quota, ...durations, active: values.active === "on" };
}

// Create or edit one subscription plan. Editing snapshots the plan as read and sends its revision so a concurrent save
// prompts a reload instead of overwriting.
export function SubscriptionPlanDialog({
  plan,
  currency,
  onClose,
}: {
  plan?: Plan;
  currency: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const [read, reload] = useSnapshot(plan);
  const current = read;
  const activeCurrency = current?.price.currency ?? currency;
  const words: PlanWords = {
    nameInvalid: t("subscriptions.plan.nameInvalid"),
    priceInvalid: t("subscriptions.plan.priceInvalid"),
    quotaInvalid: t("subscriptions.plan.quotaInvalid"),
    windowInvalid: t("subscriptions.plan.windowInvalid"),
    termInvalid: t("subscriptions.plan.termInvalid"),
  };
  return (
    <ChangeDialog
      operation={current ? `subscription-plan:${current.id}` : "subscription-plan:new"}
      title={current ? t("subscriptions.plan.editTitle", { name: current.name }) : t("subscriptions.plan.createTitle")}
      intro={<p>{t(current ? "subscriptions.plan.editIntro" : "subscriptions.plan.createIntro")}</p>}
      action={t(current ? "subscriptions.plan.editAction" : "subscriptions.plan.createAction")}
      reload={plan ? reload : undefined}
      fields={(error) => <PlanFields plan={current} currency={activeCurrency} error={error} />}
      read={(values) => readPlan(values, activeCurrency, words)}
      send={(payload, { reason, key }) =>
        unwrapChange(
          current
            ? adminApi.PUT("/api/admin/v1/subscription-plans/{plan_id}", {
                params: { path: { plan_id: current.id }, header: { "Idempotency-Key": key } },
                body: { ...payload, rev: current.rev, reason },
              })
            : adminApi.POST("/api/admin/v1/subscription-plans", {
                params: { header: { "Idempotency-Key": key } },
                body: { ...payload, reason },
              }),
        )
      }
      done={(saved) => (
        <Text>
          {current
            ? t("subscriptions.plan.updated", { name: saved.name, rev: saved.rev })
            : t("subscriptions.plan.created", {
                name: saved.name,
                price: format.money(saved.price),
                quota: format.money({ amount: saved.quota, currency: saved.price.currency }),
              })}
        </Text>
      )}
      onClose={onClose}
    />
  );
}

function PlanFields({ plan, currency, error }: { plan?: Plan; currency: string; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Field
        label={t("subscriptions.plan.name")}
        hint={t("subscriptions.plan.nameHint")}
        name="name"
        defaultValue={plan?.name ?? ""}
        maxLength={200}
        autoComplete="off"
        error={errors.field(error, "name")}
      />
      <Field
        label={t("subscriptions.plan.price", { currency })}
        hint={t("subscriptions.plan.priceHint")}
        name="price"
        defaultValue={plan ? moneyInput(plan.price.amount) : ""}
        inputMode="decimal"
        autoComplete="off"
        error={errors.field(error, "price") ?? errors.field(error, "price/amount")}
      />
      <Field
        label={t("subscriptions.plan.quota", { currency })}
        hint={t("subscriptions.plan.quotaHint")}
        name="quota"
        defaultValue={plan ? moneyInput(plan.quota) : ""}
        inputMode="decimal"
        autoComplete="off"
        error={errors.field(error, "quota")}
      />
      <Field
        label={t("subscriptions.plan.windowSeconds")}
        hint={t("subscriptions.plan.windowHint")}
        name="window_seconds"
        defaultValue={plan ? String(plan.window_seconds) : "86400"}
        inputMode="numeric"
        autoComplete="off"
        error={errors.field(error, "window_seconds")}
      />
      <Field
        label={t("subscriptions.plan.termSeconds")}
        hint={t("subscriptions.plan.termHint")}
        name="term_seconds"
        defaultValue={plan ? String(plan.term_seconds) : "2592000"}
        inputMode="numeric"
        autoComplete="off"
        error={errors.field(error, "term_seconds")}
      />
      <CheckField
        name="active"
        label={t("subscriptions.plan.activeLabel")}
        defaultChecked={plan ? plan.active : true}
      />
    </>
  );
}
