import type { paths } from "../packages/api/src/console.gen.ts";
import type { MockState, Schemas } from "./console-data.ts";
import { type Ctx as CoreCtx, fail, ok } from "./mock.ts";

type Ctx<K extends keyof paths, M extends "get" | "post" | "put" | "delete"> = CoreCtx<paths, K, M, MockState>;

type UsageAcc = {
  currency: string;
  spend: bigint;
  requests: number;
  successes: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  cacheRateRequests: number;
};

const HOUR_MS = 3_600_000;
const DAY_MS = 86_400_000;
const GRANS = new Set(["hour", "day", "week"]);
const GROUPS = new Set(["model", "key", "workspace"]);

function usageBucketFloor(ms: number, granularity: "hour" | "day" | "week"): number {
  if (granularity === "hour") return Math.floor(ms / HOUR_MS) * HOUR_MS;
  if (granularity === "day") return Math.floor(ms / DAY_MS) * DAY_MS;
  const day = Math.floor(ms / DAY_MS);
  const weekday = (new Date(day * DAY_MS).getUTCDay() + 6) % 7;
  return (day - weekday) * DAY_MS;
}

const emptyAcc = (currency: string): UsageAcc => ({
  currency,
  spend: 0n,
  requests: 0,
  successes: 0,
  input: 0,
  output: 0,
  cacheRead: 0,
  cacheWrite: 0,
  cacheRateRequests: 0,
});

function accumulateCall(acc: UsageAcc, call: Schemas["Call"]) {
  acc.spend += BigInt(call.price.amount);
  acc.requests += 1;
  if (call.error_kind === "") acc.successes += 1;
  const inp = call.usage?.input ?? 0;
  const cr = call.usage?.cache_read ?? 0;
  acc.input += inp;
  acc.output += call.usage?.output ?? 0;
  acc.cacheRead += cr;
  acc.cacheWrite += call.usage?.cache_write ?? 0;
  if (inp + cr > 0) acc.cacheRateRequests += 1;
}

function toUsageMetrics(acc: UsageAcc): Schemas["UsageMetrics"] {
  const denom = acc.input + acc.cacheRead;
  return {
    spend: { amount: acc.spend.toString(), currency: acc.currency },
    requests: String(acc.requests),
    successes: String(acc.successes),
    tokens: {
      input: String(acc.input),
      output: String(acc.output),
      cache_read: String(acc.cacheRead),
      cache_write: String(acc.cacheWrite),
    },
    cache_rate_requests: String(acc.cacheRateRequests),
    ...(acc.cacheRateRequests > 0 && denom > 0 ? { cache_hit_rate: acc.cacheRead / denom } : {}),
    ...(acc.requests > 0 ? { success_rate: acc.successes / acc.requests } : {}),
  };
}

function usageGroupKey(call: Schemas["Call"], groupBy?: "model" | "key" | "workspace"): string | undefined {
  if (groupBy === "model") return call.model;
  if (groupBy === "key") return String(call.key_id);
  if (groupBy === "workspace") return String(call.workspace_id);
  return undefined;
}

function rollupUsageWindow(
  calls: Schemas["Call"][],
  sinceMs: number,
  untilMs: number,
  granularity: "hour" | "day" | "week",
  groupBy: "model" | "key" | "workspace" | undefined,
) {
  const bucketMap = new Map<string, { at: string; group?: string; acc: UsageAcc }>();
  const totalsMap = new Map<string, UsageAcc>();
  for (const call of calls) {
    const atMs = Date.parse(call.at);
    if (!Number.isFinite(atMs) || atMs < sinceMs || atMs >= untilMs) continue;
    const totalAcc = totalsMap.get(call.price.currency) ?? emptyAcc(call.price.currency);
    accumulateCall(totalAcc, call);
    totalsMap.set(call.price.currency, totalAcc);
    const at = new Date(usageBucketFloor(atMs, granularity)).toISOString();
    const group = usageGroupKey(call, groupBy);
    const key = `${at}|${group ?? ""}|${call.price.currency}`;
    const entry = bucketMap.get(key) ?? { at, group, acc: emptyAcc(call.price.currency) };
    accumulateCall(entry.acc, call);
    bucketMap.set(key, entry);
  }
  const buckets: Schemas["UsageBucket"][] = [...bucketMap.values()]
    .sort((a, b) => a.at.localeCompare(b.at) || (a.group ?? "").localeCompare(b.group ?? ""))
    .map((b) => ({ at: b.at, ...(b.group !== undefined ? { group: b.group } : {}), metrics: toUsageMetrics(b.acc) }));
  return { buckets, totals: [...totalsMap.values()].map(toUsageMetrics) };
}

function validHourWindow(sinceMs: number, untilMs: number, gran: string) {
  if (!Number.isFinite(sinceMs) || !Number.isFinite(untilMs) || sinceMs >= untilMs) return false;
  if (sinceMs % HOUR_MS !== 0 || untilMs % HOUR_MS !== 0) return false;
  return untilMs - sinceMs <= (gran === "hour" ? 7 : 90) * DAY_MS;
}

function parseUsageQuery(query: URLSearchParams) {
  const gran = query.get("granularity") ?? "hour";
  const groupRaw = query.get("group_by");
  if (!GRANS.has(gran)) return null;
  if (groupRaw && !GROUPS.has(groupRaw)) return null;
  const sinceMs = Date.parse(query.get("since") ?? "2026-10-13T00:00:00Z");
  const untilMs = Date.parse(query.get("until") ?? "2026-10-14T00:00:00Z");
  if (!validHourWindow(sinceMs, untilMs, gran)) return null;
  return {
    gran: gran as "hour" | "day" | "week",
    groupBy: (groupRaw ?? undefined) as "model" | "key" | "workspace" | undefined,
    sinceMs,
    untilMs,
  };
}

export function usageStatistics({ query, state }: Ctx<"/api/console/v1/usage/statistics", "get">) {
  const parsed = parseUsageQuery(query);
  if (!parsed) return fail(400, "invalid_request");
  const same = (value: string, filter: string | null) => !filter || value === filter;
  const filtered = state.calls.filter(
    (c) =>
      same(c.model, query.get("model")) &&
      same(c.key_id, query.get("key_id")) &&
      same(c.workspace_id, query.get("workspace_id")),
  );
  const curr = rollupUsageWindow(filtered, parsed.sinceMs, parsed.untilMs, parsed.gran, parsed.groupBy);
  const span = parsed.untilMs - parsed.sinceMs;
  const previous =
    query.get("compare") === "previous"
      ? rollupUsageWindow(filtered, parsed.sinceMs - span, parsed.sinceMs, parsed.gran, undefined).totals
      : undefined;
  return ok({
    since: new Date(parsed.sinceMs).toISOString(),
    until: new Date(parsed.untilMs).toISOString(),
    granularity: parsed.gran,
    ...(parsed.groupBy ? { group_by: parsed.groupBy } : {}),
    backfill_complete: state.usageBackfillComplete ?? true,
    buckets: curr.buckets,
    totals: curr.totals,
    ...(previous !== undefined ? { previous } : {}),
  });
}
