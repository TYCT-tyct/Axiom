import type { Schemas } from "@axiom/api";
import { moneyInput } from "@axiom/ui";
import type { UsageGranularity } from "../queries";

export type UsageMetricKey = "spend" | "requests" | "tokens";
export type UsageRangePreset = "today" | "d7" | "d30" | "month" | "custom";
export type UsageSortCol = "spend" | "requests" | "input" | "output" | "avg";

const HOUR_MS = 3_600_000;
export const DAY_MS = 86_400_000;
const MAX_RANGE_HOURS: Record<UsageGranularity, number> = {
  hour: 7 * 24,
  day: 90 * 24,
  week: 90 * 24,
};

// Board palette order for stacked series and breakdown rows.
export const SERIES_COLORS = ["var(--c0)", "var(--c4)", "var(--c1)", "var(--c3)", "var(--c2)"] as const;

export type UsageTotals = {
  currency: string;
  spendNano: bigint;
  spendReported: boolean;
  requests: bigint;
  requestsReported: boolean;
  successes: bigint;
  successesReported: boolean;
  input: bigint;
  inputReported: boolean;
  output: bigint;
  outputReported: boolean;
  cacheRead: bigint;
  cacheReadReported: boolean;
  cacheWrite: bigint;
  cacheWriteReported: boolean;
  tokens: bigint;
  totalTokensReported: boolean;
  cacheRateRequests: bigint;
  cacheHitRate: number | null;
  successRate: number | null;
};

export type GroupAggregate = {
  id: string;
  totals: UsageTotals;
  bySlot: Map<number, UsageTotals>;
};

export type BucketSlot = {
  atMs: number;
  sinceIso: string;
  untilIso: string;
};

export type RangeValidation =
  | { ok: true; sinceMs: number; untilMs: number; hours: number; days: number }
  | { ok: false; reason: "hour" | "order" | "limit"; days: number; limitDays: number };

const parseOptBig = (value?: string): { value: bigint; reported: boolean } =>
  value !== undefined && /^\d+$/.test(value)
    ? { value: BigInt(value), reported: true }
    : { value: 0n, reported: false };

const bigRatio = (num: bigint, den: bigint): number | null =>
  den > 0n ? Number((num * 10_000n + den / 2n) / den) / 10_000 : null;

export const seriesColor = (index: number) => SERIES_COLORS[index % SERIES_COLORS.length] as string;

export function utcDayOf(date: Date): number {
  return Math.floor(Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate()) / DAY_MS);
}

export function presetDaySpan(preset: Exclude<UsageRangePreset, "custom">, now = new Date()) {
  const today = utcDayOf(now);
  if (preset === "today") return { startDay: today, endDay: today };
  if (preset === "d7") return { startDay: today - 6, endDay: today };
  if (preset === "d30") return { startDay: today - 29, endDay: today };
  const monthStart = Math.floor(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1) / DAY_MS);
  return { startDay: monthStart, endDay: today };
}

export function windowFromDays(startDay: number, endDay: number) {
  const lo = Math.min(startDay, endDay);
  const hi = Math.max(startDay, endDay);
  return {
    since: new Date(lo * DAY_MS).toISOString(),
    until: new Date((hi + 1) * DAY_MS).toISOString(),
  };
}

export function defaultGranularity(preset: UsageRangePreset, days: number): UsageGranularity {
  if (preset === "today") return "hour";
  if (days > 62) return "week";
  return "day";
}

export function validateDaySpan(startDay: number, endDay: number, granularity: UsageGranularity) {
  const days = Math.abs(endDay - startDay) + 1;
  const limitDays = MAX_RANGE_HOURS[granularity] / 24;
  return days > limitDays ? ({ ok: false, days, limitDays } as const) : ({ ok: true, days, limitDays } as const);
}

export function validateUsageRange(sinceIso: string, untilIso: string, granularity: UsageGranularity): RangeValidation {
  const sinceMs = Date.parse(sinceIso);
  const untilMs = Date.parse(untilIso);
  const limitHours = MAX_RANGE_HOURS[granularity];
  const limitDays = limitHours / 24;
  if (!Number.isFinite(sinceMs) || !Number.isFinite(untilMs) || sinceMs % HOUR_MS !== 0 || untilMs % HOUR_MS !== 0) {
    return { ok: false, reason: "hour", days: 0, limitDays };
  }
  if (sinceMs >= untilMs) return { ok: false, reason: "order", days: 0, limitDays };
  const hours = (untilMs - sinceMs) / HOUR_MS;
  const days = Math.ceil(hours / 24);
  if (hours > limitHours) return { ok: false, reason: "limit", days, limitDays };
  return { ok: true, sinceMs, untilMs, hours, days };
}

export function previousWindow(sinceMs: number, untilMs: number) {
  const span = untilMs - sinceMs;
  return {
    since: new Date(sinceMs - span).toISOString(),
    until: new Date(sinceMs).toISOString(),
  };
}

function emptyTotals(currency: string, allReported = false): UsageTotals {
  return {
    currency,
    spendNano: 0n,
    spendReported: allReported,
    requests: 0n,
    requestsReported: allReported,
    successes: 0n,
    successesReported: allReported,
    input: 0n,
    inputReported: allReported,
    output: 0n,
    outputReported: allReported,
    cacheRead: 0n,
    cacheReadReported: allReported,
    cacheWrite: 0n,
    cacheWriteReported: allReported,
    tokens: 0n,
    totalTokensReported: allReported,
    cacheRateRequests: 0n,
    cacheHitRate: null,
    successRate: null,
  };
}

function fromMetric(currency: string, metric: Schemas["UsageMetrics"]): UsageTotals {
  const spend = parseOptBig(metric.spend?.amount);
  const requests = parseOptBig(metric.requests);
  const successes = parseOptBig(metric.successes);
  const input = parseOptBig(metric.tokens?.input);
  const output = parseOptBig(metric.tokens?.output);
  const cacheRead = parseOptBig(metric.tokens?.cache_read);
  const cacheWrite = parseOptBig(metric.tokens?.cache_write);
  const cacheRateRequests = parseOptBig(metric.cache_rate_requests);
  const totalTokensReported = input.reported || output.reported || cacheRead.reported || cacheWrite.reported;
  return {
    currency,
    spendNano: spend.value,
    spendReported: spend.reported,
    requests: requests.value,
    requestsReported: requests.reported,
    successes: successes.value,
    successesReported: successes.reported,
    input: input.value,
    inputReported: input.reported,
    output: output.value,
    outputReported: output.reported,
    cacheRead: cacheRead.value,
    cacheReadReported: cacheRead.reported,
    cacheWrite: cacheWrite.value,
    cacheWriteReported: cacheWrite.reported,
    tokens: input.value + output.value + cacheRead.value + cacheWrite.value,
    totalTokensReported,
    cacheRateRequests: cacheRateRequests.value,
    cacheHitRate: metric.cache_hit_rate ?? null,
    successRate:
      metric.success_rate ??
      (requests.reported && successes.reported ? bigRatio(successes.value, requests.value) : null),
  };
}

function mergeUsageTotals(a: UsageTotals, b: UsageTotals): UsageTotals {
  const requests = a.requests + b.requests;
  const successes = a.successes + b.successes;
  const requestsReported = a.requestsReported || b.requestsReported;
  const successesReported = a.successesReported || b.successesReported;
  const input = a.input + b.input;
  const output = a.output + b.output;
  const cacheRead = a.cacheRead + b.cacheRead;
  const cacheWrite = a.cacheWrite + b.cacheWrite;
  return {
    currency: a.currency,
    spendNano: a.spendNano + b.spendNano,
    spendReported: a.spendReported || b.spendReported,
    requests,
    requestsReported,
    successes,
    successesReported,
    input,
    inputReported: a.inputReported || b.inputReported,
    output,
    outputReported: a.outputReported || b.outputReported,
    cacheRead,
    cacheReadReported: a.cacheReadReported || b.cacheReadReported,
    cacheWrite,
    cacheWriteReported: a.cacheWriteReported || b.cacheWriteReported,
    tokens: input + output + cacheRead + cacheWrite,
    totalTokensReported: a.totalTokensReported || b.totalTokensReported,
    cacheRateRequests: a.cacheRateRequests + b.cacheRateRequests,
    cacheHitRate: null,
    successRate: requestsReported && successesReported ? bigRatio(successes, requests) : null,
  };
}

export function currenciesOfUsage(data: Schemas["UsageStatistics"] | undefined): string[] {
  const out: string[] = [];
  const push = (c?: string) => {
    if (c && !out.includes(c)) out.push(c);
  };
  for (const item of data?.totals ?? []) push(item.spend?.currency);
  for (const bucket of data?.buckets ?? []) push(bucket.metrics?.spend?.currency);
  for (const item of data?.previous ?? []) push(item.spend?.currency);
  return out;
}

export const hasUsageStatisticsPayload = (data: Schemas["UsageStatistics"] | undefined): boolean =>
  Boolean(data && Array.isArray(data.totals) && Array.isArray(data.buckets));

const metricForCurrency = (metric: Schemas["UsageMetrics"], currency: string, hasOtherCurrency: boolean): boolean =>
  metric.spend?.currency ? metric.spend.currency === currency : !hasOtherCurrency;

export function selectCurrency(
  totals: readonly Schemas["UsageMetrics"][] | undefined,
  preferred?: string,
  allCurrencies: readonly string[] = [],
): string | undefined {
  const present = totals?.map((item) => item.spend?.currency).filter((c): c is string => Boolean(c)) ?? [];
  const candidates = allCurrencies.length ? allCurrencies : present;
  if (!candidates.length) return preferred;
  if (preferred && candidates.includes(preferred)) return preferred;
  return candidates[0] ?? preferred;
}

export const hasNonEmptyTotals = (t: UsageTotals | null): boolean =>
  Boolean(t && (t.requests > 0n || t.spendNano > 0n || t.tokens > 0n));

export function hasCurrencyBuckets(buckets: readonly Schemas["UsageBucket"][] | undefined, currency: string): boolean {
  const list = buckets ?? [];
  const hasOther = list.some((b) => Boolean(b.metrics?.spend?.currency && b.metrics.spend.currency !== currency));
  return list.some((b) => {
    const m = b.metrics;
    return Boolean(m && metricForCurrency(m, currency, hasOther) && hasNonEmptyTotals(fromMetric(currency, m)));
  });
}

export function toUsageTotals(
  metrics: readonly Schemas["UsageMetrics"][] | undefined,
  currency: string,
  emptyAsZero = false,
): UsageTotals | null {
  if (!Array.isArray(metrics)) return null;
  const hasOther = metrics.some((item) => Boolean(item.spend?.currency && item.spend.currency !== currency));
  const matching = metrics.filter((item) => metricForCurrency(item, currency, hasOther));
  if (!matching.length) return emptyAsZero ? emptyTotals(currency, true) : null;
  if (matching.length === 1) return fromMetric(currency, matching[0] as Schemas["UsageMetrics"]);
  let acc = emptyTotals(currency);
  for (const item of matching) acc = mergeUsageTotals(acc, fromMetric(currency, item));
  acc.cacheHitRate = acc.cacheRateRequests > 0n ? bigRatio(acc.cacheRead, acc.input + acc.cacheRead) : null;
  return acc;
}

function utcWeekFloorMs(ms: number): number {
  const day = Math.floor(ms / DAY_MS);
  const weekday = (new Date(day * DAY_MS).getUTCDay() + 6) % 7;
  return (day - weekday) * DAY_MS;
}

function alignBucketMs(ms: number, granularity: UsageGranularity): number {
  if (granularity === "hour") return Math.floor(ms / HOUR_MS) * HOUR_MS;
  if (granularity === "day") return Math.floor(ms / DAY_MS) * DAY_MS;
  return utcWeekFloorMs(ms);
}

export function buildBucketSlots(sinceMs: number, untilMs: number, granularity: UsageGranularity): BucketSlot[] {
  const slots: BucketSlot[] = [];
  const step = granularity === "hour" ? HOUR_MS : granularity === "day" ? DAY_MS : 7 * DAY_MS;
  for (let atMs = alignBucketMs(sinceMs, granularity); atMs < untilMs; atMs += step) {
    const start = Math.max(atMs, sinceMs);
    const end = Math.min(atMs + step, untilMs);
    slots.push({ atMs, sinceIso: new Date(start).toISOString(), untilIso: new Date(end).toISOString() });
  }
  return slots;
}

function addBucket(
  map: Map<string, GroupAggregate>,
  bucket: Schemas["UsageBucket"],
  currency: string,
  granularity: UsageGranularity,
  hasOtherCurrency: boolean,
) {
  const m = bucket.metrics;
  if (!m || !metricForCurrency(m, currency, hasOtherCurrency)) return;
  const item = fromMetric(currency, m);
  const id = bucket.group ?? "";
  const entry = map.get(id) ?? { id, totals: emptyTotals(currency), bySlot: new Map<number, UsageTotals>() };
  map.set(id, entry);
  entry.totals = mergeUsageTotals(entry.totals, item);
  const parsedAt = bucket.at ? Date.parse(bucket.at) : Number.NaN;
  if (!Number.isFinite(parsedAt)) return;
  const slotMs = alignBucketMs(parsedAt, granularity);
  entry.bySlot.set(slotMs, mergeUsageTotals(entry.bySlot.get(slotMs) ?? emptyTotals(currency), item));
}

export function aggregateGroups(
  buckets: readonly Schemas["UsageBucket"][] | undefined,
  currency: string,
  granularity: UsageGranularity,
): GroupAggregate[] {
  const list = buckets ?? [];
  const hasOther = list.some((b) => Boolean(b.metrics?.spend?.currency && b.metrics.spend.currency !== currency));
  const map = new Map<string, GroupAggregate>();
  for (const bucket of list) addBucket(map, bucket, currency, granularity, hasOther);
  return [...map.values()];
}

export function sumUsageTotals(items: readonly UsageTotals[], currency: string): UsageTotals {
  let acc = emptyTotals(currency);
  for (const item of items) acc = mergeUsageTotals(acc, item);
  if (acc.cacheRateRequests > 0n) acc.cacheHitRate = bigRatio(acc.cacheRead, acc.input + acc.cacheRead);
  return acc;
}

export const sumGroupTotals = (groups: readonly GroupAggregate[], currency: string): UsageTotals =>
  sumUsageTotals(
    groups.map((g) => g.totals),
    currency,
  );

export function metricValue(totals: UsageTotals, metric: UsageMetricKey): number {
  if (metric === "spend") return Number(totals.spendNano) / 1e9;
  if (metric === "requests") return Number(totals.requests);
  return Number(totals.tokens);
}

const cmpBig = (a: bigint, b: bigint): number => (a < b ? -1 : a > b ? 1 : 0);
const cmpReported = (aOk: boolean, bOk: boolean, cmp: () => number): number =>
  (aOk ? 1 : 0) - (bOk ? 1 : 0) || (aOk ? cmp() : 0);

const sortComparators: Record<UsageSortCol, (a: UsageTotals, b: UsageTotals) => number> = {
  spend: (a, b) => cmpReported(a.spendReported, b.spendReported, () => cmpBig(a.spendNano, b.spendNano)),
  requests: (a, b) => cmpReported(a.requestsReported, b.requestsReported, () => cmpBig(a.requests, b.requests)),
  input: (a, b) => cmpReported(a.inputReported, b.inputReported, () => cmpBig(a.input, b.input)),
  output: (a, b) => cmpReported(a.outputReported, b.outputReported, () => cmpBig(a.output, b.output)),
  avg: (a, b) =>
    cmpReported(
      a.spendReported && a.requestsReported && a.requests > 0n,
      b.spendReported && b.requestsReported && b.requests > 0n,
      () => cmpBig(a.spendNano * b.requests, b.spendNano * a.requests),
    ),
};

export const compareUsageTotals = (a: UsageTotals, b: UsageTotals, col: UsageSortCol): number =>
  sortComparators[col](a, b);

export function metricBigValue(totals: UsageTotals, metric: UsageMetricKey): bigint {
  if (metric === "spend") return totals.spendNano;
  if (metric === "requests") return totals.requests;
  return totals.tokens;
}

export function sortColForMetric(metric: UsageMetricKey): UsageSortCol {
  if (metric === "requests") return "requests";
  if (metric === "tokens") return "input";
  return "spend";
}

export function niceAxisScale(maxValue: number, integerOnly = false): { step: number; top: number; ticks: number[] } {
  const mx = maxValue > 0 ? maxValue : 1;
  const raw = mx / 3;
  const pow = 10 ** Math.floor(Math.log10(raw));
  const frac = raw / pow;
  const baseStep = Number(((frac <= 1 ? 1 : frac <= 2 ? 2 : frac <= 5 ? 5 : 10) * pow).toPrecision(10));
  const step = integerOnly ? Math.max(1, baseStep) : baseStep;
  const top = Math.max(1, Math.ceil(mx / step)) * step * 1.02;
  const ticks: number[] = [];
  for (let i = 1; i * step < top; i++) ticks.push(Number((i * step).toPrecision(10)));
  return { step, top, ticks };
}

function formatScaledUnit(value: bigint, div: bigint, suffix: string): string {
  const tenths = (value * 10n + div / 2n) / div;
  return `${tenths / 10n}.${tenths % 10n}${suffix}`;
}

export function formatCompactTokens(tokens: bigint, integerFmt: (n: bigint | number) => string): string {
  if (tokens < 1000n) return integerFmt(tokens);
  if (tokens < 999_950n) return formatScaledUnit(tokens, 1_000n, "k");
  if (tokens < 999_950_000n) return formatScaledUnit(tokens, 1_000_000n, "M");
  return formatScaledUnit(tokens, 1_000_000_000n, "B");
}

export function splitCurrency(formatted: string): { symbol: string; number: string } {
  const m = /^([^\d−+-]*)\s*([\d,]+(?:\.\d+)?.*)$/.exec(formatted);
  return m ? { symbol: (m[1] ?? "").trim(), number: (m[2] ?? formatted).trim() } : { symbol: "", number: formatted };
}

export function formatSpendPerMillion(totals: UsageTotals): string {
  if (!totals.spendReported || !totals.totalTokensReported || totals.tokens <= 0n) return "—";
  const cents = (totals.spendNano + totals.tokens * 5n) / (totals.tokens * 10n);
  return `${cents / 100n}.${(cents % 100n).toString().padStart(2, "0")}`;
}

export function formatAvgPerRequest(
  totals: UsageTotals,
  moneyFmt: (m: { amount: string; currency: string }) => string,
  currency: string,
): string {
  if (!totals.spendReported || !totals.requestsReported || totals.requests <= 0n) return "—";
  const roundedNano = (totals.spendNano + totals.requests / 2n) / totals.requests;
  return moneyFmt({ amount: roundedNano.toString(), currency });
}

export function formatBigDeltaPercent(curr: bigint, prev: bigint | null, newLabel: string): string {
  if (prev === null) return "—";
  if (prev === 0n) return curr > 0n ? newLabel : "0%";
  const diff = curr - prev;
  const sign = diff >= 0n ? "+" : "−";
  const absDiff = diff >= 0n ? diff : -diff;
  const tenths = (absDiff * 1000n + prev / 2n) / prev;
  if (tenths < 100n) return `${sign}${tenths / 10n}.${tenths % 10n}%`;
  const whole = (absDiff * 100n + prev / 2n) / prev;
  return `${sign}${whole}%`;
}

export function xTickIndices(count: number, granularity: UsageGranularity): number[] {
  if (count <= 0) return [];
  if (count === 1) return [0];
  const ticks: number[] = [];
  if (granularity === "hour") {
    const step = count <= 24 ? 6 : Math.max(6, Math.round(count / 5));
    for (let i = 0; i < count; i += step) ticks.push(i);
  } else {
    const step = Math.max(1, Math.round(count / 5));
    for (let i = 0; i < count; i += step) ticks.push(i);
    if (count - 1 - (ticks.at(-1) ?? 0) >= step * 0.6) ticks.push(count - 1);
    return ticks;
  }
  if ((ticks.at(-1) ?? -1) !== count - 1) ticks.push(count - 1);
  return ticks;
}

const csvSafe = (value: string): string => {
  const lead = value.trimStart();
  const escaped = /^[=+\-@\t\r]/.test(value) || /^[=+\-@]/.test(lead) ? `'${value}` : value;
  return /[",\n\r]/.test(escaped) ? `"${escaped.replaceAll('"', '""')}"` : escaped;
};

const optCell = (reported: boolean, val: bigint) => (reported ? val.toString() : "");

const csvRow = (slotIso: string, groupName: string, m: UsageTotals) =>
  [
    slotIso,
    m.currency,
    groupName,
    optCell(m.requestsReported, m.requests),
    optCell(m.successesReported, m.successes),
    m.spendReported ? moneyInput(m.spendNano.toString()) : "",
    optCell(m.inputReported, m.input),
    optCell(m.outputReported, m.output),
    optCell(m.cacheReadReported, m.cacheRead),
    optCell(m.cacheWriteReported, m.cacheWrite),
  ]
    .map(csvSafe)
    .join(",");

export function buildUsageCsv(
  headers: readonly string[],
  slots: readonly BucketSlot[],
  groups: readonly GroupAggregate[],
  groupNames: ReadonlyMap<string, string>,
): string {
  const lines = [headers.map(csvSafe).join(",")];
  for (const slot of slots) {
    for (const group of groups) {
      const m = group.bySlot.get(slot.atMs);
      if (m && (m.requests > 0n || m.spendNano > 0n || m.tokens > 0n)) {
        lines.push(csvRow(slot.sinceIso, groupNames.get(group.id) ?? group.id, m));
      }
    }
  }
  return `\uFEFF${lines.join("\n")}\n`;
}
