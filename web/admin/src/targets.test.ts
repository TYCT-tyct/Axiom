import { expect, it } from "vitest";
import { readTargets, targetMonth, targetsEmpty, targetTone } from "./targets";

it("takes the UTC month of the visit", () => {
  expect(targetMonth(new Date("2026-11-30T23:30:00Z"))).toBe("2026-11");
  expect(targetMonth(new Date("2026-12-01T00:30:00Z"))).toBe("2026-12");
});

it("calls a set with no metric empty", () => {
  expect(targetsEmpty({})).toBe(true);
  expect(targetsEmpty({ calls: { target: 0 } })).toBe(false);
  expect(targetsEmpty({ cost: { target: { amount: "0", currency: "USD" } } })).toBe(false);
});

it.each([
  { metric: "calls" as const, sign: 1 as const, tone: "ahead" },
  { metric: "calls" as const, sign: -1 as const, tone: "behind" },
  { metric: "revenue" as const, sign: 1 as const, tone: "ahead" },
  { metric: "errors" as const, sign: 1 as const, tone: "over" },
  { metric: "errors" as const, sign: -1 as const, tone: "under" },
  { metric: "cost" as const, sign: 1 as const, tone: "over" },
  { metric: "cost" as const, sign: -1 as const, tone: "under" },
  { metric: "calls" as const, sign: 0 as const, tone: "at" },
])("$metric with a $sign gap is $tone", ({ metric, sign, tone }) => {
  expect(targetTone(metric, sign)).toBe(tone);
});

it("reads the form as the month's whole set, leaving empty fields out", () => {
  expect(
    readTargets({ calls: "3100", errors: "0", revenue: "1200.50", cost: "" }, "USD", {
      count: "count",
      money: "money",
    }),
  ).toEqual({
    calls: 3100,
    errors: 0,
    revenue: { amount: "1200500000000", currency: "USD" },
  });
  expect(
    readTargets({ calls: "", errors: " ", revenue: "", cost: "" }, "USD", { count: "count", money: "money" }),
  ).toEqual({});
});

it.each([
  { field: "calls", value: "1.5", want: "count" },
  { field: "errors", value: "-1", want: "count" },
  { field: "calls", value: "1000000000000001", want: "count" },
  { field: "revenue", value: "1,200", want: "money" },
  { field: "cost", value: "-1", want: "money" },
  { field: "revenue", value: "1.1234567890", want: "money" },
])("refuses $field = $value as $want", ({ field, value, want }) => {
  expect(readTargets({ [field]: value }, "USD", { count: "count", money: "money" })).toEqual({
    problem: want,
  });
});
