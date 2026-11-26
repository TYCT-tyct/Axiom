import { Delta, MetricStrip, Sparkline, type StripItem, useFormat } from "@axiom/ui";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { change, failureRate, margin, type Overview, shift, tokenTotal, trendOf, units } from "./dashboard";
import { callsDrill, type OverviewRange, type Window } from "./overview";

type Day = NonNullable<Overview["days"]>[number];

// The dashboard's eight figures (adk8): each says what it is, how it moved against the period before it, draws its days
// as a trend line and opens the page where the number is made. A figure the API does not give is "unavailable", never a
// guess; a comparison there is nothing to compare with is left out.
export function Figures({
  range,
  window,
  current,
  previous,
  trend,
}: {
  range: OverviewRange;
  window: Window;
  current: Overview;
  previous?: Overview;
  trend?: Overview;
}) {
  const { t } = useTranslation("dashboard");
  const items = [
    ...useMoneyCells(range, current, previous, trend),
    ...useTrafficCells(range, window, current, previous, trend),
    ...useReachCells(range, current, previous, trend),
  ];
  return <MetricStrip label={t("figures")} columns={4} items={items} />;
}

// compare is the note under a figure: what it is compared with, and how far it moved, in the colour of what that means for
// the business (a tone), or none where up is neither good nor bad.
function useCompare(range: OverviewRange) {
  const { t } = useTranslation("dashboard");
  return (moved: number | undefined, unit: string, tone?: "good" | "bad") =>
    moved === undefined ? null : (
      <>
        {t(`compare.${range}`)}
        <Delta value={moved} unit={unit} tone={tone} />
      </>
    );
}

const better = (moved: number | undefined, goodWhenUp: boolean) =>
  moved === undefined || moved === 0 ? undefined : moved > 0 === goodWhenUp ? "good" : "bad";

// What every cell needs: the words and formats, the comparison under the figure, the trend line beside it, and what a
// figure the API does not give says.
function useTools(range: OverviewRange, trend: Overview | undefined) {
  const { t } = useTranslation("dashboard");
  return { t, format: useFormat(), compare: useCompare(range), spark: useTrend(trend), none: t("unavailable") };
}

function useTrend(trend: Overview | undefined) {
  return (pick: (day: Day) => number | undefined, tone?: "bad") => {
    const values = trendOf(trend?.days, pick);
    return values.length > 0 ? <Sparkline values={values} tone={tone} /> : undefined;
  };
}

function useMoneyCells(
  range: OverviewRange,
  current: Overview,
  previous: Overview | undefined,
  trend: Overview | undefined,
): StripItem[] {
  const { t, format, compare, spark, none } = useTools(range, trend);
  const moved = change(units(current.revenue), units(previous?.revenue));
  const spent = change(units(current.cost), units(previous?.cost));
  const kept = margin(current);
  const rate = current.gross_margin === undefined ? undefined : current.gross_margin / 100;
  return [
    {
      key: "revenue",
      label: t("revenue"),
      value: current.revenue ? format.rounded(current.revenue) : none,
      note: compare(moved, "%", better(moved, true)),
      trend: spark((day) => units(day.revenue)),
      link: <Link to="/orders" />,
    },
    {
      key: "cost",
      label: t("cost"),
      value: current.cost ? format.rounded(current.cost) : none,
      note: compare(spent, "%"),
      trend: spark((day) => units(day.cost)),
      link: <Link to="/suppliers" search={{}} />,
    },
    {
      key: "margin",
      label: t("margin"),
      value: kept ? format.rounded(kept) : none,
      note:
        rate !== undefined
          ? t("marginRate", { rate: rate.toFixed(1) })
          : current.unknown_cost_calls
            ? t("marginUnknown", { calls: format.number(current.unknown_cost_calls) })
            : undefined,
      trend: spark((day) => (day.revenue && day.cost ? (units(day.revenue) ?? 0) - (units(day.cost) ?? 0) : undefined)),
      link: <Link to="/pricing" />,
    },
  ];
}

function useTrafficCells(
  range: OverviewRange,
  window: Window,
  current: Overview,
  previous: Overview | undefined,
  trend: Overview | undefined,
): StripItem[] {
  const { t, format, compare, spark, none } = useTools(range, trend);
  const called = change(current.calls, previous?.calls);
  const total = tokenTotal(current.tokens);
  const read = current.tokens && current.tokens.input + current.tokens.cache_read + current.tokens.cache_write;
  const failing = failureRate(current);
  const failed = shift(failing, failureRate(previous), 2);
  const drill = callsDrill(window, { site: current.site_id });
  return [
    {
      key: "calls",
      label: t("calls"),
      value: current.calls === undefined ? none : format.number(current.calls),
      note: compare(called, "%", better(called, true)),
      trend: spark((day) => day.calls),
      link: <Link to="/calls" search={drill} />,
    },
    {
      key: "tokens",
      label: t("tokens"),
      value: total === undefined ? none : format.compact(total),
      note:
        current.tokens && read !== undefined
          ? t("tokenSplit", { input: format.compact(read), output: format.compact(current.tokens.output) })
          : undefined,
      trend: spark((day) => tokenTotal(day.tokens)),
      link: <Link to="/calls" search={drill} />,
    },
    {
      key: "failure",
      label: t("failure"),
      value: failing === undefined ? none : failing.toFixed(2),
      suffix: failing === undefined ? undefined : "%",
      note: compare(failed, t("points"), better(failed, false)),
      trend: spark((day) => (day.calls ? (day.errors / day.calls) * 100 : undefined), "bad"),
      link: <Link to="/calls" search={{ ...drill, status: "error" }} />,
    },
  ];
}

const seconds = (ms: number | undefined) => (ms === undefined ? undefined : ms / 1000);

function useReachCells(
  range: OverviewRange,
  current: Overview,
  previous: Overview | undefined,
  trend: Overview | undefined,
): StripItem[] {
  const { t, format, compare, spark, none } = useTools(range, trend);
  const first = seconds(current.first_token_ms_p50);
  const slower = shift(first, seconds(previous?.first_token_ms_p50), 2);
  return [
    {
      key: "active",
      label: t("active"),
      value: current.active_accounts === undefined ? none : format.number(current.active_accounts),
      note: current.new_users === undefined ? undefined : t("newUsers", { count: current.new_users }),
      trend: spark((day) => day.active_accounts),
      link: <Link to="/users" search={{ q: undefined }} />,
    },
    {
      key: "firstToken",
      label: t("firstToken"),
      value: first === undefined ? none : first.toFixed(2),
      suffix: first === undefined ? undefined : t("seconds"),
      note: compare(slower, t("secondsUnit"), better(slower, false)),
      trend: spark((day) => day.first_token_ms_p50),
      link: <Link to="/monitor" search={{ window: "1h" }} />,
    },
  ];
}
