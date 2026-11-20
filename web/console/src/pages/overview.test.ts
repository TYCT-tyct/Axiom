import type { Schemas } from "@axiom/api";
import type { TFunction } from "i18next";
import { describe, expect, it } from "vitest";
import {
  barRows,
  chartLabels,
  chartStacks,
  dayStacks,
  modelStacks,
  spendChange,
  statisticsWindows,
} from "./overview-chart";
import {
  dailyRows,
  dailyTotals,
  modelRowsOf,
  monthWindow,
  rankedModels,
  requestRowsOf,
  splitMoney,
  sumTotals,
} from "./overview-data";

type Money = Schemas["Money"];
type Call = Schemas["Call"];
const t = ((key: string) => key) as unknown as TFunction;
const format = {
  money: ({ amount }: Money) => `$${amount}`,
  number: (value: number | bigint) => String(value),
  hour: () => "08:23",
  monthLabel: (month: string) => month,
  monthName: (month: number) => `M${month}`,
  dateTime: () => "full time",
};
const money = (amount: string): Money => ({ amount, currency: "USD" });
const aggregate = (bucket: string, calls: string, amount: string, currency = "USD"): Schemas["CallAggregate"] => ({
  bucket,
  calls,
  price: { amount, currency },
});

describe("the month the overview covers", () => {
  it("is the UTC calendar month with the days it has", () => {
    expect(monthWindow(new Date("2026-02-10T23:30:00Z"))).toMatchObject({
      since: "2026-02-01T00:00:00.000Z",
      month: "2026-02",
      monthNumber: 2,
      days: 28,
    });
    expect(monthWindow(new Date("2026-10-24T10:00:00+08:00")).days).toBe(31);
  });

  // The chart's month and both sides of the comparison run to the end of the current hour, the same number of hours
  // from each month's start, cut to last month's length when it is the shorter.
  it.each([
    {
      now: "2026-10-24T02:30:00Z",
      month: { since: "2026-10-01T00:00:00.000Z", until: "2026-10-24T03:00:00.000Z" },
      comparison: {
        month: { since: "2026-10-01T00:00:00.000Z", until: "2026-10-24T03:00:00.000Z" },
        lastMonth: { since: "2026-09-01T00:00:00.000Z", until: "2026-09-24T03:00:00.000Z" },
      },
      lastMonthNumber: 9,
    },
    {
      // last month is the shorter: both sides are its 672 hours
      now: "2026-03-31T05:00:00Z",
      month: { since: "2026-03-01T00:00:00.000Z", until: "2026-03-31T06:00:00.000Z" },
      comparison: {
        month: { since: "2026-03-01T00:00:00.000Z", until: "2026-03-29T00:00:00.000Z" },
        lastMonth: { since: "2026-02-01T00:00:00.000Z", until: "2026-03-01T00:00:00.000Z" },
      },
      lastMonthNumber: 2,
    },
    {
      // exactly on the hour, the hour that has just begun is counted on both sides
      now: "2026-10-01T01:00:00Z",
      month: { since: "2026-10-01T00:00:00.000Z", until: "2026-10-01T02:00:00.000Z" },
      comparison: {
        month: { since: "2026-10-01T00:00:00.000Z", until: "2026-10-01T02:00:00.000Z" },
        lastMonth: { since: "2026-09-01T00:00:00.000Z", until: "2026-09-01T02:00:00.000Z" },
      },
      lastMonthNumber: 9,
    },
    {
      // the month's first hour: one hour on each side
      now: "2026-01-01T00:00:00Z",
      month: { since: "2026-01-01T00:00:00.000Z", until: "2026-01-01T01:00:00.000Z" },
      comparison: {
        month: { since: "2026-01-01T00:00:00.000Z", until: "2026-01-01T01:00:00.000Z" },
        lastMonth: { since: "2025-12-01T00:00:00.000Z", until: "2025-12-01T01:00:00.000Z" },
      },
      lastMonthNumber: 12,
    },
  ])("puts both months on the same span of whole hours at $now", ({ now, ...want }) => {
    expect(statisticsWindows(monthWindow(new Date(now)))).toEqual(want);
  });
});

describe("the figures", () => {
  it("counts only the account's currency and keeps the exact sums", () => {
    const rows = [
      aggregate("2026-10-01", "2", "1500000000"),
      aggregate("2026-10-02", "1", "2500000000"),
      aggregate("2026-10-02", "9", "999", "EUR"),
    ];
    expect(sumTotals(rows, "USD")).toEqual({ calls: 3n, amount: 4000000000n });
    expect(sumTotals(rows, "EUR")).toEqual({ calls: 9n, amount: 999n });
    expect(sumTotals(undefined, "USD")).toBeUndefined();
  });

  it("buckets a day's calls under the day of the month", () => {
    const daily = dailyTotals([aggregate("2026-10-01", "2", "15"), aggregate("2026-10-02", "1", "25")], "USD");
    expect([...(daily ?? [])]).toEqual([
      [1, 15n],
      [2, 25n],
    ]);
  });

  it("keeps a day and a model whose calls cost nothing", () => {
    const rows = [aggregate("2026-10-01", "5", "0")];
    expect(dailyRows(rows, "USD")).toEqual([{ day: 1, calls: 5n, amount: 0n }]);
    const models = modelRowsOf(rankedModels(rows, "USD"), "USD", "", format, t);
    expect(models).toHaveLength(1);
    expect(models[0]).toMatchObject({ f3: "2026-10-01", f5: "0%", f7: "$0" });
  });

  it("writes an error's reason where a narrow row can still read it, and its time or date in the board's column", () => {
    const at = new Date(2026, 9, 24, 8, 23);
    const call = {
      id: "call-1",
      at: at.toISOString(),
      model: "deepseek/x",
      key_id: "key-1",
      error_kind: "upstream_timeout",
      price: money("0"),
    } as Call;
    const [row] = requestRowsOf([call], "", new Map(), format, t, new Date(2026, 9, 24, 23, 59));
    expect(row).toMatchObject({
      f2: "08:23",
      f7: "upstream_timeout · deepseek/x",
      f9: "—",
      f10: "",
      f11: "calls.failed",
    });
    const [older] = requestRowsOf([call], "", new Map(), format, t, new Date(2026, 9, 25, 0, 1));
    expect(older).toMatchObject({ f2: "10/24" });
  });

  it("splits the currency and integer digits from the fraction the board sets smaller", () => {
    expect(splitMoney("US$3.141592653")).toEqual(["US$3", ".141592653"]);
    expect(splitMoney("-US$9,223")).toEqual(["-US$9,223", ""]);
  });

  it("draws each day's bar in its slot against the month's largest day, as the board does", () => {
    const rows = barRows(
      dayStacks(
        new Map([
          [1, 10n],
          [2, 5n],
          [3, 0n],
        ]),
      ),
      31,
    );
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ key: "1-0", f0: "6.5", f1: "24.8", f3: "164.0", f5: "fill: var(--c0);" });
    expect(rows[1]).toMatchObject({ key: "2-0", f0: "38.7", f1: "107.4", f3: "81.4" });
    expect(barRows(dayStacks(new Map([[1, 0n]])), 31)).toEqual([]);
  });

  it("stacks a day's models from the bottom in the model card's order and colours, a gap apart", () => {
    const bucket = (at: string, group: string, amount: string): Schemas["UsageBucket"] => ({
      at,
      group,
      metrics: { spend: { amount, currency: "USD" }, requests: "1" },
    });
    const stacks = modelStacks(
      [
        bucket("2026-10-01T00:00:00Z", "b", "300"),
        bucket("2026-10-01T00:00:00Z", "a", "600"),
        bucket("2026-10-01T00:00:00Z", "unranked", "100"),
        bucket("2026-10-02T00:00:00Z", "a", "1"),
        { at: "2026-10-02T00:00:00Z", group: "a", metrics: { spend: { amount: "999", currency: "EUR" } } },
      ],
      "USD",
      new Map([
        ["a", 0],
        ["gone", 1],
        ["b", 2],
      ]),
    );
    expect(stacks.get(1)).toEqual([
      { amount: 600n, fill: "var(--c0)" },
      { amount: 300n, fill: "var(--c1)" },
      { amount: 100n, fill: "var(--c3)" },
    ]);
    const rows = barRows(stacks, 31);
    expect(rows.map((row) => [row.f1, row.f3])).toEqual([
      ["90.9", "97.9"],
      ["41.3", "48.4"],
      ["24.8", "15.3"],
      // a day too small to clear the gap still shows
      ["189.8", "1.0"],
    ]);
  });

  it("draws the split by model only while it accounts for each day's total, and one total bar otherwise", () => {
    const statistics = (backfill: boolean): Schemas["UsageStatistics"] => ({
      backfill_complete: backfill,
      buckets: [
        { at: "2026-10-01T00:00:00Z", group: "a", metrics: { spend: { amount: "600", currency: "USD" } } },
        { at: "2026-10-01T00:00:00Z", group: "b", metrics: { spend: { amount: "300", currency: "USD" } } },
      ],
      totals: [],
    });
    const order = new Map([
      ["a", 0],
      ["b", 1],
    ]);
    const daily = new Map([[1, 900n]]);
    // two models on one day: two segments at the same x
    const rows = barRows(chartStacks(daily, statistics(true), "USD", order), 31);
    expect(rows.map((row) => [row.f0, row.f5])).toEqual([
      ["6.5", "fill: var(--c0);"],
      ["6.5", "fill: var(--c4);"],
    ]);
    const total = [{ amount: 900n, fill: "var(--c0)" }];
    for (const [stats, days] of [
      [statistics(false), daily], // history still backfilling
      [statistics(true), new Map([[1, 1000n]])], // a call the rollups have not taken in yet
      [statistics(true), new Map([...daily, [2, 5n]])], // a day the rollups do not have
      [undefined, daily], // no split, or it failed
    ] as const) {
      expect(chartStacks(days, stats, "USD", order)?.get(1)).toEqual(
        days === daily ? total : [{ amount: days.get(1), fill: "var(--c0)" }],
      );
    }
    expect(chartStacks(undefined, statistics(true), "USD", order)).toBeUndefined();
  });

  it("compares the month's spend with last month's same span only while it is the card's figure", () => {
    const statistics = (amount?: string, backfill = true): Schemas["UsageStatistics"] => ({
      backfill_complete: backfill,
      buckets: [],
      totals: amount ? [{ spend: { amount, currency: "USD" } }, { spend: { amount: "1", currency: "EUR" } }] : [],
    });
    const card = (amount: string) => money(amount);
    expect(spendChange(statistics("1230"), statistics("1000"), card("1230"))).toBe("+23%");
    expect(spendChange(statistics("950"), statistics("1000"), card("950"))).toBe("−5.0%");
    // the rollups have not caught up with a call the card already counts
    expect(spendChange(statistics("1000"), statistics("1000"), card("1230"))).toBeUndefined();
    expect(spendChange(statistics(), statistics("1000"), card("10"))).toBeUndefined();
    // the card in another currency than the side compared
    expect(spendChange(statistics("1230"), statistics("1000"), { amount: "1230", currency: "EUR" })).toBeUndefined();
    expect(spendChange(undefined, statistics("1000"), card("1230"))).toBeUndefined();
    expect(spendChange(statistics("1230"), statistics(), card("1230"))).toBeUndefined();
    expect(spendChange(statistics("1230"), statistics("1000", false), card("1230"))).toBeUndefined();
    expect(spendChange(statistics("1230"), statistics("1000"), undefined)).toBeUndefined();
  });

  // The review's case: at UTC Oct 2 10:30 the month's only spend is 10 at 10:15 and last month's only spend is 10 on
  // Sep 1. Both sides run to 11:00 on their second day and hold the 10, which is the card's figure: no −100%.
  it("counts a spend of the current hour on both sides, as the card does", () => {
    const { comparison } = statisticsWindows(monthWindow(new Date("2026-10-02T10:30:00Z")));
    const inside = (at: string, window: { since: string; until: string }) =>
      Date.parse(window.since) <= Date.parse(at) && Date.parse(at) < Date.parse(window.until);
    const side = (at: string, window: { since: string; until: string }): Schemas["UsageStatistics"] => ({
      backfill_complete: true,
      buckets: [],
      totals: inside(at, window) ? [{ spend: { amount: "10000000000", currency: "USD" } }] : [],
    });
    const now = side("2026-10-02T10:15:00Z", comparison.month);
    const before = side("2026-09-01T10:15:00Z", comparison.lastMonth);
    expect(spendChange(now, before, money("10000000000"))).toBe("+0.0%");
    // the same hour, while the rollups have not taken in the 10 the card already shows
    expect(spendChange({ ...now, totals: [] }, before, money("10000000000"))).toBeUndefined();
  });

  it("labels the chart with its dates, or with the card's sentence when it has none", () => {
    expect(chartLabels(31, 10, "")[0]).toEqual({ text: "10/1", left: "left: 1.61%;" });
    expect(chartLabels(31, 10, "No calls this month")[0]?.text).toBe("No calls this month");
    expect(chartLabels(31, 10, "")).toHaveLength(5);
  });
});
