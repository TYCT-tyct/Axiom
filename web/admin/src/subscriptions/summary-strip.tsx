import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { Delta, MetricStrip, type StripItem, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { subscriptionPlansQuery, subscriptionSummaryQuery } from "../subscription-queries";

type Summary = AdminSchemas["SubscriptionSummary"];

// How far this month's revenue is from last month's, in tenths of a percent, or nothing when last month sold nothing:
// both are exact integers, and the ratio is made of them before it is rounded.
export function revenueMove(revenue: string, previous: string): number | undefined {
  const before = BigInt(previous);
  if (before === 0n) return undefined;
  return Number(((BigInt(revenue) - before) * 1000n) / before) / 10;
}

// The four figures at the top of the subscriptions tab (the design's adStrip): the active subscriptions and which plans
// they are, what subscriptions sold for this month and last, and how many end within 7 days. Revenue is what the
// subscriptions were sold for, not what is left of it: a refund later does not take it back from the month it sold in,
// and the labels say so. Without sites.manage, or while the node has no summary yet (501), the strip is left out.
export function SummaryStrip() {
  const { t } = useTranslation();
  const format = useFormat();
  const allowed = can(useSession(), "sites.manage");
  const summary = useQuery({ ...subscriptionSummaryQuery, enabled: allowed });
  const plans = useQuery({ ...subscriptionPlansQuery(true), enabled: can(useSession(), "pricing.view") });
  if (!summary.data) return null;
  const data: Summary = summary.data;
  const names = new Map((plans.data?.data ?? []).map((plan) => [plan.id, plan.name]));
  const mix = data.plans
    .filter((plan) => plan.active > 0 && names.has(plan.plan_id))
    .map((plan) => `${names.get(plan.plan_id)} ${format.number(plan.active)}`)
    .join("，");
  const move = revenueMove(data.revenue.amount, data.previous_revenue.amount);
  const items: StripItem[] = [
    {
      key: "active",
      label: t("subscriptions.strip.active"),
      value: format.number(data.active),
      suffix: ` ${t("subscriptions.strip.unit")}`,
      note: mix || undefined,
    },
    {
      key: "revenue",
      label: t("subscriptions.strip.revenue"),
      value: format.money(data.revenue),
      note: (
        <>
          {t("subscriptions.strip.beforeRefunds")}
          {move === undefined ? null : (
            <>
              {" "}
              {t("subscriptions.strip.versus")} <Delta value={move} tone={move >= 0 ? "good" : "bad"} />
            </>
          )}
        </>
      ),
    },
    {
      key: "previous",
      label: t("subscriptions.strip.previous"),
      value: format.money(data.previous_revenue),
      note: t("subscriptions.strip.beforeRefunds"),
    },
    {
      key: "ending",
      label: t("subscriptions.strip.ending"),
      value: format.number(data.ending_within_7_days),
      suffix: ` ${t("subscriptions.strip.unit")}`,
      note: t("subscriptions.strip.noRenewal"),
    },
  ];
  return <MetricStrip label={t("subscriptions.strip.label")} items={items} />;
}
