import {
  Avatar,
  BarChart,
  Card,
  PaceBar,
  RingChart,
  type RingPart,
  RowList,
  SegmentedControl,
  Text,
  useFormat,
} from "@axiom/ui";
import { useQueries } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { type MixKind, margin, mixOf, type Overview, tokenParts, units } from "./dashboard";
import type { OverviewRange } from "./overview";
import { accountQuery } from "./queries";

const utcDay = (date: string) => new Date(`${date}T00:00:00Z`);

// RevenueCard stacks each day's upstream cost under its gross margin (the board's 收入与成本); the last day, today,
// is drawn a step deeper. A day's margin is its revenue less its cost.
export function RevenueCard({ trend }: { trend: Overview }) {
  const { t } = useTranslation("dashboard");
  const days = trend.days ?? [];
  const currency = trend.revenue?.currency ?? trend.cost?.currency;
  return (
    <Card title={t("chart.title")}>
      {currency === undefined || days.length === 0 ? (
        <Text tone="faint">{t("chart.empty")}</Text>
      ) : (
        <BarChart
          label={t("chart.title")}
          empty={t("chart.empty")}
          series={[
            { key: "cost", label: t("cost"), colour: "muted" },
            { key: "margin", label: t("margin"), colour: "data" },
          ]}
          points={days.map((day) => ({
            at: utcDay(day.date),
            values: {
              cost: day.cost,
              margin: margin({ revenue: day.revenue, cost: day.cost }) ?? { amount: "0", currency },
            },
          }))}
          unit={{ currency }}
          step="day"
          timeZone="UTC"
          current={days.length - 1}
          zero
        />
      )}
    </Card>
  );
}

// TokensCard stacks the day's output, input and cache hits on one bar (the board's Token 用量).
export function TokensCard({ trend }: { trend: Overview }) {
  const { t } = useTranslation("dashboard");
  const days = (trend.days ?? []).filter((day) => day.tokens);
  return (
    <Card title={t("tokens")}>
      {days.length === 0 ? (
        <Text tone="faint">{t("chart.empty")}</Text>
      ) : (
        <BarChart
          label={t("tokens")}
          empty={t("chart.empty")}
          series={[
            { key: "output", label: t("tokenOutput"), colour: "data3" },
            { key: "input", label: t("tokenInput"), colour: "data" },
            { key: "hit", label: t("tokenHit"), colour: "data2" },
          ]}
          points={days.map((day) => ({ at: utcDay(day.date), values: day.tokens ? tokenParts(day.tokens) : {} }))}
          unit="count"
          step="day"
          timeZone="UTC"
          current={days.length - 1}
          zero
        />
      )}
    </Card>
  );
}

const models = ["c0", "c1", "c2", "c3", "c4"] as const;
const kinds: readonly MixKind[] = ["revenue", "calls", "tokens"];

// MixCard is how the window's revenue, calls or tokens divide among the models (the board's 模型分布): a ring, and a row
// per model that opens the model. What the busiest models leave over is one grey part.
export function MixCard({ overview }: { overview: Overview }) {
  const { t } = useTranslation("dashboard");
  const format = useFormat();
  const navigate = useNavigate();
  const [kind, setKind] = useState<MixKind>("revenue");
  const mix = mixOf(overview, kind);
  const total = mix.reduce((sum, part) => sum + part.value, 0);
  const parts: RingPart[] = mix.map((part, at) => ({
    key: part.model ?? "",
    label: part.model ?? t("mix.others"),
    value: part.value,
    share: `${((part.value / total) * 100).toFixed(1)}%`,
    colour: part.model === undefined ? "muted" : (models[at % models.length] ?? "c0"),
    mono: part.model !== undefined,
  }));
  return (
    <Card
      title={t("mix.title")}
      actions={
        <SegmentedControl
          label={t("mix.by")}
          value={kind}
          options={kinds.map((value) => ({ value, label: t(`mix.${value}`) }))}
          onChange={setKind}
        />
      }
    >
      {parts.length === 0 ? (
        <Text tone="faint">{t("chart.empty")}</Text>
      ) : (
        <RingChart
          label={t("mix.title")}
          parts={parts}
          centre={format.number(mix.filter((part) => part.model !== undefined).length)}
          centreLabel={t("mix.models")}
          size={112}
          onSelect={(model) => model && navigate({ to: "/models", search: { q: String(model) } })}
        />
      )}
    </Card>
  );
}

// SpendersCard ranks the accounts whose settled calls were charged most in the window (the board's 账户消费榜), each as
// long a bar as its share of the top account's, and a link to the account.
export function SpendersCard({ overview, range }: { overview: Overview; range: OverviewRange }) {
  const { t } = useTranslation("dashboard");
  const format = useFormat();
  const spenders = (overview.top_spenders ?? []).slice(0, 5);
  const accounts = useQueries({ queries: spenders.map((spender) => accountQuery(spender.account_id)) });
  const top = Math.max(1e-9, ...spenders.map((spender) => units(spender.spend) ?? 0));
  return (
    <Card layout="flush" title={t("spenders.title")} lede={t(`rangeName.${range}`)}>
      <RowList
        label={t("spenders.title")}
        empty={t("spenders.none")}
        items={spenders.map((spender, at) => {
          const name = accounts[at]?.data?.name ?? spender.account_id.slice(0, 8);
          return {
            key: spender.account_id,
            lead: <Avatar name={name} size="sm" />,
            title: name,
            sub: (
              <PaceBar label={t("spenders.share", { name })} value={units(spender.spend) ?? 0} max={top} width={160} />
            ),
            end: format.rounded(spender.spend),
            link: <Link to="/accounts/$accountId" params={{ accountId: spender.account_id }} />,
          };
        })}
      />
    </Card>
  );
}
