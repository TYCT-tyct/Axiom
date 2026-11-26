import { expect, it } from "vitest";
import { type HealthInput, healthOf, rateTone } from "./health";

const quiet: HealthInput = { lines: [], suppliers: [], unresolvedAlerts: 0, credentials: [] };
const line = (calls: number, failures: number) => ({ supplier: "s", group: "g", calls, failures });
const points = (input: Partial<HealthInput>) => 100 - healthOf({ ...quiet, ...input }).score;

it("takes nothing off a quiet platform", () => {
  expect(healthOf(quiet)).toEqual({ score: 100, deductions: [], verdict: "ok" });
});

// 97% and 99% themselves are not under: the rule is "below".
it.each([
  { case: "99% exactly", calls: 100, failures: 1, want: 0 },
  { case: "just under 99%", calls: 1000, failures: 11, want: 4 },
  { case: "97% exactly", calls: 100, failures: 3, want: 4 },
  { case: "just under 97%", calls: 1000, failures: 31, want: 12 },
  { case: "no calls is no rate", calls: 0, failures: 0, want: 0 },
])("a line at $case loses $want", ({ calls, failures, want }) => {
  expect(points({ lines: [line(calls, failures)] })).toBe(want);
});

it("reads a rate in the colour its line is scored by", () => {
  expect([line(100, 0), line(1000, 11), line(1000, 31), line(0, 0)].map(rateTone)).toEqual(["ok", "warn", "bad", "ok"]);
});

it.each([
  { case: "4 days", status: "active", days: 4, want: 6 },
  { case: "5 days", status: "active", days: 5, want: 0 },
  { case: "a balance that is low", status: "low_balance", days: 1, want: 6 },
  { case: "a paused supplier", status: "paused", days: 1, want: 0 },
  { case: "no days to go by", status: "active", days: undefined, want: 0 },
])("a supplier with $case left loses $want", ({ status, days, want }) => {
  expect(points({ suppliers: [{ id: "x", status, balanceDays: days }] })).toBe(want);
});

it.each([
  { alerts: 1, want: 2 },
  { alerts: 5, want: 10 },
  { alerts: 9, want: 10 },
])("$alerts unresolved alerts lose $want", ({ alerts, want }) => {
  expect(points({ unresolvedAlerts: alerts })).toBe(want);
});

it("takes 3 once however many credentials are at their ceiling", () => {
  const at = { in_flight: 4, max_concurrency: 4 };
  expect(points({ credentials: [at, at, { in_flight: 1, max_concurrency: 4 }] })).toBe(3);
  expect(points({ credentials: [{ in_flight: 3, max_concurrency: 4 }, { max_concurrency: 4 }] })).toBe(0);
});

it("adds the causes up, names each, and reads the score in three bands", () => {
  const worst = healthOf({
    lines: [line(1000, 100), line(1000, 20)],
    suppliers: [{ id: "x", status: "active", balanceDays: 2 }],
    unresolvedAlerts: 2,
    credentials: [{ in_flight: 2, max_concurrency: 2 }],
  });
  expect(worst.deductions.map(({ points, cause }) => [cause.kind, points])).toEqual([
    ["line", 12],
    ["line", 4],
    ["balance", 6],
    ["alerts", 4],
    ["concurrency", 3],
  ]);
  expect(worst.score).toBe(71);
  expect(worst.verdict).toBe("watch");
  expect(healthOf({ ...quiet, unresolvedAlerts: 5 }).verdict).toBe("ok");
  expect(healthOf({ ...quiet, lines: [line(10, 10), line(10, 10)], unresolvedAlerts: 5 })).toMatchObject({
    score: 66,
    verdict: "bad",
  });
});

it("never goes below zero", () => {
  const lines = Array.from({ length: 20 }, () => line(100, 100));
  expect(healthOf({ ...quiet, lines }).score).toBe(0);
});
