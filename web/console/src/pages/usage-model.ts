import type { Schemas } from "@axiom/api";
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
  requests: bigint;
  successes: bigint;
  input: bigint;
  output: bigint;
  cacheRead: bigint;
  cacheWrite: bigint;
  tokens: bigint;
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

const parseBig = (value?: string): bigint => (value && /^\d+$/.test(value) ? BigInt(value) : 0n);

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

function emptyTotals(currency: string): UsageTotals {
  return {
    currency,
    spendNano: 0n,
    requests: 0n,
    successes: 0n,
    input: 0n,
    output: 0n,
    cacheRead: 0n,
    cacheWrite: 0n,
    tokens: 0n,
    cacheRateRequests: 0n,
    cacheHitRate: null,
    successRate: null,
  };
}

function addMetric(target: UsageTotals, metric: Schemas["UsageMetrics"]): UsageTotals {
  const input = target.input + parseBig(metric.tokens?.input);
  const output = target.output + parseBig(metric.tokens?.output);
  const cacheRead = target.cacheRead + parseBig(metric.tokens?.cache_read);
  const cacheWrite = target.cacheWrite + parseBig(metric.tokens?.cache_write);
  const requests = target.requests + parseBig(metric.requests);
  const successes = target.successes + parseBig(metric.successes);
  return {
    currency: target.currency,
    spendNano: target.spendNano + parseBig(metric.spend?.amount),
    requests,
    successes,
    input,
    output,
    cacheRead,
    cacheWrite,
    tokens: input + output + cacheRead + cacheWrite,
    cacheRateRequests: target.cacheRateRequests + parseBig(metric.cache_rate_requests),
    cacheHitRate: target.cacheHitRate,
    successRate: requests > 0n ? Number(successes) / Number(requests) : null,
  };
}

export function selectCurrency(
  totals: readonly Schemas["UsageMetrics"][] | undefined,
  preferred?: string,
): string | undefined {
  if (!totals?.length) return preferred;
  if (preferred && totals.some((item) => item.spend?.currency === preferred)) return preferred;
  return totals[0]?.spend?.currency ?? preferred;
}

export function toUsageTotals(
  metrics: readonly Schemas["UsageMetrics"][] | undefined,
  currency: string,
): UsageTotals | null {
  const matching = metrics?.filter((item) => (item.spend?.currency ?? currency) === currency) ?? [];
  if (!matching.length) return null;
  let acc = emptyTotals(currency);
  for (const item of matching) acc = addMetric(acc, item);
  if (matching.length === 1) {
    const single = matching[0] as Schemas["UsageMetrics"];
    acc.cacheHitRate = single.cache_hit_rate ?? null;
    acc.successRate = single.success_rate ?? null;
  } else {
    const denom = acc.input + acc.cacheRead;
    acc.cacheHitRate = acc.cacheRateRequests > 0n && denom > 0n ? Number(acc.cacheRead) / Number(denom) : null;
  }
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
) {
  const m = bucket.metrics;
  if (!m || (m.spend?.currency ?? currency) !== currency) return;
  const id = bucket.group ?? "";
  const entry = map.get(id) ?? { id, totals: emptyTotals(currency), bySlot: new Map<number, UsageTotals>() };
  map.set(id, entry);
  entry.totals = addMetric(entry.totals, m);
  const parsedAt = bucket.at ? Date.parse(bucket.at) : Number.NaN;
  if (!Number.isFinite(parsedAt)) return;
  const slotMs = alignBucketMs(parsedAt, granularity);
  entry.bySlot.set(slotMs, addMetric(entry.bySlot.get(slotMs) ?? emptyTotals(currency), m));
}

export function aggregateGroups(
  buckets: readonly Schemas["UsageBucket"][] | undefined,
  currency: string,
  granularity: UsageGranularity,
): GroupAggregate[] {
  const map = new Map<string, GroupAggregate>();
  for (const bucket of buckets ?? []) addBucket(map, bucket, currency, granularity);
  return [...map.values()];
}

export function sumGroupTotals(groups: readonly GroupAggregate[], currency: string): UsageTotals {
  let spendNano = 0n;
  let requests = 0n;
  let successes = 0n;
  let input = 0n;
  let output = 0n;
  let cacheRead = 0n;
  let cacheWrite = 0n;
  let cacheRateRequests = 0n;
  for (const group of groups) {
    spendNano += group.totals.spendNano;
    requests += group.totals.requests;
    successes += group.totals.successes;
    input += group.totals.input;
    output += group.totals.output;
    cacheRead += group.totals.cacheRead;
    cacheWrite += group.totals.cacheWrite;
    cacheRateRequests += group.totals.cacheRateRequests;
  }
  return {
    currency,
    spendNano,
    requests,
    successes,
    input,
    output,
    cacheRead,
    cacheWrite,
    tokens: input + output + cacheRead + cacheWrite,
    cacheRateRequests,
    cacheHitRate: null,
    successRate: requests > 0n ? Number(successes) / Number(requests) : null,
  };
}

export function metricValue(totals: UsageTotals, metric: UsageMetricKey): number {
  if (metric === "spend") return Number(totals.spendNano) / 1e9;
  if (metric === "requests") return Number(totals.requests);
  return Number(totals.tokens);
}

export function sortValue(totals: UsageTotals, col: UsageSortCol): number {
  if (col === "spend") return Number(totals.spendNano);
  if (col === "requests") return Number(totals.requests);
  if (col === "input") return Number(totals.input);
  if (col === "output") return Number(totals.output);
  return totals.requests > 0n ? Number(totals.spendNano) / Number(totals.requests) : 0;
}

export function sortColForMetric(metric: UsageMetricKey): UsageSortCol {
  if (metric === "requests") return "requests";
  if (metric === "tokens") return "input";
  return "spend";
}

export function niceAxisScale(maxValue: number): { step: number; top: number; ticks: number[] } {
  const mx = maxValue > 0 ? maxValue : 1;
  const raw = mx / 3;
  const pow = 10 ** Math.floor(Math.log10(raw));
  const frac = raw / pow;
  const step = Number(((frac <= 1 ? 1 : frac <= 2 ? 2 : frac <= 5 ? 5 : 10) * pow).toPrecision(10));
  const top = Math.max(1, Math.ceil(mx / step)) * step * 1.02;
  const ticks: number[] = [];
  for (let i = 1; i * step < top; i++) ticks.push(Number((i * step).toPrecision(10)));
  return { step, top, ticks };
}

export function formatCompactTokens(tokens: bigint, integerFmt: (n: bigint | number) => string): string {
  if (tokens < 1000n) return integerFmt(tokens);
  const n = Number(tokens);
  if (n < 1_000_000) return `${(n / 1e3).toFixed(1)}k`;
  if (n < 1_000_000_000) return `${(n / 1e6).toFixed(1)}M`;
  return `${(n / 1e9).toFixed(1)}B`;
}

export function splitCurrency(formatted: string): { symbol: string; number: string } {
  const m = /^([^\d−+-]*)\s*([\d,]+(?:\.\d+)?.*)$/.exec(formatted);
  return m ? { symbol: (m[1] ?? "").trim(), number: (m[2] ?? formatted).trim() } : { symbol: "", number: formatted };
}

function nanoToMoneyString(nano: bigint): string {
  const sign = nano < 0n ? "-" : "";
  const abs = nano < 0n ? -nano : nano;
  const whole = abs / 1_000_000_000n;
  const frac = (abs % 1_000_000_000n).toString().padStart(9, "0").replace(/0+$/, "");
  return frac ? `${sign}${whole}.${frac}` : `${sign}${whole}`;
}

export function formatSpendPerMillion(spendNano: bigint, tokens: bigint): string {
  if (tokens <= 0n || spendNano <= 0n) return "0.00";
  const ppm = Number(spendNano) / (Number(tokens) * 1e3);
  return ppm.toFixed(2);
}

export function formatAvgPerRequest(
  spendNano: bigint,
  requests: bigint,
  moneyFmt: (m: { amount: string; currency: string }) => string,
  currency: string,
): string {
  if (requests <= 0n) return "—";
  const roundedNano = (spendNano + requests / 2n) / requests;
  if (roundedNano > 0n && roundedNano < 10_000_000n) {
    const millRounded = ((roundedNano + 500_000n) / 1_000_000n) * 1_000_000n;
    const formatted = moneyFmt({ amount: millRounded.toString(), currency });
    const parts = /^(.*\.\d{2})(\d)$/.exec(formatted);
    return parts ? formatted : moneyFmt({ amount: roundedNano.toString(), currency });
  }
  return moneyFmt({ amount: roundedNano.toString(), currency });
}

export function formatDeltaPercent(curr: number, prev: number | null, newLabel: string): string {
  if (prev === null) return "—";
  if (prev === 0) return curr > 0 ? newLabel : "—";
  const pct = (curr / prev - 1) * 100;
  return `${pct >= 0 ? "+" : "−"}${Math.abs(pct).toFixed(Math.abs(pct) < 10 ? 1 : 0)}%`;
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
  const trimmed = /^[=+\-@\t\r]/.test(value) ? `'${value}` : value;
  return /[",\n\r]/.test(trimmed) ? `"${trimmed.replaceAll('"', '""')}"` : trimmed;
};

const csvRow = (slotIso: string, groupName: string, m: UsageTotals) =>
  [
    slotIso,
    groupName,
    m.requests.toString(),
    m.successes.toString(),
    nanoToMoneyString(m.spendNano),
    m.input.toString(),
    m.output.toString(),
    m.cacheRead.toString(),
    m.cacheWrite.toString(),
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
      if (m && m.requests > 0n) lines.push(csvRow(slot.sinceIso, groupNames.get(group.id) ?? group.id, m));
    }
  }
  return `\uFEFF${lines.join("\n")}\n`;
}
