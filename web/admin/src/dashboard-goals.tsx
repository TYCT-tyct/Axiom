import type { AdminSchemas } from "@axiom/api";
import { Card, Cluster, Grid, PaceBar, Stack, Text, TextLink, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { cloneElement, type ReactElement, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { type Overview, units } from "./dashboard";
import { notBuilt } from "./monitor-queries";
import { paidOrdersDrill, retentionOrdersDrill, type Window } from "./overview";
import { overviewQuery, targetsQuery } from "./queries";
import { formatTargetRate, targetMonth } from "./targets";
import { TargetsAction } from "./targets-view";

type Targets = AdminSchemas["OperationsTargets"];
type Format = ReturnType<typeof useFormat>;
// A goal the owner set: how far the month is (done) toward it (goal), both as the bar's numbers, and the words for each.
// paced goals are worked toward, so the bar carries a mark where an even spread over the month would have it today.
type Goal = {
  key: "revenue" | "paid" | "margin" | "retention";
  done: number;
  goal: number;
  value: string;
  target: string;
  paced: boolean;
  // Where the figure is made, when there is a list behind it: the orders that paid, for the paying accounts and their
  // retention.
  link?: ReactElement<{ children?: ReactNode }>;
};

// The share of the UTC month gone by `at`: where a target spread evenly over the days should stand today.
function paceOf(at: Date) {
  const from = Date.UTC(at.getUTCFullYear(), at.getUTCMonth(), 1);
  const to = Date.UTC(at.getUTCFullYear(), at.getUTCMonth() + 1, 1);
  return (at.getTime() - from) / (to - from);
}

// The goals that have a target and a figure to measure it with.
function goalsOf(overview: Overview, set: Targets, format: Format, window: Window): Goal[] {
  const goals: Goal[] = [];
  if (set.revenue && overview.revenue) {
    goals.push({
      key: "revenue",
      done: units(overview.revenue) ?? 0,
      goal: units(set.revenue) ?? 0,
      value: format.rounded(overview.revenue),
      target: format.rounded(set.revenue),
      paced: true,
    });
  }
  if (set.paid_users !== undefined && overview.paid_users !== undefined) {
    goals.push({
      key: "paid",
      done: overview.paid_users,
      goal: set.paid_users,
      value: format.number(overview.paid_users),
      target: format.number(set.paid_users),
      paced: true,
      link: <Link to="/orders" search={paidOrdersDrill(window)} />,
    });
  }
  if (set.gross_margin !== undefined && overview.gross_margin !== undefined) {
    goals.push({
      key: "margin",
      done: overview.gross_margin,
      goal: set.gross_margin,
      value: formatTargetRate(overview.gross_margin),
      target: formatTargetRate(set.gross_margin),
      paced: false,
    });
  }
  const kept = overview.retention?.rate;
  if (set.retention !== undefined && kept !== undefined) {
    goals.push({
      key: "retention",
      done: kept,
      goal: set.retention,
      value: formatTargetRate(kept),
      target: formatTargetRate(set.retention),
      paced: false,
      link: <Link to="/orders" search={retentionOrdersDrill(window)} />,
    });
  }
  return goals;
}

function GoalBars({ goals, pace }: { goals: Goal[]; pace: number }) {
  const { t } = useTranslation("goals");
  if (goals.length === 0) {
    return <Text tone="faint">{t("none")}</Text>;
  }
  return (
    <Grid columns={4} gap={24}>
      {goals.map((goal) => (
        <Stack key={goal.key} gap={8}>
          <Cluster justify="between">
            <Text as="span" medium>
              {goal.link ? <TextLink>{cloneElement(goal.link, undefined, t(goal.key))}</TextLink> : t(goal.key)}
            </Text>
            <Text as="span" numeric>
              {goal.value}
              <Text as="span" size="sm" tone="faint">
                {t("of", { target: goal.target })}
              </Text>
            </Text>
          </Cluster>
          <PaceBar
            label={t(goal.key)}
            value={goal.done}
            max={goal.goal}
            warn={false}
            expected={goal.paced ? goal.goal * pace : undefined}
            expectedLabel={goal.paced ? t("pace") : undefined}
          />
        </Stack>
      ))}
    </Grid>
  );
}

// GoalsCard is the month's targets against the month so far (the board's 本月目标): a bar for each target the owner set.
// The month is the visit's UTC month, the calendar the targets are kept in; the editor is the one that was always here,
// for whichever month is picked. A node without targets leaves the card out.
export function GoalsCard({ anchor }: { anchor: Date }) {
  const { t } = useTranslation("goals");
  const format = useFormat();
  const month = targetMonth(anchor);
  const window = { since: `${month}-01T00:00:00.000Z`, until: anchor.toISOString() };
  const so = useQuery(overviewQuery(window.since, window.until));
  const set = useQuery(targetsQuery(month));
  if (notBuilt(set.error)) {
    return null;
  }
  const currency = so.data?.revenue?.currency ?? set.data?.revenue?.currency ?? "";
  return (
    <Card title={t("title")} lede={t("lede", { month: format.monthLabel(month) })}>
      <Stack gap={16}>
        {currency ? <TargetsAction anchor={anchor} currency={currency} /> : null}
        {so.data && set.data ? (
          <GoalBars goals={goalsOf(so.data, set.data, format, window)} pace={paceOf(anchor)} />
        ) : (
          <Text tone="faint">{t(so.isError || set.isError ? "failed" : "loading")}</Text>
        )}
      </Stack>
    </Card>
  );
}
