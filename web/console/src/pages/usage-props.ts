import type { Props as UsageProps } from "@axiom/boards/AxApp/regions/Usage";
import type { Props as UsageCalendarProps } from "@axiom/boards/AxApp/regions/UsageCalendar";
import type { useFormat } from "@axiom/ui";
import { type UsageLocaleKey, usageText } from "../locales/usage";
import type { UsageGranularity, UsageGroupBy } from "../queries";
import {
  buildChartView,
  buildKpiView,
  buildSegs,
  type HoverTipData,
  type UsageLanguage,
  type UsageViewLabels,
} from "./usage-chart-view";
import type { CalState, resolveRange, UsageSearch, useUsageHandlers } from "./usage-handlers";
import {
  type aggregateGroups,
  type buildBucketSlots,
  DAY_MS,
  seriesColor,
  sortColForMetric,
  type toUsageTotals,
  type UsageMetricKey,
  type UsageSortCol,
  type validateUsageRange,
} from "./usage-model";
import { buildCalendarView, buildMoversView, buildTableView, buildTokenView } from "./usage-view";

export function getUsageLabels(language: UsageLanguage): UsageViewLabels & {
  quick: readonly [string, string, string, string];
  ranges: readonly [string, string, string, string, string];
} {
  const txt = (k: UsageLocaleKey, v?: Record<string, string | number>) => usageText(language, k, v);
  return {
    newLabel: txt("newItem"),
    unknownGroup: txt("unknownGroup"),
    unitByGranularity: { hour: txt("unitHour"), day: txt("unitDay"), week: txt("unitWeek") },
    metricLabels: { spend: txt("spend"), requests: txt("requests"), tokens: txt("tokens") },
    groupByLabels: { model: txt("byModel"), key: txt("byKey"), workspace: txt("byWorkspace") },
    groupColLabels: { model: txt("colModel"), key: txt("colKey"), workspace: txt("colWorkspace") },
    granularityLabels: { hour: txt("granHour"), day: txt("granDay"), week: txt("granWeek") },
    tableCols: {
      spend: txt("colSpend"),
      share: txt("colShare"),
      requests: txt("colRequests"),
      input: txt("colInput"),
      output: txt("colOutput"),
      avg: txt("colAvg"),
    },
    tokenRows: [
      txt("csvHeaderInput")
        .replace(/ ?Token$/i, "")
        .replace(/ ?tokens$/i, ""),
      txt("csvHeaderOutput")
        .replace(/ ?Token$/i, "")
        .replace(/ ?tokens$/i, ""),
      txt("csvHeaderCacheRead")
        .replace(/ ?Token$/i, "")
        .replace(/ ?tokens$/i, ""),
      txt("csvHeaderCacheWrite")
        .replace(/ ?Token$/i, "")
        .replace(/ ?tokens$/i, ""),
    ],
    ppDelta: (sign, value) => txt("ppDelta", { sign, value }),
    successOnly: (rate) => txt("successRate", { rate }),
    deltaWithSuccess: (delta, rate) => txt("deltaWithSuccess", { delta, rate }),
    cacheHitSummary: (rate, count) => txt("cacheHitSummary", { rate, count }),
    cacheHitNone: txt("cacheHitNone"),
    noMovers: txt("noMovers"),
    chartAria: (title, buckets, total) => txt("chartAria", { title, buckets, total }),
    quick: [txt("quickYesterday"), txt("quickLastWeek"), txt("quickLastMonth"), txt("quick90d")],
    ranges: [txt("rangeToday"), txt("range7d"), txt("range30d"), txt("rangeMonth"), txt("rangeCustom")],
  };
}

function makeKeyOpt(
  key: string,
  action: string,
  id: string,
  label: string,
  active: boolean,
  idx: number,
): UsageProps["rows"]["keyOpts0"][number] {
  return {
    key,
    kind: "0",
    f0: "button",
    f1: active ? "on" : "",
    f2: action,
    f3: id,
    f4: `background: ${seriesColor(idx)};`,
    f5: label,
  };
}

function buildModelOpts(
  modelsList: readonly string[],
  currGroups: readonly { id: string }[],
  groupBy: UsageGroupBy,
  modelFilter?: string,
): UsageProps["rows"]["modelOpts0"][number][] {
  const allModels = new Set(modelsList);
  for (const g of currGroups) if (groupBy === "model" && g.id) allModels.add(g.id);
  if (modelFilter) allModels.add(modelFilter);
  return [...allModels].map((name, i) => ({
    key: `m-${name}`,
    kind: "1",
    f0: "button",
    f1: modelFilter === name ? "on" : "",
    f2: "pickm",
    f3: name,
    f4: `background: ${seriesColor(i)};`,
    f5: name,
  }));
}

function resolveKeyChipLabel(params: {
  language: UsageLanguage;
  groupBy: UsageGroupBy;
  multiWs: boolean;
  keyFilter?: string;
  workspaceFilter?: string;
  keyNames: ReadonlyMap<string, string>;
  workspaceNames: ReadonlyMap<string, string>;
}) {
  const { language, groupBy, multiWs, keyFilter, workspaceFilter, keyNames, workspaceNames } = params;
  if (keyFilter) return keyNames.get(keyFilter) ?? keyFilter;
  if (workspaceFilter) return workspaceNames.get(workspaceFilter) ?? workspaceFilter;
  return usageText(language, groupBy === "workspace" ? "allWorkspaces" : multiWs ? "keyOrWorkspace" : "allKeys");
}

export function buildFilterChips(params: {
  language: UsageLanguage;
  modelsList: readonly string[];
  currGroups: readonly { id: string }[];
  groupBy: UsageGroupBy;
  modelFilter?: string;
  keyFilter?: string;
  workspaceFilter?: string;
  keyNames: ReadonlyMap<string, string>;
  workspacesList: readonly { id: string; name: string }[];
  workspaceNames: ReadonlyMap<string, string>;
}) {
  const {
    language,
    modelsList,
    currGroups,
    groupBy,
    modelFilter,
    keyFilter,
    workspaceFilter,
    keyNames,
    workspacesList,
    workspaceNames,
  } = params;
  const multiWs = workspacesList.length > 1;
  const showWs = multiWs || groupBy === "workspace" || Boolean(workspaceFilter);
  const showKeys = groupBy !== "workspace";
  const keyPrefix = usageText(language, "keyPrefix");
  const wsPrefix = usageText(language, "workspacePrefix");
  const keyOpts = showKeys
    ? [...keyNames].map(([id, n], i) =>
        makeKeyOpt(`k-${id}`, "pickk", id, showWs ? `${keyPrefix}${n}` : n, keyFilter === id, i),
      )
    : [];
  const wsOpts = showWs
    ? workspacesList.map((w, i) =>
        makeKeyOpt(
          `w-${w.id}`,
          "pickw",
          w.id,
          showKeys ? `${wsPrefix}${w.name}` : w.name,
          workspaceFilter === w.id,
          keyOpts.length + i,
        ),
      )
    : [];
  return {
    modelOpts0: buildModelOpts(modelsList, currGroups, groupBy, modelFilter),
    keyOpts0: [...keyOpts, ...wsOpts],
    modelChipLabel: modelFilter ?? usageText(language, "allModels"),
    keyChipLabel: resolveKeyChipLabel({
      language,
      groupBy,
      multiWs,
      keyFilter,
      workspaceFilter,
      keyNames,
      workspaceNames,
    }),
  };
}

function formatComparisonSpan(
  prevSince: string,
  prevUntil: string,
  format: ReturnType<typeof useFormat>,
  lang: UsageLanguage,
) {
  const s = new Date(Date.parse(prevSince));
  const e = new Date(Date.parse(prevUntil) - 1);
  const same =
    s.getUTCFullYear() === e.getUTCFullYear() &&
    s.getUTCMonth() === e.getUTCMonth() &&
    s.getUTCDate() === e.getUTCDate();
  return usageText(lang, "compareRange", { range: same ? format.day(s) : `${format.day(s)} – ${format.day(e)}` });
}

function customButtonLabel(
  resolved: ReturnType<typeof resolveRange>,
  format: ReturnType<typeof useFormat>,
  fallback: string,
) {
  if (resolved.preset !== "custom" || resolved.startDay === null || resolved.endDay === null) return fallback;
  return resolved.startDay === resolved.endDay
    ? format.day(new Date(resolved.startDay * DAY_MS))
    : `${format.day(new Date(resolved.startDay * DAY_MS))} – ${format.day(new Date(resolved.endDay * DAY_MS))}`;
}

export function buildRangeErrorText(
  validation: ReturnType<typeof validateUsageRange>,
  granularity: UsageGranularity,
  lang: UsageLanguage,
) {
  if (validation.ok) return null;
  if (validation.reason === "hour") return usageText(lang, "rangeWholeHour");
  if (validation.reason === "order") return usageText(lang, "rangeInvalidOrder");
  return usageText(lang, granularity === "hour" ? "limitBannerHour" : "limitBannerDays", { days: validation.days });
}

function makeValues(
  p: UsagePropsParams,
  extra: { presetIdx: number; chartAria: string; hoverTip: HoverTipData | null; tipStyle: string },
): UsageProps["values"] {
  return {
    hideBody: p.unavailable,
    hideCharts: p.showState,
    omitForecast: true,
    omitSavings: true,
    filterBarRef: (el) => p.setFilterBarEl(el),
    exportDisabled: p.showState,
    onExport: p.onExport,
    onBodyClick: p.handlers.onBodyClick,
    onChartMove: p.handlers.onChartMove,
    onChartLeave: () => {
      p.setHoverSlot(null);
      p.setHoverGroup(null);
    },
    onChartKeyDown: p.handlers.onChartKeyDown,
    chartAria: extra.chartAria,
    rangeSegStyle: `--n: 5; --i: ${extra.presetIdx};`,
    modelChipCls: p.openChip === "m" ? "open" : "",
    modelChipBtnCls: p.search.model ? "on" : "",
    modelChipExpanded: p.openChip === "m",
    keyChipCls: p.openChip === "k" ? "open" : "",
    keyChipBtnCls: p.search.key_id || p.search.workspace_id ? "on" : "",
    keyChipExpanded: p.openChip === "k",
    chartCls: p.focusGroup !== null || p.hoverGroup !== null ? "hl" : "",
    tipCls: extra.hoverTip ? "on" : "",
    tipStyle: extra.tipStyle,
  };
}

export type UsagePropsParams = {
  language: UsageLanguage;
  format: ReturnType<typeof useFormat>;
  todayDay: number;
  resolved: ReturnType<typeof resolveRange>;
  search: UsageSearch;
  granularity: UsageGranularity;
  metric: UsageMetricKey;
  groupBy: UsageGroupBy;
  sortCol: UsageSortCol | null;
  openChip: "m" | "k" | null;
  focusGroup: string | null;
  hoverGroup: string | null;
  hoverSlot: number | null;
  chartWidth: number;
  cal: CalState;
  prevRange: { since: string; until: string } | null;
  currency: string;
  effectiveTotals: NonNullable<ReturnType<typeof toUsageTotals>>;
  prevTotals: ReturnType<typeof toUsageTotals>;
  currGroups: ReturnType<typeof aggregateGroups>;
  prevGroups: ReturnType<typeof aggregateGroups> | null;
  groupNames: ReadonlyMap<string, string>;
  colorMap: ReadonlyMap<string, string>;
  slots: ReturnType<typeof buildBucketSlots>;
  chips: ReturnType<typeof buildFilterChips>;
  unavailable: boolean;
  showState: boolean;
  handlers: ReturnType<typeof useUsageHandlers>;
  setFilterBarEl: (el: Element | null) => void;
  setHoverSlot: (v: number | null) => void;
  setHoverGroup: (v: string | null) => void;
  onExport: () => void;
};

function buildCalendarBlock(p: UsagePropsParams, quick: readonly [string, string, string, string]) {
  const calView = buildCalendarView({
    leftYearMonth: p.cal.leftYm,
    selStart: p.cal.start,
    selEnd: p.cal.end,
    hoverDay: p.cal.hover,
    todayDay: p.todayDay,
    granularity: p.granularity,
    quickIndex: p.cal.quick,
    language: p.language,
    format: p.format,
    labels: {
      quick,
      pickStart: usageText(p.language, "pickStart"),
      pickEnd: (date) => usageText(p.language, "pickEnd", { date }),
      pickedSpan: (span, days) => usageText(p.language, "pickedSpan", { span, days }),
      limitError: (days, limit) => usageText(p.language, limit <= 7 ? "rangeLimitHour" : "rangeLimitDays", { days }),
    },
  });
  const values: UsageCalendarProps["values"] = {
    calendarOnly: true,
    calOverlayStyle: "position: absolute; top: 0; left: 0; right: 0; height: 0; pointer-events: auto;",
    onCalClick: p.handlers.onCalClick,
    onCalMove: p.handlers.onCalMove,
    prevMonthDisabled: calView.prevDisabled,
    nextMonthDisabled: calView.nextDisabled,
    confirmDisabled: calView.confirmDisabled,
  };
  return { values, rows: calView.rows, text: calView.text };
}

function buildSubViews(p: UsagePropsParams, labels: ReturnType<typeof getUsageLabels>) {
  const shared = { names: p.groupNames, colors: p.colorMap, labels };
  const chart = buildChartView({ ...p, ...shared, groups: p.currGroups, totals: p.effectiveTotals });
  const table = buildTableView({
    ...p,
    ...shared,
    groups: p.currGroups,
    totals: p.effectiveTotals,
    sortCol: p.sortCol ?? sortColForMetric(p.metric),
  });
  const tok = buildTokenView(p.effectiveTotals, p.format, labels);
  const movers0 = buildMoversView({ ...p, ...shared });
  return { chart, table, tok, movers0 };
}

function buildRowsAndText(p: UsagePropsParams, labels: ReturnType<typeof getUsageLabels>) {
  const presetIdx = ["today", "d7", "d30", "month", "custom"].indexOf(p.resolved.preset);
  const ranges = [...labels.ranges.slice(0, 4), customButtonLabel(p.resolved, p.format, labels.ranges[4])];
  const kpi = buildKpiView(p.effectiveTotals, p.prevTotals, p.currency, p.format, labels);
  const { chart, table, tok, movers0 } = buildSubViews(p, labels);
  const rows: UsageProps["rows"] = {
    ranges0: ranges.map((lbl, i) => ({
      key: `r-${i}`,
      kind: "0",
      f0: "button",
      f1: presetIdx === i ? "on" : "",
      f2: "r",
      f3: String(i),
      f4: lbl,
    })),
    modelOpts0: p.chips.modelOpts0,
    keyOpts0: p.chips.keyOpts0,
    segs0: buildSegs(p.granularity, p.metric, p.groupBy, labels),
    gridLines0: chart.gridLines0,
    bars0: chart.bars0,
    yLabels0: chart.yLabels0,
    xLabels0: chart.xLabels0,
    tableHeaders0: table.tableHeaders0,
    models0: table.models0,
    tokenBars0: tok.tokenBars0,
    tokens0: tok.tokens0,
    movers0,
  };
  const text: UsageProps["text"] = {
    ...kpi,
    modelChipLabel: p.chips.modelChipLabel,
    keyChipLabel: p.chips.keyChipLabel,
    cmp: p.prevRange ? formatComparisonSpan(p.prevRange.since, p.prevRange.until, p.format, p.language) : "",
    chartTitle: chart.chartTitle,
    chartViewBox: chart.chartViewBox,
    tfootTotals0: table.tfootTotals0,
    tfootTotals1: table.tfootTotals1,
    tfootTotals2: table.tfootTotals2,
    tfootTotals3: table.tfootTotals3,
    tfootTotals4: table.tfootTotals4,
    cacheHitNote: tok.cacheHitNote,
  };
  return { presetIdx, chart, rows, text };
}

export function buildUsageProps(p: UsagePropsParams) {
  const labels = getUsageLabels(p.language);
  const { presetIdx, chart, rows, text } = buildRowsAndText(p, labels);
  const values = makeValues(p, {
    presetIdx,
    chartAria: chart.chartAria,
    hoverTip: chart.hoverTip,
    tipStyle: chart.tipStyle,
  });
  return {
    hoverTip: chart.hoverTip,
    usageProps: { values, rows, text },
    calendarProps: buildCalendarBlock(p, labels.quick),
  };
}
