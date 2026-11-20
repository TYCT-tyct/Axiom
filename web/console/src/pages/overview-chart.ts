import type { Schemas } from "@axiom/api";
import type DesktopOverview from "@axiom/boards/AxApp/regions/Overview";
import type { MonthWindow } from "./overview-data";
import { aggregateGroups, formatBigDeltaPercent, seriesColor, toUsageTotals } from "./usage-model";

// The overview's daily spend chart and the spend's comparison with last month: what they ask the usage rollups for, and
// how the answers become the board's bars and sentence.

type BarRow = Parameters<typeof DesktopOverview>[0]["rows"]["bars0"][number];
type Statistics = Schemas["UsageStatistics"];

const HOUR_MS = 3_600_000;

// statisticsWindows are the windows the rollups are asked for, in the whole UTC hours they take: this month up to the
// end of the current hour, and the same span from the start of last month, cut at this month's start when last month is
// the shorter one. The same span on both sides is what makes the comparison fair on the 24th as on the 31st.
export function statisticsWindows(period: MonthWindow) {
  const sinceMs = Date.parse(period.since);
  const untilMs = (Math.floor(Date.parse(period.until) / HOUR_MS) + 1) * HOUR_MS;
  const start = new Date(sinceMs);
  const lastStart = Date.UTC(start.getUTCFullYear(), start.getUTCMonth() - 1, 1);
  return {
    month: { since: period.since, until: new Date(untilMs).toISOString() },
    lastMonth: {
      since: new Date(lastStart).toISOString(),
      until: new Date(Math.min(lastStart + untilMs - sinceMs, sinceMs)).toISOString(),
    },
    lastMonthNumber: new Date(lastStart).getUTCMonth() + 1,
  };
}

export type Segment = { amount: bigint; fill: string };

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

// modelStacks splits each day by model, the models in the model card's order and colours (ranked), and a model the card
// does not rank after them by its spend, so a segment and its row in the card are the same colour.
export function modelStacks(
  buckets: Schemas["UsageBucket"][],
  currency: string,
  ranked: readonly string[],
): Map<number, Segment[]> {
  const groups = aggregateGroups(buckets, currency, "day");
  const unranked = groups
    .filter((group) => !ranked.includes(group.id))
    .sort((a, b) => (b.totals.spendNano > a.totals.spendNano ? 1 : b.totals.spendNano < a.totals.spendNano ? -1 : 0))
    .map((group) => group.id);
  const order = [...ranked, ...unranked];
  const stacks = new Map<number, Segment[]>();
  for (const group of [...groups].sort((a, b) => order.indexOf(a.id) - order.indexOf(b.id))) {
    const fill = seriesColor(order.indexOf(group.id));
    for (const [slotMs, totals] of group.bySlot) {
      const day = new Date(slotMs).getUTCDate();
      stacks.set(day, [...(stacks.get(day) ?? []), { amount: totals.spendNano, fill }]);
    }
  }
  return stacks;
}

// barStacks prefers the rollups' split by model; until they answer, when they fail, or while their history is still
// being backfilled, the chart shows the calls' daily totals in one colour.
export function barStacks(
  statistics: Statistics | undefined,
  daily: Map<number, bigint> | undefined,
  currency: string | undefined,
  ranked: readonly string[],
) {
  return statistics && currency && statistics.backfill_complete !== false
    ? modelStacks(statistics.buckets ?? [], currency, ranked)
    : dayStacks(daily);
}

// spendChange compares this month's spend with last month's over the same span (statisticsWindows), both from the
// rollups, so the two sides are counted alike. It is undefined until both answer, while their history is still being
// backfilled, and when last month spent nothing, where a percentage has no meaning.
export function spendChange(
  month: Statistics | undefined,
  lastMonth: Statistics | undefined,
  currency: string | undefined,
): string | undefined {
  if (!month || !lastMonth || !currency || month.backfill_complete === false || lastMonth.backfill_complete === false) {
    return undefined;
  }
  const now = toUsageTotals(month.totals, currency, true);
  const before = toUsageTotals(lastMonth.totals, currency, true);
  if (!now || !before || before.spendNano === 0n) {
    return undefined;
  }
  return formatBigDeltaPercent(now.spendNano, before.spendNano, "");
}
