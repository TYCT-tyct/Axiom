import { describe, expect, it } from "vitest";
import { buildChartView, buildKpiView, type Formatters, type UsageViewLabels } from "./usage-chart-view";
import {
  aggregateGroups,
  buildBucketSlots,
  buildUsageCsv,
  compareUsageTotals,
  currenciesOfUsage,
  defaultGranularity,
  formatAvgPerRequest,
  formatBigDeltaPercent,
  formatCompactTokens,
  formatSpendPerMillion,
  hasCurrencyBuckets,
  hasUsageStatisticsPayload,
  niceAxisScale,
  presetDaySpan,
  selectCurrency,
  sumGroupTotals,
  toUsageTotals,
  utcDayOf,
  validateDaySpan,
  validateUsageRange,
  windowFromDays,
} from "./usage-model";
import { buildCalendarView, buildMoversView, buildTableView, buildTokenView } from "./usage-view";

const fmt: Formatters = {
  number: (n) => BigInt(n).toLocaleString("en-US"),
  money: ({ amount, currency }) => {
    const v = BigInt(amount);
    const whole = v / 1_000_000_000n;
    const frac9 = (v % 1_000_000_000n).toString().padStart(9, "0");
    const tail = frac9.slice(2).replace(/0+$/, "");
    const sym = currency === "CNY" ? "¥" : "$";
    return `${sym}${whole}.${frac9.slice(0, 2)}${tail}`;
  },
  day: (d) => `${d.getUTCMonth() + 1}/${d.getUTCDate()}`,
  dayFull: (d) => `${d.getUTCFullYear()}-${d.getUTCMonth() + 1}-${d.getUTCDate()}`,
  hour: (d) => `${String(d.getUTCHours()).padStart(2, "0")}:00`,
};

const labels: UsageViewLabels = {
  newLabel: "New",
  unknownGroup: "Unassigned",
  unitByGranularity: { hour: "Hourly ", day: "Daily ", week: "Weekly " },
  metricLabels: { spend: "spend", requests: "requests", tokens: "tokens" },
  metricTitles: { spend: "spend", requests: "requests", tokens: "tokens" },
  groupByLabels: { model: "By model", key: "By key", workspace: "By workspace" },
  groupColLabels: { model: "Model", key: "Key", workspace: "Workspace" },
  granularityLabels: { hour: "Hourly", day: "Daily", week: "Weekly" },
  tableCols: { spend: "Spend", share: "Share", requests: "Requests", input: "Input", output: "Output", avg: "Avg" },
  tokenRows: ["Input", "Output", "Cache read", "Cache write"],
  ppDelta: (s, v) => `${s}${v} pp`,
  successOnly: (r) => `${r} succeeded`,
  deltaWithSuccess: (d, r) => `${d} · ${r} succeeded`,
  cacheHitSummary: (r, c) => `Hit ${r} across ${c}`,
  cacheHitZeroDenom: (c) => `Eligible ${c}, 0 physical input`,
  cacheHitNone: "No cache calls",
  noMovers: "No prior usage",
  moversLoading: "Loading movers",
  moversFailed: "Failed movers",
  moversMissingPrev: "Missing prev",
  noMetricChange: "No metric change",
  chartAria: (t, b, tot) => `${t} ${b} ${tot}`,
};

const baseView = {
  names: new Map<string, string>(),
  colors: new Map<string, string>(),
  format: fmt,
  currency: "USD",
  labels,
};
const mkBucket = (
  at: string,
  group: string,
  spend: string,
  requests: string,
  tokens?: { input?: string; output?: string },
) => ({
  at,
  group,
  metrics: { spend: { amount: spend, currency: "USD" }, requests, ...(tokens ? { tokens } : {}) },
});

describe("usage range limits", () => {
  it("enforces 7-day limit for hour granularity and 90-day limit for day/week", () => {
    const day0 = utcDayOf(new Date("2026-10-01T00:00:00Z"));
    expect(validateDaySpan(day0, day0 + 6, "hour")).toEqual({ ok: true, days: 7, limitDays: 7 });
    expect(validateDaySpan(day0, day0 + 7, "hour")).toEqual({ ok: false, days: 8, limitDays: 7 });
    expect(validateDaySpan(day0, day0 + 89, "day")).toEqual({ ok: true, days: 90, limitDays: 90 });
    expect(validateDaySpan(day0, day0 + 90, "day")).toEqual({ ok: false, days: 91, limitDays: 90 });
    expect(validateDaySpan(day0, day0 + 90, "week")).toEqual({ ok: false, days: 91, limitDays: 90 });
  });

  it("rejects non-whole-hour bounds, reversed ranges, and over-limit windows", () => {
    expect(validateUsageRange("2026-10-01T00:30:00Z", "2026-10-02T00:00:00Z", "day")).toMatchObject({
      ok: false,
      reason: "hour",
    });
    expect(validateUsageRange("2026-10-02T00:00:00Z", "2026-10-02T00:00:00Z", "day")).toMatchObject({
      ok: false,
      reason: "order",
    });
    const overHour = windowFromDays(100, 107);
    expect(validateUsageRange(overHour.since, overHour.until, "hour")).toEqual({
      ok: false,
      reason: "limit",
      days: 8,
      limitDays: 7,
    });
    const maxWeek = windowFromDays(100, 189);
    expect(validateUsageRange(maxWeek.since, maxWeek.until, "week")).toMatchObject({
      ok: true,
      days: 90,
      hours: 2160,
    });
  });

  it("resolves presets cleanly in UTC with whole-hour bounds", () => {
    const now = new Date("2026-10-24T02:15:00Z");
    const today = presetDaySpan("today", now);
    expect(windowFromDays(today.startDay, today.endDay)).toEqual({
      since: "2026-10-24T00:00:00.000Z",
      until: "2026-10-25T00:00:00.000Z",
    });
    expect(defaultGranularity("today", 1)).toBe("hour");
    expect(defaultGranularity("month", 24)).toBe("day");
    expect(defaultGranularity("custom", 90)).toBe("week");
  });
});

describe("group sum equals total invariant", () => {
  it("preserves exact BigInt sum === totals when grouped including unnamed groups", () => {
    const currency = "USD";
    const buckets = [
      {
        at: "2026-10-14T00:00:00Z",
        group: "g-1",
        metrics: {
          spend: { amount: "12345678901", currency },
          requests: "120",
          successes: "118",
          tokens: { input: "1000000", output: "250000", cache_read: "500000", cache_write: "25000" },
          cache_rate_requests: "100",
        },
      },
      {
        at: "2026-10-15T00:00:00Z",
        group: "g-1",
        metrics: {
          spend: { amount: "500000000", currency },
          requests: "5",
          successes: "5",
          tokens: { input: "50000", output: "10000", cache_read: "0", cache_write: "0" },
          cache_rate_requests: "5",
        },
      },
      {
        at: "2026-10-15T00:00:00Z",
        group: "",
        metrics: {
          spend: { amount: "999999999", currency },
          requests: "10",
          successes: "9",
          tokens: { input: "100000", output: "20000", cache_read: "10000", cache_write: "5000" },
          cache_rate_requests: "10",
        },
      },
      {
        at: "2026-10-15T00:00:00Z",
        group: "other-currency",
        metrics: {
          spend: { amount: "888888888", currency: "CNY" },
          requests: "9",
          successes: "9",
          tokens: { input: "9000", output: "1000", cache_read: "0", cache_write: "0" },
        },
      },
    ];
    const serverTotals = [
      {
        spend: { amount: "13845678900", currency },
        requests: "135",
        successes: "132",
        tokens: { input: "1150000", output: "280000", cache_read: "510000", cache_write: "30000" },
        cache_rate_requests: "115",
        cache_hit_rate: 510000 / 1660000,
        success_rate: 132 / 135,
      },
      {
        spend: { amount: "888888888", currency: "CNY" },
        requests: "9",
        successes: "9",
        tokens: { input: "9000", output: "1000", cache_read: "0", cache_write: "0" },
      },
    ];

    expect(selectCurrency(serverTotals, "USD")).toBe("USD");
    const totals = toUsageTotals(serverTotals, "USD");
    const groups = aggregateGroups(buckets, "USD", "day");
    const groupedSum = sumGroupTotals(groups, "USD");

    expect(totals).not.toBeNull();
    expect(groups).toHaveLength(2);
    expect(groupedSum.spendNano).toBe(totals?.spendNano);
    expect(groupedSum.requests).toBe(totals?.requests);
    expect(groupedSum.successes).toBe(totals?.successes);
    expect(groupedSum.input).toBe(totals?.input);
    expect(groupedSum.output).toBe(totals?.output);
    expect(groupedSum.cacheRead).toBe(totals?.cacheRead);
    expect(groupedSum.cacheWrite).toBe(totals?.cacheWrite);
    expect(groupedSum.tokens).toBe(totals?.tokens);
  });
});

describe("bucket slots, KPI rates, calendar limit guard, and CSV", () => {
  it("aligns week buckets to ISO Monday 00:00Z matching SQL date_trunc", () => {
    const since = Date.parse("2026-10-14T00:00:00Z"); // Wednesday -> Monday 2026-10-12
    const until = Date.parse("2026-10-25T00:00:00Z");
    const slots = buildBucketSlots(since, until, "week");
    expect(slots).toHaveLength(2);
    expect(new Date(slots[0]?.atMs ?? 0).toISOString()).toBe("2026-10-12T00:00:00.000Z");
    expect(new Date(slots[1]?.atMs ?? 0).toISOString()).toBe("2026-10-19T00:00:00.000Z");
  });

  it("formats success_rate, cache_hit_rate, compact tokens, and nice Y ticks", () => {
    const totals = toUsageTotals(
      [
        {
          spend: { amount: "5000000000", currency: "CNY" },
          requests: "200",
          successes: "197",
          tokens: { input: "1500000", output: "500000", cache_read: "500000", cache_write: "0" },
          cache_rate_requests: "150",
          cache_hit_rate: 0.25,
          success_rate: 0.985,
        },
      ],
      "CNY",
    );
    expect(totals).not.toBeNull();
    if (!totals) return;
    const prev = toUsageTotals(
      [
        {
          spend: { amount: "4000000000", currency: "CNY" },
          requests: "100",
          successes: "96",
          tokens: { input: "1000000", output: "500000", cache_read: "500000", cache_write: "0" },
          cache_hit_rate: 0.22,
          success_rate: 0.96,
        },
      ],
      "CNY",
    );
    const kpi = buildKpiView(totals, prev, "CNY", fmt, labels);
    expect(kpi.spendCurrency).toBe("¥");
    expect(kpi.totals0).toBe("5.00");
    expect(kpi.changes0).toBe("+25%");
    expect(kpi.changes1).toBe("+100% · 98.5% succeeded");
    expect(kpi.totals4).toBe("25.0");
    expect(kpi.changes4).toBe("+3.0 pp");
    expect(formatCompactTokens(4_800_000n, fmt.number)).toBe("4.8M");
    expect(formatBigDeltaPercent(5n, 0n, "New")).toBe("New");
    expect(niceAxisScale(14).ticks).toEqual([5, 10, 15]);
    expect(niceAxisScale(1, true).ticks).toEqual([1]);
  });

  it("disables calendar Apply when selected span exceeds active granularity limit", () => {
    const today = utcDayOf(new Date("2026-10-24T00:00:00Z"));
    const cal = buildCalendarView({
      leftYearMonth: 2026 * 12 + 8,
      selStart: today - 9,
      selEnd: today,
      hoverDay: null,
      todayDay: today,
      granularity: "hour",
      language: "en",
      format: fmt,
      labels: {
        quick: ["Y", "W", "M", "90d"],
        pickStart: "Pick",
        pickEnd: (d) => d,
        pickedSpan: (s, days) => `${s} (${days})`,
        limitError: (days, limit) => `Over limit: ${days} > ${limit}`,
      },
    });
    expect(cal.confirmDisabled).toBe(true);
    expect(cal.text.calStatus).toBe("Over limit: 10 > 7");
  });

  it("produces empty bar list when empty and escapes formula values after whitespace in CSV", () => {
    const emptyTotals = sumGroupTotals([], "USD");
    const slots = buildBucketSlots(Date.parse("2026-10-24T00:00:00Z"), Date.parse("2026-10-25T00:00:00Z"), "day");
    const chart = buildChartView({
      slots,
      groups: [],
      totals: emptyTotals,
      names: new Map(),
      colors: new Map(),
      metric: "spend",
      granularity: "day",
      chartWidth: 800,
      hoverSlot: null,
      focusGroup: null,
      format: fmt,
      currency: "USD",
      labels,
    });
    expect(chart.bars0.filter((b) => b.kind === "3")).toHaveLength(0);

    const groups = aggregateGroups(
      [
        {
          at: "2026-10-24T00:00:00Z",
          group: "=SUM(1,2)",
          metrics: { spend: { amount: "1000000000", currency: "USD" }, requests: "1", successes: "1" },
        },
        {
          at: "2026-10-24T00:00:00Z",
          group: "  =1+1",
          metrics: { spend: { amount: "500000000", currency: "USD" }, requests: "1", successes: "1" },
        },
      ],
      "USD",
      "day",
    );
    const csv = buildUsageCsv(
      ["bucket", "currency", "group", "req", "ok", "spend", "in", "out", "cr", "cw"],
      slots,
      groups,
      new Map(),
    );
    expect(csv.startsWith("\uFEFF")).toBe(true);
    expect(csv).toContain(`"'=SUM(1,2)",1,1,1,,,,\n`);
    expect(csv).toContain(`,USD,'  =1+1,1,1,0.5,,,,\n`);
  });

  it("distinguishes unreported fields from zero and handles zero physical input cache denominator", () => {
    const metrics = [{ spend: { amount: "1000000000", currency: "USD" }, requests: "2", cache_rate_requests: "2" }];
    const totals = toUsageTotals(metrics, "USD", true);
    expect(totals).not.toBeNull();
    if (!totals) return;
    const kpi = buildKpiView(totals, null, "USD", fmt, labels);
    expect(kpi.totals2).toBe("—");
    expect(kpi.totals3).toBe("—");
    expect(kpi.ppmCurrency).toBe("");
    expect(kpi.hrUnit).toBe("");
    expect(formatSpendPerMillion(totals)).toBe("—");

    const tok = buildTokenView(totals, fmt, labels);
    expect(tok.tokens0[0]?.f4).toBe("—");
    expect(tok.cacheHitNote).toBe("Eligible 2, 0 physical input");

    const groups = aggregateGroups([{ at: "2026-10-24T00:00:00Z", group: "m1", metrics: metrics[0] }], "USD", "day");
    const table = buildTableView({
      groups,
      totals,
      names: new Map(),
      colors: new Map(),
      metric: "spend",
      groupBy: "model",
      sortCol: "spend",
      focusGroup: null,
      format: fmt,
      currency: "USD",
      labels,
    });
    expect(table.models0[0]?.f18).toBe("—");
    expect(table.models0[0]?.f20).toBe("—");
  });

  it("preserves exact BigInt money precision, small averages, partial week labels, and focused aria", () => {
    const groups = aggregateGroups(
      [
        mkBucket("2026-10-14T00:00:00Z", "g1", "9007199254740992", "1"),
        mkBucket("2026-10-14T00:00:00Z", "g2", "1", "1"),
      ],
      "USD",
      "week",
    );
    const total = sumGroupTotals(groups, "USD");
    expect(total.spendNano).toBe(9007199254740993n);

    const slots = buildBucketSlots(Date.parse("2026-10-14T00:00:00Z"), Date.parse("2026-10-16T00:00:00Z"), "week");
    const chartAll = buildChartView({
      ...baseView,
      slots,
      groups,
      totals: total,
      metric: "spend",
      granularity: "week",
      chartWidth: 800,
      hoverSlot: 0,
      focusGroup: null,
    });
    expect(chartAll.hoverTip?.bucketLabel).toBe("10/14 – 10/15");
    expect(chartAll.hoverTip?.totalFormatted).toBe("$9007199.254740993");

    const chartFocused = buildChartView({
      ...baseView,
      slots,
      groups,
      totals: total,
      metric: "spend",
      granularity: "week",
      chartWidth: 800,
      hoverSlot: null,
      focusGroup: "g2",
    });
    expect(chartFocused.chartAria).toContain("$0.000000001");

    const smallTot = toUsageTotals([{ spend: { amount: "1499999", currency: "USD" }, requests: "1" }], "USD");
    expect(smallTot && formatAvgPerRequest(smallTot, fmt.money, "USD")).toBe("$0.001499999");
    const bigA = groups[0]?.totals;
    const bigB = groups[1]?.totals;
    expect(bigA && bigB ? compareUsageTotals(bigA, bigB, "avg") : 0).toBe(1);
  });

  it("distinguishes previous-period states, payload validation, and multiple currencies", () => {
    const emptyPrev = toUsageTotals([], "USD", true);
    const curr = toUsageTotals([{ spend: { amount: "5000000000", currency: "USD" }, requests: "10" }], "USD");
    expect(curr && buildKpiView(curr, emptyPrev, "USD", fmt, labels).changes0).toBe("New");
    expect(curr && buildKpiView(curr, null, "USD", fmt, labels).changes0).toBe("—");

    const freeGroups = aggregateGroups([mkBucket("2026-10-14T00:00:00Z", "m1", "0", "10")], "USD", "week");
    expect(
      buildMoversView({
        ...baseView,
        currGroups: freeGroups,
        prevGroups: freeGroups,
        prevStatus: "ready",
        prevHasCalls: true,
        metric: "spend",
      }).statusNote,
    ).toBe("No metric change");
    expect(
      buildMoversView({
        ...baseView,
        currGroups: freeGroups,
        prevGroups: null,
        prevStatus: "error",
        prevHasCalls: true,
        metric: "spend",
      }),
    ).toMatchObject({ isError: true, statusNote: "Failed movers" });

    expect(
      currenciesOfUsage({
        totals: [
          { spend: { amount: "100", currency: "USD" }, requests: "1" },
          { spend: { amount: "200", currency: "CNY" }, requests: "1" },
        ],
        buckets: [],
      }),
    ).toEqual(["USD", "CNY"]);
    expect(hasUsageStatisticsPayload({ totals: [] })).toBe(false);
    expect(hasUsageStatisticsPayload({ buckets: [] })).toBe(false);
    expect(hasUsageStatisticsPayload({ totals: [], buckets: [] })).toBe(true);
  });

  it("distinguishes unreported from zero in table share, movers, hover tips, and currency buckets", () => {
    const unreportedCurr = aggregateGroups([mkBucket("2026-10-14T00:00:00Z", "m1", "0", "1")], "USD", "day");
    const unreportedTot = sumGroupTotals(unreportedCurr, "USD");
    const unreportedTable = buildTableView({
      ...baseView,
      groups: unreportedCurr,
      totals: unreportedTot,
      metric: "tokens",
      groupBy: "model",
      sortCol: "spend",
      focusGroup: null,
    });
    expect(unreportedTable.models0[0]?.f14).toBe("—");

    const reportedPrev = aggregateGroups(
      [
        mkBucket("2026-10-07T00:00:00Z", "m1", "0", "1", { input: "100" }),
        mkBucket("2026-10-07T00:00:00Z", "m2", "0", "1", { input: "50" }),
      ],
      "USD",
      "week",
    );
    expect(
      buildMoversView({
        ...baseView,
        currGroups: unreportedCurr,
        prevGroups: reportedPrev,
        prevStatus: "ready",
        prevHasCalls: true,
        metric: "tokens",
      }).rows.map((r) => [r.f2, r.f9, r.f11]),
    ).toEqual([["m2", "50 → —", "−100%"]]);

    const freeSlots = buildBucketSlots(Date.parse("2026-10-14T00:00:00Z"), Date.parse("2026-10-15T00:00:00Z"), "day");
    const freeChart = buildChartView({
      ...baseView,
      groups: unreportedCurr,
      totals: unreportedTot,
      slots: freeSlots,
      metric: "spend",
      granularity: "day",
      focusGroup: null,
      hoverSlot: 0,
      chartWidth: 400,
    });
    expect(freeChart.hoverTip?.totalFormatted).toBe("$0.00");
    expect(freeChart.hoverTip?.items).toEqual([]);

    const usdBuckets = [mkBucket("2026-10-14T00:00:00Z", "m1", "100", "1")];
    expect(hasCurrencyBuckets(usdBuckets, "USD")).toBe(true);
    expect(hasCurrencyBuckets(usdBuckets, "CNY")).toBe(false);
  });
});
