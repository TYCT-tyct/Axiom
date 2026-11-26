import { expect, it } from "vitest";
import { failureSplit, type Monitor, perMinute, ranges, switchRate } from "./monitor";

const hour = (at: number, calls: number) => ({
  at: `2026-11-26T${String(at).padStart(2, "0")}:00:00Z`,
  calls,
  failures: 0,
  tokens: 0,
});

const monitor = (over: Partial<Monitor>): Monitor => ({
  window: "24h",
  since: "2026-11-25T14:00:00Z",
  until: "2026-11-26T14:00:00Z",
  bucket_seconds: 3600,
  buckets: [],
  calls: 0,
  failures: 0,
  tokens: 0,
  switched_calls: 0,
  first_token_ms: {},
  total_ms: {},
  durations: [],
  failure_causes: [],
  lines: [],
  ...over,
});

it("averages over the window's minutes and takes the busiest bucket as per minute", () => {
  const day = monitor({ calls: 14_400, buckets: [hour(1, 1200), hour(2, 6000), hour(3, 0)] });
  expect(perMinute(day, (bucket) => bucket.calls, day.calls)).toEqual({ average: 10, peak: 100 });
  const last = monitor({
    window: "1h",
    since: "2026-11-26T13:00:00Z",
    until: "2026-11-26T14:00:00Z",
    bucket_seconds: 60,
    calls: 120,
    buckets: [hour(1, 3), hour(2, 9)],
  });
  expect(perMinute(last, (bucket) => bucket.calls, last.calls)).toEqual({ average: 2, peak: 9 });
});

it("splits the failures into the supplier's side and the request's own", () => {
  const causes = [
    { error_kind: "upstream_error", upstream: true, calls: 6 },
    { error_kind: "timeout", upstream: true, calls: 2 },
    { error_kind: "invalid_request", upstream: false, calls: 3 },
  ];
  expect(failureSplit(monitor({ failures: 11, failure_causes: causes }))).toEqual({ upstream: 8, own: 3 });
  expect(failureSplit(monitor({ failures: 0 }))).toEqual({ upstream: 0, own: 0 });
});

it("reads the switch rate of the calls, and none for no calls", () => {
  expect(switchRate(monitor({ calls: 200, switched_calls: 5 }))).toBe(2.5);
  expect(switchRate(monitor({}))).toBeUndefined();
});

it("turns the bins into ranges of seconds with their share, the last one open", () => {
  const bins = [
    { below_ms: 1000, calls: 20 },
    { below_ms: 2000, calls: 60 },
    { below_ms: 5000, calls: 15 },
    { calls: 5 },
  ];
  expect(ranges(bins)).toEqual([
    { from: 0, to: 1, share: 20 },
    { from: 1, to: 2, share: 60 },
    { from: 2, to: 5, share: 15 },
    { from: 5, to: undefined, share: 5 },
  ]);
  expect(ranges([{ below_ms: 1000, calls: 0 }])).toEqual([{ from: 0, to: 1, share: 0 }]);
});
