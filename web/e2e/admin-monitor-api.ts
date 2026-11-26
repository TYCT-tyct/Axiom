import type { AdminState, Schemas } from "./admin-data.ts";
import { base, type Ctx, reads, route } from "./admin-route.ts";
import { type Handler, ok } from "./mock.ts";

// The operations monitor of the last hour or day (getOperationsMonitor) and the supplier credentials with what each holds
// now (listSupplierCredentials), made from what the mock suppliers have: three lines, one healthy, one under 99% and one
// under 97%, so the score has something to take off, and a credential at its ceiling.

// The ten busiest groups of the mocked calls under a key, each with its calls and errors.
export const ranked = (state: AdminState) => (key: (call: Schemas["AdminCallSummary"]) => string | undefined) =>
  [
    ...state.calls
      .reduce((groups, call) => {
        const name = key(call) ?? "";
        const group = groups.get(name) ?? { calls: 0, errors: 0 };
        groups.set(name, { calls: group.calls + 1, errors: group.errors + (call.error_kind ? 1 : 0) });
        return groups;
      }, new Map<string, { calls: number; errors: number }>())
      .entries(),
  ]
    .sort((a, b) => b[1].calls - a[1].calls)
    .slice(0, 10);

// The window's tokens, its busiest spenders and a line of days for the trend lines: the mocked totals spread over each
// UTC day the window touches, a little more on each later day.
export function overviewExtras(
  state: AdminState,
  query: URLSearchParams,
  revenue: bigint,
  cost: bigint,
  errors: number,
) {
  const calls = state.calls.length;
  const spend = new Map<string, bigint>();
  for (const call of state.calls) {
    const account = call.account_id ?? "";
    spend.set(account, (spend.get(account) ?? 0n) + BigInt(call.price?.amount ?? 0));
  }
  const first = Date.parse(query.get("since") ?? "2026-10-29T00:00:00.000Z");
  const last = Date.parse(query.get("until") ?? "2026-10-30T23:59:59.000Z");
  const dates: string[] = [];
  for (let at = Math.floor(first / 864e5) * 864e5; at <= last; at += 864e5) {
    dates.push(new Date(at).toISOString().slice(0, 10));
  }
  const weight = (dates.length * (dates.length + 1)) / 2;
  const days = dates.map((date, i) => {
    const part = (total: number) => Math.floor((total * (i + 1)) / weight);
    const money = (total: bigint) => ({ amount: String((total * BigInt(i + 1)) / BigInt(weight)), currency: "USD" });
    return {
      date,
      calls: part(calls),
      errors: part(errors),
      revenue: money(revenue),
      cost: money(cost),
      tokens: { input: part(calls) * 600, cache_read: part(calls) * 300, cache_write: 0, output: part(calls) * 400 },
      active_accounts: 1 + (i % 3),
      new_users: i % 2,
      first_token_ms_p50: 400 + i * 10,
    };
  });
  return {
    tokens: { input: calls * 600, cache_read: calls * 300, cache_write: 0, output: calls * 400 },
    active_accounts: spend.size,
    new_users: 2,
    first_token_ms_p50: 430,
    top_spenders: [...spend]
      .sort((a, b) => (b[1] > a[1] ? 1 : b[1] < a[1] ? -1 : 0))
      .slice(0, 5)
      .map(([account_id, amount]) => ({
        account_id: account_id as Schemas["AccountId"],
        spend: { amount: String(amount), currency: "USD" },
      })),
    days,
  };
}

type Bucket = Schemas["MonitorBucket"];

function buckets(window: "1h" | "24h", now: number): Bucket[] {
  const count = window === "1h" ? 60 : 24;
  const step = window === "1h" ? 60_000 : 3_600_000;
  const end = Math.floor(now / step) * step;
  return Array.from({ length: count }, (_, i) => {
    const calls = 10 + (i % 7);
    return {
      at: new Date(end - (count - 1 - i) * step).toISOString(),
      calls,
      failures: i % 9 === 0 ? 2 : 0,
      tokens: calls * 1500,
      first_token_ms_p50: 400 + (i % 5) * 20,
      first_token_ms_p95: 1100 + (i % 4) * 100,
    };
  });
}

function getMonitor({ query, state }: Ctx<"/api/admin/v1/reports/monitor", "get">) {
  const window = query.get("window") === "24h" ? ("24h" as const) : ("1h" as const);
  const now = Date.now();
  const series = buckets(window, now);
  const sum = (pick: (bucket: Bucket) => number) => series.reduce((total, bucket) => total + pick(bucket), 0);
  const failures = sum((bucket) => bucket.failures);
  const [first, second] = state.suppliers;
  return ok({
    window,
    since: new Date(now - (window === "1h" ? 3_600_000 : 86_400_000)).toISOString(),
    until: new Date(now).toISOString(),
    bucket_seconds: window === "1h" ? (60 as const) : (3600 as const),
    buckets: series,
    calls: sum((bucket) => bucket.calls),
    failures,
    tokens: sum((bucket) => bucket.tokens),
    switched_calls: 3,
    first_token_ms: { p50: 430, p90: 900, p95: 1200, p99: 2500 },
    total_ms: { p50: 4200, p90: 9000, p95: 12000, p99: 30000 },
    durations: [
      { below_ms: 1000, calls: 10 },
      { below_ms: 2000, calls: 30 },
      { below_ms: 5000, calls: 40 },
      { below_ms: 10000, calls: 15 },
      { calls: 5 },
    ],
    failure_causes: [
      { error_kind: "upstream_error", upstream: true, calls: Math.max(0, failures - 1) },
      { error_kind: "invalid_request", upstream: false, calls: Math.min(1, failures) },
    ],
    lines: [
      {
        supplier_id: first?.supplier.id ?? "",
        group_id: first?.groups[0]?.id ?? "",
        calls: 400,
        failures: 2,
        first_token_ms_p50: 410,
      },
      {
        supplier_id: first?.supplier.id ?? "",
        group_id: first?.groups[1]?.id ?? "",
        calls: 200,
        failures: 10,
        first_token_ms_p50: 520,
      },
      { supplier_id: second?.supplier.id ?? "", group_id: second?.groups[0]?.id ?? "", calls: 100, failures: 2 },
    ],
  });
}

function listCredentials({ query, state }: Ctx<"/api/admin/v1/supplier-credentials", "get">) {
  const wanted = query.get("status");
  const supplier = query.get("supplier_id");
  const credentials = state.suppliers
    .filter((detail) => !supplier || detail.supplier.id === supplier)
    .flatMap((detail) => detail.credentials.map((credential) => ({ ...credential, supplier_id: detail.supplier.id })))
    .filter((credential) => !wanted || credential.status === wanted)
    .map((credential, at) => ({ ...credential, in_flight: at === 0 ? credential.max_concurrency : 1 }));
  return ok({ data: credentials, next_cursor: null });
}

export const monitorHandlers: Handler<AdminState>[] = [
  route("get", `${base}/reports/monitor`, reads("sites.manage"), getMonitor),
  route("get", `${base}/supplier-credentials`, reads("suppliers.view"), listCredentials),
];
