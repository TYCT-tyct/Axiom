import type { AdminSchemas } from "@axiom/api";

// What the operations monitor works out from the window the API answers (getOperationsMonitor): rates per minute, how the
// failures divide, and the completion-time bins as ranges with their share. Pure, so the figures are tested without a page.

export type Monitor = AdminSchemas["OperationsMonitor"];

const minutesOf = (monitor: Pick<Monitor, "since" | "until">) =>
  Math.max(1, (Date.parse(monitor.until) - Date.parse(monitor.since)) / 60_000);

// A count over the window as a rate per minute: the window's average, and the busiest bucket's, which a minute's bucket
// holds as it is and an hour's holds as sixty minutes.
export function perMinute(monitor: Monitor, pick: (bucket: Monitor["buckets"][number]) => number, total: number) {
  const minutesPerBucket = monitor.bucket_seconds / 60;
  return {
    average: total / minutesOf(monitor),
    peak: Math.max(0, ...monitor.buckets.map((bucket) => pick(bucket) / minutesPerBucket)),
  };
}

// How many of the failed calls were the supplier's side, and how many the request's own.
export function failureSplit(monitor: Monitor) {
  const upstream = monitor.failure_causes
    .filter((cause) => cause.upstream)
    .reduce((sum, cause) => sum + cause.calls, 0);
  return { upstream, own: Math.max(0, monitor.failures - upstream) };
}

// The share of the calls that switched to another endpoint after a failure, in percent; nothing for no calls.
export const switchRate = (monitor: Monitor) =>
  monitor.calls > 0 ? (monitor.switched_calls / monitor.calls) * 100 : undefined;

export type Range = { from: number; to?: number; share: number };

// The completion times' bins as ranges in seconds, each with its share of the completed calls in percent: bin `below_ms`
// is the upper bound, the one before it the lower, and the last, open one has none.
export function ranges(durations: Monitor["durations"]): Range[] {
  const total = durations.reduce((sum, bin) => sum + bin.calls, 0);
  let from = 0;
  return durations.map((bin) => {
    const range = {
      from,
      to: bin.below_ms === undefined ? undefined : bin.below_ms / 1000,
      share: total > 0 ? (bin.calls / total) * 100 : 0,
    };
    from = range.to ?? from;
    return range;
  });
}
