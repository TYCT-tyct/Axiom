import type { Schemas } from "@axiom/api";
import type DesktopOverview from "@axiom/boards/AxApp/regions/Overview";
import type { MonthWindow } from "./overview-data";
import { aggregateGroups, formatBigDeltaPercent, type GroupAggregate, seriesColor, toUsageTotals } from "./usage-model";

// The overview's daily spend chart and the spend's comparison with last month: what they ask the usage rollups for, and
// how the answers become the board's bars and sentence.

type BarRow = Parameters<typeof DesktopOverview>[0]["rows"]["bars0"][number];
type Statistics = Schemas["UsageStatistics"];

const HOUR_MS = 3_600_000;

type Window = { since: string; until: string };
const iso = (ms: number) => new Date(ms).toISOString();

// statisticsWindows are the windows the overview asks for, in the whole UTC hours the rollups take. The month runs to
// the end of the current hour, for the calls' totals and the chart alike, so both count every call so far and a refetch
// within the hour covers the newest. The comparison puts both months on that same span: this month to the end of the
// current hour, and as many hours from the start of last month, both cut to last month's length when it is the
// shorter. The card's month spend counts every call so far, so this month's side can equal it (spendChange).
export function statisticsWindows(period: MonthWindow): {
  month: Window;
  comparison: { month: Window; lastMonth: Window };
  lastMonthNumber: number;
} {
  const sinceMs = Date.parse(period.since);
  const untilMs = (Math.floor(Date.parse(period.until) / HOUR_MS) + 1) * HOUR_MS;
  const start = new Date(sinceMs);
  const lastStart = Date.UTC(start.getUTCFullYear(), start.getUTCMonth() - 1, 1);
  const span = Math.min(untilMs - sinceMs, sinceMs - lastStart);
  return {
    month: { since: period.since, until: iso(untilMs) },
    comparison: {
      month: { since: period.since, until: iso(sinceMs + span) },
      lastMonth: { since: iso(lastStart), until: iso(lastStart + span) },
    },
    lastMonthNumber: new Date(lastStart).getUTCMonth() + 1,
  };
}

type Segment = { amount: bigint; fill: string };

const chartHeight = 190;
// The board's geometry: the month's largest day reaches 1/1.15 of the chart, and each segment is drawn 1.2 units short
// of its share, which leaves the gap between two models and above the axis.
const chartHeadroom = 1.15;
const segmentGap = 1.2;

// barRows draws each day as a stack of segments from the bottom, in the order given. A segment too small to clear the
// gap is still drawn one unit high, so a day that spent anything shows.
export function barRows(stacks: Map<number, Segment[]> | undefined, days: number): BarRow[] {
  const totals = [...(stacks?.values() ?? [])].map((day) => day.reduce((sum, segment) => sum + segment.amount, 0n));
  const max = totals.reduce((a, b) => (b > a ? b : a), 0n);
  if (!stacks || max === 0n) {
    return [];
  }
  const scale = chartHeight / chartHeadroom / Number(max);
  const slot = 1000 / days;
  const width = slot * 0.6;
  const rows: BarRow[] = [];
  for (const [day, segments] of [...stacks.entries()].sort(([a], [b]) => a - b)) {
    let bottom = chartHeight;
    for (const [index, segment] of segments.entries()) {
      if (segment.amount <= 0n) {
        continue;
      }
      const share = Number(segment.amount) * scale;
      bottom -= share;
      rows.push({
        key: `${day}-${index}`,
        kind: "0" as const,
        f0: (slot * (day - 1) + (slot - width) / 2).toFixed(1),
        f1: bottom.toFixed(1),
        f2: width.toFixed(1),
        f3: Math.max(share - segmentGap, 1).toFixed(1),
        f4: "2",
        f5: `fill: ${segment.fill};`,
      });
    }
  }
  return rows;
}

// dayStacks is each day as one segment, for when the spend by model is not to be had: the day's total from the calls.
export function dayStacks(daily: Map<number, bigint> | undefined): Map<number, Segment[]> | undefined {
  return daily && new Map([...daily].map(([day, amount]) => [day, [{ amount, fill: seriesColor(0) }]]));
}

// modelStacks splits each day by model, in the model card's order and colours (order: a model's rank on the card), and
// a model the card does not rank after them by its spend, so a segment and its row in the card are the same colour.
export function modelStacks(
  buckets: Schemas["UsageBucket"][],
  currency: string,
  order: ReadonlyMap<string, number>,
): Map<number, Segment[]> {
  const groups = aggregateGroups(buckets, currency, "day");
  const rank = new Map(order);
  const bySpend = (a: GroupAggregate, b: GroupAggregate) =>
    b.totals.spendNano > a.totals.spendNano ? 1 : b.totals.spendNano < a.totals.spendNano ? -1 : 0;
  for (const group of groups.filter((one) => !rank.has(one.id)).sort(bySpend)) {
    rank.set(group.id, rank.size);
  }
  const at = (group: GroupAggregate) => rank.get(group.id) ?? 0;
  const stacks = new Map<number, Segment[]>();
  for (const group of groups.sort((a, b) => at(a) - at(b))) {
    const fill = seriesColor(at(group));
    for (const [slotMs, totals] of group.bySlot) {
      const day = new Date(slotMs).getUTCDate();
      const segments = stacks.get(day) ?? [];
      segments.push({ amount: totals.spendNano, fill });
      stacks.set(day, segments);
    }
  }
  return stacks;
}

// chartStacks picks the one dataset the chart draws, its axis and its empty state come from: the calls' daily totals.
// The rollups' split by model replaces their one colour only while it accounts for every day's total exactly. Ingestion
// lags the calls by up to a minute and backfill_complete speaks for history only, so a split that disagrees with a
// day's total, an unfinished backfill, or no split at all draws each day as its total.
export function chartStacks(
  daily: Map<number, bigint> | undefined,
  statistics: Statistics | undefined,
  currency: string | undefined,
  order: ReadonlyMap<string, number>,
): Map<number, Segment[]> | undefined {
  if (!daily || !statistics || !currency || statistics.backfill_complete === false) {
    return dayStacks(daily);
  }
  const split = modelStacks(statistics.buckets ?? [], currency, order);
  const sum = (day: number) => (split.get(day) ?? []).reduce((total, segment) => total + segment.amount, 0n);
  const agree = [...new Set([...daily.keys(), ...split.keys()])].every((day) => sum(day) === (daily.get(day) ?? 0n));
  return agree ? split : dayStacks(daily);
}

// spendChange compares the month's spend with last month's over the same span (statisticsWindows), both sides from the
// rollups, so they are counted alike. It is given only when this month's side is the card's figure (spent, what calls
// settled this month): the rollups lag the calls and a month longer than the last is cut, and a change measured on
// another amount than the one it stands under would contradict it. Not while either side is missing or its history is
// still backfilling, and not when last month spent nothing, where a percentage has no meaning.
export function spendChange(
  month: Statistics | undefined,
  lastMonth: Statistics | undefined,
  spent: Schemas["Money"] | undefined,
): string | undefined {
  if (!month || !lastMonth || !spent) {
    return undefined;
  }
  if (month.backfill_complete === false || lastMonth.backfill_complete === false) {
    return undefined;
  }
  const now = toUsageTotals(month.totals, spent.currency, true);
  const before = toUsageTotals(lastMonth.totals, spent.currency, true);
  if (!now || !before || now.spendNano !== BigInt(spent.amount) || before.spendNano === 0n) {
    return undefined;
  }
  return formatBigDeltaPercent(now.spendNano, before.spendNano, "");
}

export function chartLabels(days: number, month: number, note: string): { text: string; left: string }[] {
  const marks = [1, 8, 15, 22, days];
  if (note) {
    return [{ text: note, left: "left: 1.61%;" }, ...marks.slice(1).map(() => ({ text: "", left: "" }))];
  }
  return marks.map((day) => ({ text: `${month}/${day}`, left: `left: ${(((day - 0.5) / days) * 100).toFixed(2)}%;` }));
}

export function chartOf(daily: Map<number, bigint> | undefined, note: string, window: MonthWindow) {
  const hasSpend = daily !== undefined && [...daily.values()].some((amount) => amount > 0n);
  const labels = chartLabels(window.days, window.monthNumber, hasSpend ? "" : note);
  const at = (index: number) => labels[index] ?? { text: "", left: "" };
  return {
    chartDate0: at(0).text,
    chartDateLeft0: at(0).left,
    chartDate1: at(1).text,
    chartDateLeft1: at(1).left,
    chartDate2: at(2).text,
    chartDateLeft2: at(2).left,
    chartDate3: at(3).text,
    chartDateLeft3: at(3).left,
    chartDate4: at(4).text,
    chartDateLeft4: at(4).left,
  };
}

// spendChart draws the chart from one dataset (chartStacks) and gives the comparison under the card's spend.
export function spendChart(
  daily: Map<number, bigint> | undefined,
  statistics: { byModel?: Statistics; month?: Statistics; lastMonth?: Statistics },
  spent: Schemas["Money"] | undefined,
  currency: string | undefined,
  ranked: Schemas["CallAggregate"][] | undefined,
  days: number,
) {
  const order = new Map((ranked ?? []).map((row, index) => [row.bucket ?? "", index]));
  const stacks = chartStacks(daily, statistics.byModel, currency, order);
  return { bars: barRows(stacks, days), change: spendChange(statistics.month, statistics.lastMonth, spent) };
}
