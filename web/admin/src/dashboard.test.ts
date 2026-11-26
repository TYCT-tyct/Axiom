import { expect, it } from "vitest";
import { byUrgency, change, failureRate, margin, mixOf, shift, tokenParts, tokenTotal, trendOf } from "./dashboard";

const usd = (amount: string) => ({ amount, currency: "USD" });

it.each([
  { now: 112, before: 100, want: 12 },
  { now: 96.1, before: 100, want: -3.9 },
  { now: 5, before: 0, want: undefined },
  { now: undefined, before: 5, want: undefined },
  { now: 5, before: undefined, want: undefined },
])("a move from $before to $now is $want percent", ({ now, before, want }) => {
  expect(change(now, before)).toBe(want);
});

it("moves a rate by its own unit and says nothing without both ends", () => {
  expect(shift(2.35, 2.0, 2)).toBe(0.35);
  expect(shift(0.43, 0.52, 2)).toBe(-0.09);
  expect(shift(1, undefined, 2)).toBeUndefined();
  expect(failureRate({ calls: 200, errors: 3 })).toBe(1.5);
  expect(failureRate({ calls: 0, errors: 0 })).toBeUndefined();
});

// A margin is a subtraction of exact amounts; with a call whose cost is unknown it would be bigger than it is.
it.each([
  { case: "the difference", overview: { revenue: usd("1500000000"), cost: usd("400000000") }, want: usd("1100000000") },
  { case: "a loss", overview: { revenue: usd("1"), cost: usd("3") }, want: usd("-2") },
  {
    case: "digits past 64 bits",
    overview: { revenue: usd("30000000000000000000"), cost: usd("1") },
    want: usd("29999999999999999999"),
  },
  {
    case: "no cost for some calls",
    overview: { revenue: usd("5"), cost: usd("1"), unknown_cost_calls: 2 },
    want: undefined,
  },
  { case: "no revenue", overview: { cost: usd("1") }, want: undefined },
  { case: "two currencies", overview: { revenue: usd("5"), cost: { amount: "1", currency: "CNY" } }, want: undefined },
])("the margin is $case", ({ overview, want }) => {
  expect(margin(overview)).toEqual(want);
});

it("counts every kind of token once and draws the cache writes as input", () => {
  const tokens = { input: 100, cache_read: 40, cache_write: 10, output: 50 };
  expect(tokenTotal(tokens)).toBe(200);
  expect(tokenParts(tokens)).toEqual({ output: 50, input: 110, hit: 40 });
  expect(tokenTotal(undefined)).toBeUndefined();
});

it("leaves out the days that lack a figure and draws no trend from fewer than two", () => {
  const days = [{ calls: 1 }, { calls: 3 }, {}, { calls: 2 }] as unknown as Parameters<typeof trendOf>[0];
  expect(trendOf(days, (day) => day.calls)).toEqual([1, 3, 2]);
  expect(trendOf(days?.slice(0, 1), (day) => day.calls)).toEqual([]);
  expect(trendOf(undefined, (day) => day.calls)).toEqual([]);
});

it("shares each measure among the listed models and the rest of the window", () => {
  const overview = {
    calls: 100,
    revenue: usd("10000000000"),
    tokens: { input: 600, cache_read: 0, cache_write: 0, output: 400 },
    top_models: [
      { model: "a", calls: 50, tokens: 500, revenue: usd("6000000000") },
      { model: "b", calls: 30, tokens: 300, revenue: usd("2000000000") },
      { model: "idle", calls: 0, tokens: 0 },
    ],
  };
  expect(mixOf(overview, "calls")).toEqual([
    { model: "a", value: 50 },
    { model: "b", value: 30 },
    { model: undefined, value: 20 },
  ]);
  expect(mixOf(overview, "tokens").map((part) => part.value)).toEqual([500, 300, 200]);
  expect(mixOf(overview, "revenue").map((part) => part.value)).toEqual([6, 2, 2]);
  // With nothing beyond the listed models there is no "others", and with no total there is none to take from.
  expect(mixOf({ ...overview, calls: 80 }, "calls")).toHaveLength(2);
  expect(mixOf({ top_models: overview.top_models }, "calls")).toHaveLength(2);
});

it("puts what is failing first and keeps the order of the equally pressing", () => {
  const items = [
    { name: "refunds", rank: 3 as const },
    { name: "approvals", rank: 1 as const },
    { name: "warn", rank: 2 as const },
    { name: "fail", rank: 0 as const },
    { name: "mappings", rank: 3 as const },
  ];
  expect(byUrgency(items).map((item) => item.name)).toEqual(["fail", "approvals", "warn", "refunds", "mappings"]);
});
