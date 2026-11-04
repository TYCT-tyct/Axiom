import { expect, it } from "vitest";
import {
  formatTargetRate,
  legacyTargetMetrics,
  parseTargetMoney,
  parseTargetRate,
  readTargets,
  targetMonth,
  targetsEmpty,
  targetTone,
} from "./targets";

const problem = { count: "count", money: "money", rate: "rate" };

it("takes the UTC month of the visit", () => {
  expect(targetMonth(new Date("2026-11-30T23:30:00Z"))).toBe("2026-11");
  expect(targetMonth(new Date("2026-12-01T00:30:00Z"))).toBe("2026-12");
});

it("calls a set with no metric empty", () => {
  expect(targetsEmpty({})).toBe(true);
  expect(targetsEmpty({ calls: { target: 0 } })).toBe(false);
  expect(targetsEmpty({ cost: { target: { amount: "0", currency: "USD" } } })).toBe(false);
  expect(targetsEmpty({ paid_users: { target: 0 } })).toBe(false);
  expect(targetsEmpty({ monthly_revenue: { target: { amount: "0", currency: "USD" } } })).toBe(false);
  expect(targetsEmpty({ gross_margin: { target: 0 } })).toBe(false);
  expect(targetsEmpty({ retention: { target: 0 } })).toBe(false);
});

it.each([
  { metric: "calls" as const, sign: 1 as const, tone: "ahead" },
  { metric: "calls" as const, sign: -1 as const, tone: "behind" },
  { metric: "revenue" as const, sign: 1 as const, tone: "ahead" },
  { metric: "paid_users" as const, sign: 1 as const, tone: "ahead" },
  { metric: "monthly_revenue" as const, sign: -1 as const, tone: "behind" },
  { metric: "gross_margin" as const, sign: 1 as const, tone: "ahead" },
  { metric: "retention" as const, sign: -1 as const, tone: "behind" },
  { metric: "errors" as const, sign: 1 as const, tone: "over" },
  { metric: "errors" as const, sign: -1 as const, tone: "under" },
  { metric: "cost" as const, sign: 1 as const, tone: "over" },
  { metric: "cost" as const, sign: -1 as const, tone: "under" },
  { metric: "calls" as const, sign: 0 as const, tone: "at" },
])("$metric with a $sign gap is $tone", ({ metric, sign, tone }) => {
  expect(targetTone(metric, sign)).toBe(tone);
});

it("keeps a target amount above int64 and up to 38 digits", () => {
  expect(parseTargetMoney("9223372036.854775808")).toBe("9223372036854775808");
  expect(parseTargetMoney("10000000000")).toBe("10000000000000000000");
  expect(parseTargetMoney("99999999999999999999999999999.999999999")).toBe("99999999999999999999999999999999999999");
  expect(parseTargetMoney("999999999999999999999999999999999999999")).toBeNull();
  expect(parseTargetMoney("1.1234567890")).toBeNull();
  expect(readTargets({ revenue: "9223372036.854775808" }, "USD", problem)).toEqual({
    revenue: { amount: "9223372036854775808", currency: "USD" },
  });
  expect(readTargets({ cost: "999999999999999999999999999999999999999" }, "USD", problem)).toEqual({
    problem: "money",
  });
});

it.each([
  ["0", 0],
  ["35", 3500],
  ["35.5", 3550],
  ["35.05", 3505],
  ["100", 10000],
  ["100.00", 10000],
])("parses %s percent as %i basis points", (text, basisPoints) => {
  expect(parseTargetRate(text)).toBe(basisPoints);
});

it.each(["", "100.01", "-1", "35.055", "35%", "1,000"])("refuses %s as a rate", (text) => {
  expect(parseTargetRate(text)).toBeNull();
});

it.each([
  [0, "0%"],
  [3500, "35%"],
  [3550, "35.5%"],
  [3505, "35.05%"],
  [10000, "100%"],
  [-550, "-5.5%"],
])("formats %i basis points as %s", (basisPoints, text) => {
  expect(formatTargetRate(basisPoints)).toBe(text);
});

it("reads the form as the month's whole set, leaving empty fields out", () => {
  expect(
    readTargets(
      {
        calls: "3100",
        errors: "0",
        revenue: "1200.50",
        cost: "",
        paid_users: "120",
        monthly_revenue: "5000",
        gross_margin: "35.05",
        retention: "80",
      },
      "USD",
      problem,
    ),
  ).toEqual({
    calls: 3100,
    errors: 0,
    revenue: { amount: "1200500000000", currency: "USD" },
    paid_users: 120,
    monthly_revenue: { amount: "5000000000000", currency: "USD" },
    gross_margin: 3505,
    retention: 8000,
  });
  expect(readTargets({ calls: "", errors: " ", revenue: "", cost: "" }, "USD", problem)).toEqual({});
});

it("does not send a commercial field to a node that does not store it", () => {
  expect(
    readTargets(
      { calls: "1", paid_users: "2", monthly_revenue: "3", gross_margin: "4", retention: "5" },
      "USD",
      problem,
      legacyTargetMetrics,
    ),
  ).toEqual({ calls: 1 });
});

it.each([
  { field: "calls", value: "1.5", want: "count" },
  { field: "errors", value: "-1", want: "count" },
  { field: "calls", value: "1000000000000001", want: "count" },
  { field: "paid_users", value: "-1", want: "count" },
  { field: "revenue", value: "1,200", want: "money" },
  { field: "cost", value: "-1", want: "money" },
  { field: "revenue", value: "1.1234567890", want: "money" },
  { field: "monthly_revenue", value: "-1", want: "money" },
  { field: "gross_margin", value: "100.01", want: "rate" },
  { field: "retention", value: "35.055", want: "rate" },
])("refuses $field = $value as $want", ({ field, value, want }) => {
  expect(readTargets({ [field]: value }, "USD", problem)).toEqual({
    problem: want,
  });
});
