import { DataState, Grid } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { Overview } from "./dashboard";
import { MixCard, RevenueCard, SpendersCard, TokensCard } from "./dashboard-charts";
import { GoalsCard } from "./dashboard-goals";
import { TodoCard } from "./dashboard-todo";
import { LinesCard } from "./lines-card";
import enGoals from "./locales/en/goals";
import enOps from "./locales/en/ops";
import zhGoals from "./locales/zh/goals";
import zhOps from "./locales/zh/ops";
import { monitorQuery, notBuilt } from "./monitor-queries";
import type { OverviewRange } from "./overview";
import { BalancesCard } from "./upstream-balances";

// The words of the cards, which the page loads with them (locales/load-page.ts).
export const namespaces = {
  goals: { zh: zhGoals, en: enGoals },
  ops: { zh: zhOps, en: enOps },
};

// The lines of the last hour, from the monitor: a node that has none leaves the card out.
function MonitorLines() {
  const query = useQuery(monitorQuery("1h"));
  if (notBuilt(query.error)) {
    return null;
  }
  return <DataState query={query}>{(monitor) => <LinesCard monitor={monitor} />}</DataState>;
}

// The cards under the dashboard's figures: the charts, what waits, the lines and balances, and the month's goals. They are
// a chunk of their own, loaded as the figures are drawn: the first screen is the head and the figures, which is what the
// first-screen budget (scripts/budget.ts) is for.
export default function Cards({
  overview,
  trend,
  range,
  anchor,
}: {
  overview: Overview;
  trend: Overview;
  range: OverviewRange;
  anchor: Date;
}) {
  return (
    <>
      <Grid columns="2:1">
        <RevenueCard trend={trend} />
        <TodoCard />
      </Grid>
      <Grid columns="1:1:1">
        <TokensCard trend={trend} />
        <MixCard overview={overview} />
        <SpendersCard overview={overview} range={range} />
      </Grid>
      <Grid columns="2:1">
        <MonitorLines />
        <BalancesCard />
      </Grid>
      <GoalsCard anchor={anchor} />
    </>
  );
}
