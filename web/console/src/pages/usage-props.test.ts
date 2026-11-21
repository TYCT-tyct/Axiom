import { describe, expect, it } from "vitest";
import { buildChartView, buildSegs, type Formatters } from "./usage-chart-view";
import { buildBucketSlots, sumGroupTotals } from "./usage-model";
import { getUsageLabels } from "./usage-props";

const fmt: Formatters = {
  number: (n) => String(n),
  money: ({ amount }) => amount,
  day: (d) => d.toISOString(),
  dayFull: (d) => d.toISOString(),
  hour: (d) => d.toISOString(),
};

// The English resources write the metric in sentence case in the chart's title and capitalised on its own button, so
// only the real resources catch a title built from the button's word ("Daily Spend").
describe("the usage page's English", () => {
  it("titles the chart in sentence case and keeps the metric button's own word", () => {
    const labels = getUsageLabels("en");
    const chart = buildChartView({
      slots: buildBucketSlots(Date.parse("2026-10-24T00:00:00Z"), Date.parse("2026-10-25T00:00:00Z"), "day"),
      groups: [],
      totals: sumGroupTotals([], "USD"),
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
    expect(chart.chartTitle).toBe("Daily spend");
    expect(buildSegs("day", "spend", "model", labels).find((seg) => seg.key === "met")?.f7).toBe("Spend");
  });

  it("reduces x-axis tick density on narrow viewports to prevent label overlap", () => {
    const labels = getUsageLabels("en");
    const common = {
      slots: buildBucketSlots(Date.parse("2026-11-01T00:00:00Z"), Date.parse("2026-11-21T00:00:00Z"), "day"),
      groups: [],
      totals: sumGroupTotals([], "USD"),
      names: new Map(),
      colors: new Map(),
      metric: "spend" as const,
      granularity: "day" as const,
      hoverSlot: null,
      focusGroup: null,
      format: fmt,
      currency: "USD",
      labels,
    };
    const narrow = buildChartView({ ...common, chartWidth: 270 });
    const wide = buildChartView({ ...common, chartWidth: 800 });
    expect(narrow.xLabels0.length).toBeLessThanOrEqual(4);
    expect(wide.xLabels0.length).toBeGreaterThan(narrow.xLabels0.length);
  });
});
