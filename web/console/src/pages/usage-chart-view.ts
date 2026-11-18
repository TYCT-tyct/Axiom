import type { Props as UsageProps } from "@axiom/boards/AxApp/regions/Usage";
import type { UsageGranularity, UsageGroupBy } from "../queries";
import {
  type BucketSlot,
  formatCompactTokens,
  formatDeltaPercent,
  formatSpendPerMillion,
  type GroupAggregate,
  metricValue,
  niceAxisScale,
  SERIES_COLORS,
  splitCurrency,
  type UsageMetricKey,
  type UsageTotals,
  xTickIndices,
} from "./usage-model";

export type UsageLanguage = "zh" | "en";

export type Formatters = {
  number: (value: number | bigint) => string;
  money: (value: { amount: string; currency: string }) => string;
  day: (value: Date) => string;
  dayFull: (value: Date) => string;
  hour: (value: Date) => string;
};

export type HoverTipData = {
  bucketLabel: string;
  totalFormatted: string;
  items: readonly { id: string; name: string; color: string; valueFormatted: string }[];
};

export type UsageViewLabels = {
  newLabel: string;
  unknownGroup: string;
  unitByGranularity: Record<UsageGranularity, string>;
  metricLabels: Record<UsageMetricKey, string>;
  groupByLabels: Record<UsageGroupBy, string>;
  groupColLabels: Record<UsageGroupBy, string>;
  granularityLabels: Record<UsageGranularity, string>;
  tableCols: { spend: string; share: string; requests: string; input: string; output: string; avg: string };
  tokenRows: readonly [string, string, string, string];
  ppDelta: (sign: string, value: string) => string;
  successOnly: (rate: string) => string;
  deltaWithSuccess: (delta: string, rate: string) => string;
  cacheHitSummary: (rate: string, count: string) => string;
  cacheHitNone: string;
  noMovers: string;
  chartAria: (title: string, buckets: number, total: string) => string;
};

const formatSuccessPct = (rate: number) => `${(rate * 100).toFixed(1)}%`;

export function formatMetricValue(
  value: number,
  totals: UsageTotals | null,
  metric: UsageMetricKey,
  format: Formatters,
  currency: string,
): string {
  if (metric === "spend") {
    const nano = totals ? totals.spendNano : BigInt(Math.round(value * 1e9));
    return format.money({ amount: nano.toString(), currency });
  }
  if (metric === "requests") {
    return format.number(totals ? totals.requests : Math.round(value));
  }
  return formatCompactTokens(totals ? totals.tokens : BigInt(Math.round(value)), format.number);
}

const formatReqChange = (totals: UsageTotals, previous: UsageTotals | null, labels: UsageViewLabels): string => {
  const reqDelta = formatDeltaPercent(
    Number(totals.requests),
    previous ? Number(previous.requests) : null,
    labels.newLabel,
  );
  if (totals.successRate === null) return reqDelta;
  const rate = formatSuccessPct(totals.successRate);
  return previous ? labels.deltaWithSuccess(reqDelta, rate) : labels.successOnly(rate);
};

const formatHitChange = (
  currRate: number | null,
  prevRate: number | null | undefined,
  ppDelta: UsageViewLabels["ppDelta"],
): string => {
  if (currRate === null || prevRate === null || prevRate === undefined) return "—";
  const pp = (currRate - prevRate) * 100;
  return ppDelta(pp >= 0 ? "+" : "−", Math.abs(pp).toFixed(1));
};

export function buildKpiView(
  totals: UsageTotals,
  previous: UsageTotals | null,
  currency: string,
  format: Formatters,
  labels: UsageViewLabels,
) {
  const spendParts = splitCurrency(format.money({ amount: totals.spendNano.toString(), currency }));
  const oneParts = splitCurrency(format.money({ amount: "1000000000", currency }));
  const currPpm = totals.tokens > 0n ? Number(totals.spendNano) / (Number(totals.tokens) * 1e3) : 0;
  const prevPpm =
    previous && previous.tokens > 0n
      ? Number(previous.spendNano) / (Number(previous.tokens) * 1e3)
      : previous
        ? 0
        : null;
  return {
    spendCurrency: spendParts.symbol,
    ppmCurrency: oneParts.symbol,
    totals0: spendParts.number,
    totals1: format.number(totals.requests),
    totals2: formatCompactTokens(totals.tokens, format.number),
    totals3: formatSpendPerMillion(totals.spendNano, totals.tokens),
    totals4: totals.cacheHitRate !== null ? (totals.cacheHitRate * 100).toFixed(1) : "—",
    changes0: formatDeltaPercent(
      Number(totals.spendNano),
      previous ? Number(previous.spendNano) : null,
      labels.newLabel,
    ),
    changes1: formatReqChange(totals, previous, labels),
    changes2: formatDeltaPercent(Number(totals.tokens), previous ? Number(previous.tokens) : null, labels.newLabel),
    changes3: formatDeltaPercent(currPpm, prevPpm, labels.newLabel),
    changes4: formatHitChange(totals.cacheHitRate, previous?.cacheHitRate, labels.ppDelta),
  };
}

function formatTickValue(value: number, metric: UsageMetricKey, format: Formatters, currency: string): string {
  if (metric === "spend") {
    const nano = BigInt(Math.round(value * 1e9));
    return format.money({ amount: nano.toString(), currency }).replace(/\.00$/, "");
  }
  if (metric === "requests") return format.number(Math.round(value));
  return formatCompactTokens(BigInt(Math.round(value)), format.number);
}

function formatBucketLabel(
  slot: BucketSlot,
  granularity: UsageGranularity,
  multiDay: boolean,
  long: boolean,
  format: Formatters,
) {
  const start = new Date(slot.atMs);
  if (granularity === "hour") {
    const h = format.hour(start);
    return long || (multiDay && start.getUTCHours() === 0) ? `${format.day(start)} ${h}` : h;
  }
  if (granularity === "week") {
    if (!long) return format.day(start);
    const end = new Date(Date.parse(slot.untilIso) - 1);
    return `${format.day(start)} – ${format.day(end)}`;
  }
  return long ? format.dayFull(start) : format.day(start);
}

function buildGridAndY(
  ticks: readonly number[],
  top: number,
  width: number,
  height: number,
  metric: UsageMetricKey,
  format: Formatters,
  currency: string,
) {
  const gridLines0: UsageProps["rows"]["gridLines0"][number][] = [];
  const yLabels0: UsageProps["rows"]["yLabels0"][number][] = [];
  for (const val of ticks) {
    const yPx = height - (val / top) * height;
    gridLines0.push({
      key: `g-${val}`,
      kind: "0",
      f0: String(val),
      f1: "0",
      f2: String(width),
      f3: yPx.toFixed(1),
      f4: yPx.toFixed(1),
      f5: "opacity: 1;",
    });
    yLabels0.push({
      key: `y-${val}`,
      kind: "0",
      f0: String(val),
      f1: `top: ${((yPx / height) * 100).toFixed(2)}%; opacity: 1;`,
      f2: formatTickValue(val, metric, format, currency),
    });
  }
  return { gridLines0, yLabels0 };
}

function buildBarRects(params: {
  slots: readonly BucketSlot[];
  shown: readonly GroupAggregate[];
  colors: ReadonlyMap<string, string>;
  metric: UsageMetricKey;
  granularity: UsageGranularity;
  bw: number;
  top: number;
  hoverSlot: number | null;
  hlGroup: string | null;
}) {
  const { slots, shown, colors, metric, granularity, bw, top, hoverSlot, hlGroup } = params;
  const height = 200;
  const gap = Math.min(granularity === "hour" ? 3 : 6, bw * 0.28);
  const rx = granularity === "hour" ? "1.5" : "2";
  const barW = Math.max(1, bw - gap).toFixed(1);
  const hits: UsageProps["rows"]["bars0"][number][] = slots.map((slot, i) => ({
    key: `hit-${slot.atMs}`,
    kind: "1",
    f0: hoverSlot === i ? "ushit cur" : "ushit",
    f1: "day",
    f2: String(i),
    f3: (i * bw).toFixed(1),
    f4: "0",
    f5: bw.toFixed(1),
    f6: String(height),
  }));
  const stacks: UsageProps["rows"]["bars0"][number][] = [];
  for (let i = 0; i < slots.length; i++) {
    const slot = slots[i] as BucketSlot;
    let acc = 0;
    const x = (i * bw + gap / 2).toFixed(1);
    for (const g of shown) {
      const val = g.bySlot.get(slot.atMs) ? metricValue(g.bySlot.get(slot.atMs) as UsageTotals, metric) : 0;
      if (val <= 0) continue;
      const hh = (val / top) * height;
      const cls = hlGroup === g.id ? "usbar on" : "usbar";
      const fill = `fill: ${colors.get(g.id) ?? SERIES_COLORS[0]};`;
      stacks.push({
        key: `b-${slot.atMs}-${g.id}`,
        kind: "3",
        f0: cls,
        f1: g.id,
        f2: String(slot.atMs),
        f3: x,
        f4: (height - acc - hh).toFixed(1),
        f5: barW,
        f6: Math.max(1, hh - 1).toFixed(1),
        f7: rx,
        f8: fill,
      });
      acc += hh;
    }
  }
  return [...hits, ...stacks];
}

function buildHoverTip(params: {
  slot: BucketSlot;
  hoverSlot: number;
  shown: readonly GroupAggregate[];
  names: ReadonlyMap<string, string>;
  colors: ReadonlyMap<string, string>;
  metric: UsageMetricKey;
  granularity: UsageGranularity;
  multiDay: boolean;
  bw: number;
  width: number;
  format: Formatters;
  currency: string;
  unknownGroup: string;
}) {
  const {
    slot,
    hoverSlot,
    shown,
    names,
    colors,
    metric,
    granularity,
    multiDay,
    bw,
    width,
    format,
    currency,
    unknownGroup,
  } = params;
  const items = shown
    .map((g) => ({ group: g, slotTotals: g.bySlot.get(slot.atMs) }))
    .filter((x): x is { group: GroupAggregate; slotTotals: UsageTotals } =>
      Boolean(x.slotTotals && metricValue(x.slotTotals, metric) > 0),
    )
    .sort((a, b) => metricValue(b.slotTotals, metric) - metricValue(a.slotTotals, metric));
  const slotSum = items.reduce((s, x) => s + metricValue(x.slotTotals, metric), 0);
  const rightX = (hoverSlot + 1) * bw + 8;
  const leftX = rightX + 200 > width ? Math.max(0, hoverSlot * bw - 208) : rightX;
  const hoverTip: HoverTipData = {
    bucketLabel: formatBucketLabel(slot, granularity, multiDay, true, format),
    totalFormatted: formatMetricValue(slotSum, null, metric, format, currency),
    items: items.slice(0, 4).map(({ group, slotTotals }) => ({
      id: group.id,
      name: names.get(group.id) ?? (group.id || unknownGroup),
      color: colors.get(group.id) ?? SERIES_COLORS[0],
      valueFormatted: formatMetricValue(metricValue(slotTotals, metric), slotTotals, metric, format, currency),
    })),
  };
  return { hoverTip, tipStyle: `left: ${Math.round(leftX)}px;` };
}

function buildXLabels(slots: readonly BucketSlot[], granularity: UsageGranularity, format: Formatters) {
  const multiDay = slots.length > 24;
  return xTickIndices(slots.length, granularity).map((idx): UsageProps["rows"]["xLabels0"][number] => ({
    key: `x-${idx}`,
    kind: "0",
    f0: `left: ${(((idx + 0.5) / slots.length) * 100).toFixed(2)}%;`,
    f1: formatBucketLabel(slots[idx] as BucketSlot, granularity, multiDay, false, format),
  }));
}

export type ChartViewParams = {
  slots: readonly BucketSlot[];
  groups: readonly GroupAggregate[];
  totals: UsageTotals;
  names: ReadonlyMap<string, string>;
  colors: ReadonlyMap<string, string>;
  metric: UsageMetricKey;
  granularity: UsageGranularity;
  chartWidth: number;
  hoverSlot: number | null;
  focusGroup: string | null;
  hoverGroup: string | null;
  format: Formatters;
  currency: string;
  labels: UsageViewLabels;
};

export function buildChartView(p: ChartViewParams) {
  const width = Math.max(240, Math.round(p.chartWidth));
  const sorted = [...p.groups].sort((a, b) => metricValue(b.totals, p.metric) - metricValue(a.totals, p.metric));
  const shown = p.focusGroup === null ? sorted : sorted.filter((g) => g.id === p.focusGroup);
  const colSums = p.slots.map((s) =>
    shown.reduce(
      (acc, g) => acc + (g.bySlot.get(s.atMs) ? metricValue(g.bySlot.get(s.atMs) as UsageTotals, p.metric) : 0),
      0,
    ),
  );
  const { top, ticks } = niceAxisScale(Math.max(0, ...colSums));
  const bw = p.slots.length ? width / p.slots.length : width;
  const { gridLines0, yLabels0 } = buildGridAndY(ticks, top, width, 200, p.metric, p.format, p.currency);
  const bars0 = buildBarRects({
    slots: p.slots,
    shown,
    colors: p.colors,
    metric: p.metric,
    granularity: p.granularity,
    bw,
    top,
    hoverSlot: p.hoverSlot,
    hlGroup: p.hoverGroup ?? p.focusGroup,
  });
  const slotObj = p.hoverSlot !== null ? p.slots[p.hoverSlot] : undefined;
  const tip = slotObj
    ? buildHoverTip({
        slot: slotObj,
        hoverSlot: p.hoverSlot as number,
        shown,
        names: p.names,
        colors: p.colors,
        metric: p.metric,
        granularity: p.granularity,
        multiDay: p.slots.length > 24,
        bw,
        width,
        format: p.format,
        currency: p.currency,
        unknownGroup: p.labels.unknownGroup,
      })
    : { hoverTip: null, tipStyle: "" };
  const title = `${p.labels.unitByGranularity[p.granularity]}${p.labels.metricLabels[p.metric]}`;
  return {
    chartTitle: title,
    chartViewBox: `0 0 ${width} 200`,
    chartAria: p.labels.chartAria(
      title,
      p.slots.length,
      formatMetricValue(metricValue(p.totals, p.metric), p.totals, p.metric, p.format, p.currency),
    ),
    gridLines0,
    bars0,
    yLabels0,
    xLabels0: buildXLabels(p.slots, p.granularity, p.format),
    hoverTip: tip.hoverTip,
    tipStyle: tip.tipStyle,
  };
}

const buildSeg = (
  name: string,
  activeIdx: number,
  items: readonly string[],
  itemLabels: readonly string[],
): UsageProps["rows"]["segs0"][number] => ({
  key: name,
  kind: "0",
  f0: "qseg",
  f1: name,
  f2: `--n: 3; --i: ${activeIdx};`,
  f3: "button",
  f4: activeIdx === 0 ? "on" : "",
  f5: name,
  f6: items[0] as string,
  f7: itemLabels[0] as string,
  f8: "button",
  f9: activeIdx === 1 ? "on" : "",
  f10: name,
  f11: items[1] as string,
  f12: itemLabels[1] as string,
  f13: "button",
  f14: activeIdx === 2 ? "on" : "",
  f15: name,
  f16: items[2] as string,
  f17: itemLabels[2] as string,
});

export function buildSegs(
  granularity: UsageGranularity,
  metric: UsageMetricKey,
  groupBy: UsageGroupBy,
  labels: UsageViewLabels,
): UsageProps["rows"]["segs0"][number][] {
  const grans: UsageGranularity[] = ["hour", "day", "week"];
  const mets: UsageMetricKey[] = ["spend", "requests", "tokens"];
  const grps: UsageGroupBy[] = ["model", "key", "workspace"];
  return [
    buildSeg(
      "gran",
      grans.indexOf(granularity),
      grans,
      grans.map((g) => labels.granularityLabels[g]),
    ),
    buildSeg(
      "met",
      mets.indexOf(metric),
      mets,
      mets.map((m) => labels.metricLabels[m]),
    ),
    buildSeg(
      "grp",
      grps.indexOf(groupBy),
      grps,
      grps.map((g) => labels.groupByLabels[g]),
    ),
  ];
}
