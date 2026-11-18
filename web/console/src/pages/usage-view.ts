import type { Props as UsageProps } from "@axiom/boards/AxApp/regions/Usage";
import type { Props as UsageCalendarProps } from "@axiom/boards/AxApp/regions/UsageCalendar";
import type { UsageGranularity, UsageGroupBy } from "../queries";
import {
  type Formatters,
  formatMetricValue,
  isMetricReported,
  type UsageLanguage,
  type UsageViewLabels,
} from "./usage-chart-view";
import {
  compareUsageTotals,
  DAY_MS,
  formatAvgPerRequest,
  formatBigDeltaPercent,
  formatCompactTokens,
  type GroupAggregate,
  metricBigValue,
  SERIES_COLORS,
  type UsageMetricKey,
  type UsageSortCol,
  type UsageTotals,
  utcDayOf,
  validateDaySpan,
} from "./usage-model";

const sharePct = (part: bigint, whole: bigint): number => (whole > 0n ? Number((part * 10_000n) / whole) / 100 : 0);

function buildTableHeaderRows(
  groupBy: UsageGroupBy,
  sortCol: UsageSortCol,
  labels: UsageViewLabels,
): UsageProps["rows"]["tableHeaders0"][number][] {
  const sortIds: Record<UsageSortCol, string> = { spend: "0", requests: "1", input: "3", output: "4", avg: "5" };
  const active = sortIds[sortCol];
  const sortCls = (base: string, id: string) => (active === id ? `${base ? `${base} ` : ""}on` : base);
  const sortColItem = (
    key: string,
    cls: string,
    id: string,
    label: string,
  ): UsageProps["rows"]["tableHeaders0"][number] => ({
    key,
    kind: "1",
    f0: sortCls(cls, id),
    f1: "sort",
    f2: id,
    f3: "button",
    f4: "0",
    f5: label,
  });
  return [
    { key: "h-grp", kind: "0", f0: "ussh", f1: labels.groupColLabels[groupBy] },
    sortColItem("h-sp", "", "0", labels.tableCols.spend),
    { key: "h-sh", kind: "0", f0: "ussh", f1: labels.tableCols.share },
    sortColItem("h-rq", "usrq", "1", labels.tableCols.requests),
    sortColItem("h-in", "ustc", "3", labels.tableCols.input),
    sortColItem("h-out", "ustc", "4", labels.tableCols.output),
    sortColItem("h-av", "usav", "5", labels.tableCols.avg),
  ];
}

const optCompact = (reported: boolean, val: bigint, numFmt: Formatters["number"]) =>
  reported ? formatCompactTokens(val, numFmt) : "—";

function buildTableRow(
  g: GroupAggregate,
  ctx: {
    metric: UsageMetricKey;
    metricTotal: bigint;
    names: ReadonlyMap<string, string>;
    colors: ReadonlyMap<string, string>;
    focusGroup: string | null;
    format: Formatters;
    currency: string;
    unknownGroup: string;
  },
): UsageProps["rows"]["models0"][number] {
  const { metric, metricTotal, names, colors, focusGroup, format, currency, unknownGroup } = ctx;
  const share = isMetricReported(g.totals, metric) ? sharePct(metricBigValue(g.totals, metric), metricTotal) : 0;
  const color = colors.get(g.id) ?? SERIES_COLORS[0];
  return {
    key: `row-${g.id || "_"}`,
    kind: "1",
    f0: focusGroup === g.id ? "on" : focusGroup !== null ? "dim" : "",
    f1: "row",
    f2: g.id,
    f3: g.id,
    f4: "usnm",
    f5: "button",
    f6: "background:none;border:0;padding:0;font:inherit;color:inherit;text-align:inherit;cursor:pointer",
    f7: `background: ${color};`,
    f8: names.get(g.id) ?? (g.id || unknownGroup),
    f9: g.totals.spendReported ? format.money({ amount: g.totals.spendNano.toString(), currency }) : "—",
    f10: "ussh",
    f11: "usshb",
    f12: `width: ${share.toFixed(1)}%; background: ${color};`,
    f13: "usmut",
    f14: `${Math.round(share)}%`,
    f15: "usrq",
    f16: g.totals.requestsReported ? format.number(g.totals.requests) : "—",
    f17: "ustc",
    f18: optCompact(g.totals.inputReported, g.totals.input, format.number),
    f19: "ustc",
    f20: optCompact(g.totals.outputReported, g.totals.output, format.number),
    f21: "usav usmut",
    f22: formatAvgPerRequest(g.totals, format.money, currency),
  };
}

export function buildTableView(params: {
  groups: readonly GroupAggregate[];
  totals: UsageTotals;
  names: ReadonlyMap<string, string>;
  colors: ReadonlyMap<string, string>;
  metric: UsageMetricKey;
  groupBy: UsageGroupBy;
  sortCol: UsageSortCol;
  focusGroup: string | null;
  format: Formatters;
  currency: string;
  labels: UsageViewLabels;
}) {
  const { groups, totals, names, colors, metric, groupBy, sortCol, focusGroup, format, currency, labels } = params;
  const metricTotal = metricBigValue(totals, metric);
  const sorted = [...groups].sort((a, b) => compareUsageTotals(b.totals, a.totals, sortCol));
  const rowCtx = {
    metric,
    metricTotal,
    names,
    colors,
    focusGroup,
    format,
    currency,
    unknownGroup: labels.unknownGroup,
  };
  return {
    tableHeaders0: buildTableHeaderRows(groupBy, sortCol, labels),
    models0: sorted.map((g) => buildTableRow(g, rowCtx)),
    tfootTotals0: totals.spendReported ? format.money({ amount: totals.spendNano.toString(), currency }) : "—",
    tfootTotals1: totals.requestsReported ? format.number(totals.requests) : "—",
    tfootTotals2: optCompact(totals.inputReported, totals.input, format.number),
    tfootTotals3: optCompact(totals.outputReported, totals.output, format.number),
    tfootTotals4: formatAvgPerRequest(totals, format.money, currency),
  };
}

export function buildTokenView(totals: UsageTotals, format: Formatters, labels: UsageViewLabels) {
  const parts = [
    { val: totals.input, reported: totals.inputReported, label: labels.tokenRows[0], bg: "var(--accentSolid)" },
    {
      val: totals.output,
      reported: totals.outputReported,
      label: labels.tokenRows[1],
      bg: "color-mix(in srgb, var(--accentSolid) 60%, var(--cardBg))",
    },
    {
      val: totals.cacheRead,
      reported: totals.cacheReadReported,
      label: labels.tokenRows[2],
      bg: "color-mix(in srgb, var(--accentSolid) 30%, var(--cardBg))",
    },
    { val: totals.cacheWrite, reported: totals.cacheWriteReported, label: labels.tokenRows[3], bg: "var(--line)" },
  ];
  const tokenBars0: UsageProps["rows"]["tokenBars0"][number][] = parts.map((p, i) => ({
    key: `tb-${i}`,
    kind: "0",
    f0: String(i),
    f1: `width: ${(p.reported ? sharePct(p.val, totals.tokens) : 0).toFixed(2)}%; background: ${p.bg};`,
  }));
  const tokens0: UsageProps["rows"]["tokens0"][number][] = parts.map((p, i) => ({
    key: `tr-${i}`,
    kind: "0",
    f0: "usrw",
    f1: `background: ${p.bg};`,
    f2: p.label,
    f3: "num",
    f4: p.reported ? formatCompactTokens(p.val, format.number) : "—",
    f5: p.reported && totals.tokens > 0n ? `${Math.round(sharePct(p.val, totals.tokens))}%` : "—",
  }));
  const cacheHitNote =
    totals.cacheHitRate !== null
      ? labels.cacheHitSummary(`${(totals.cacheHitRate * 100).toFixed(1)}%`, format.number(totals.cacheRateRequests))
      : totals.cacheRateRequests > 0n
        ? labels.cacheHitZeroDenom(format.number(totals.cacheRateRequests))
        : labels.cacheHitNone;
  return { tokenBars0, tokens0, cacheHitNote };
}

export type MoversPrevStatus = "missing" | "loading" | "error" | "ready";

export function buildMoversView(params: {
  currGroups: readonly GroupAggregate[];
  prevGroups: readonly GroupAggregate[] | null;
  prevStatus: MoversPrevStatus;
  prevHasCalls: boolean;
  names: ReadonlyMap<string, string>;
  colors: ReadonlyMap<string, string>;
  metric: UsageMetricKey;
  format: Formatters;
  currency: string;
  labels: UsageViewLabels;
}): { rows: UsageProps["rows"]["movers0"][number][]; statusNote: string | null; isError: boolean } {
  const { currGroups, prevGroups, prevStatus, prevHasCalls, names, colors, metric, format, currency, labels } = params;
  if (prevStatus === "loading") return { rows: [], statusNote: labels.moversLoading, isError: false };
  if (prevStatus === "error") return { rows: [], statusNote: labels.moversFailed, isError: true };
  if (prevStatus === "missing" || !prevGroups)
    return { rows: [], statusNote: labels.moversMissingPrev, isError: false };
  if (!prevHasCalls) return { rows: [], statusNote: labels.noMovers, isError: false };
  const currMap = new Map(currGroups.map((g) => [g.id, g.totals]));
  const prevMap = new Map(prevGroups.map((g) => [g.id, g.totals]));
  const ids = new Set([...currMap.keys(), ...prevMap.keys()]);
  const rows = [...ids]
    .map((id) => {
      const cTot = currMap.get(id) ?? null;
      const pTot = prevMap.get(id) ?? null;
      const cVal = cTot ? metricBigValue(cTot, metric) : 0n;
      const pVal = pTot ? metricBigValue(pTot, metric) : 0n;
      const abs = cVal >= pVal ? cVal - pVal : pVal - cVal;
      return { id, cTot, pTot, cVal, pVal, abs };
    })
    .filter((item) => item.abs > 0n)
    .sort((a, b) => (b.abs > a.abs ? 1 : b.abs < a.abs ? -1 : 0))
    .slice(0, 4);
  if (!rows.length) return { rows: [], statusNote: labels.noMetricChange, isError: false };
  return {
    statusNote: null,
    isError: false,
    rows: rows.map((row) => ({
      key: `mov-${row.id || "_"}`,
      kind: "0",
      f0: "usrw",
      f1: "row",
      f2: row.id,
      f3: row.id,
      f4: "button",
      f5: "0",
      f6: `background: ${colors.get(row.id) ?? SERIES_COLORS[0]};`,
      f7: names.get(row.id) ?? (row.id || labels.unknownGroup),
      f8: "num",
      f9: `${row.pTot ? formatMetricValue(row.pTot, metric, format, currency) : formatMetricValue(null, metric, format, currency)} → ${formatMetricValue(row.cTot, metric, format, currency)}`,
      f10: "num",
      f11: formatBigDeltaPercent(
        row.cVal,
        row.pTot && isMetricReported(row.pTot, metric) ? row.pVal : 0n,
        labels.newLabel,
      ),
    })),
  };
}

function dayCellClass(idx: number, todayDay: number, lo: number | null, hi: number | null) {
  const out = idx > todayDay;
  const cls = ["usdd"];
  if (out) cls.push("x");
  if (idx === todayDay) cls.push("td");
  if (!out && lo !== null && hi !== null && idx >= lo && idx <= hi) cls.push("in");
  if (!out && idx === lo) cls.push("s");
  if (!out && idx === hi) cls.push("e");
  return { out, cls: cls.join(" ") };
}

function buildCalendarMonth(
  ym: number,
  todayDay: number,
  lo: number | null,
  hi: number | null,
  language: UsageLanguage,
) {
  const year = Math.floor(ym / 12);
  const month = ym % 12;
  const first = new Date(Date.UTC(year, month, 1));
  const days = new Date(Date.UTC(year, month + 1, 0)).getUTCDate();
  const firstDayIdx = utcDayOf(first);
  const off = (first.getUTCDay() + 6) % 7;
  const cells: UsageCalendarProps["rows"]["rightCells0"][number][] = [];
  for (let k = 0; k < off; k++) cells.push({ key: `sp-${ym}-${k}`, kind: "0" });
  for (let d = 1; d <= days; d++) {
    const idx = firstDayIdx + d - 1;
    const { out, cls } = dayCellClass(idx, todayDay, lo, hi);
    cells.push({
      key: `d-${idx}`,
      kind: "1",
      f0: "button",
      f1: cls,
      f2: out ? "" : "cd",
      f3: String(idx),
      f4: String(d),
    });
  }
  const title = language === "zh" ? `${year} 年 ${month + 1} 月` : `${year}-${String(month + 1).padStart(2, "0")}`;
  return { title, cells };
}

function calendarStatusText(
  lo: number | null,
  hi: number | null,
  selEnd: number | null,
  hoverDay: number | null,
  granularity: UsageGranularity,
  format: Formatters,
  labels: {
    pickStart: string;
    pickEnd: (d: string) => string;
    pickedSpan: (span: string, days: number) => string;
    limitError: (days: number, limit: number) => string;
  },
) {
  if (lo === null || hi === null) return { calStatus: labels.pickStart, confirmDisabled: true };
  if (selEnd === null && hoverDay === null) {
    return { calStatus: labels.pickEnd(format.dayFull(new Date(lo * DAY_MS))), confirmDisabled: false };
  }
  const check = validateDaySpan(lo, hi, granularity);
  if (!check.ok) return { calStatus: labels.limitError(check.days, check.limitDays), confirmDisabled: true };
  const span =
    lo === hi
      ? format.dayFull(new Date(lo * DAY_MS))
      : `${format.dayFull(new Date(lo * DAY_MS))} – ${format.dayFull(new Date(hi * DAY_MS))}`;
  return { calStatus: labels.pickedSpan(span, check.days), confirmDisabled: false };
}

export function buildCalendarView(params: {
  leftYearMonth: number;
  selStart: number | null;
  selEnd: number | null;
  hoverDay: number | null;
  todayDay: number;
  granularity: UsageGranularity;
  language: UsageLanguage;
  format: Formatters;
  labels: {
    quick: readonly [string, string, string, string];
    pickStart: string;
    pickEnd: (d: string) => string;
    pickedSpan: (span: string, days: number) => string;
    limitError: (days: number, limit: number) => string;
  };
}): Pick<UsageCalendarProps, "rows" | "text"> & {
  prevDisabled: boolean;
  nextDisabled: boolean;
  confirmDisabled: boolean;
} {
  const { leftYearMonth, selStart, selEnd, hoverDay, todayDay, granularity, language, format, labels } = params;
  const endCandidate = selEnd ?? (selStart !== null && hoverDay !== null ? hoverDay : null);
  const lo = selStart !== null && endCandidate !== null ? Math.min(selStart, endCandidate) : selStart;
  const hi = selStart !== null && endCandidate !== null ? Math.max(selStart, endCandidate) : selStart;
  const todayDate = new Date(todayDay * DAY_MS);
  const maxRightYm = todayDate.getUTCFullYear() * 12 + todayDate.getUTCMonth();
  const left = buildCalendarMonth(leftYearMonth, todayDay, lo, hi, language);
  const right = buildCalendarMonth(leftYearMonth + 1, todayDay, lo, hi, language);
  const wkNames =
    language === "zh" ? ["一", "二", "三", "四", "五", "六", "日"] : ["Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"];
  const makeWk = (p: string): UsageCalendarProps["rows"]["weekdaysLeft0"][number][] =>
    wkNames.map((w, i) => ({ key: `${p}-${i}`, kind: "0", f0: "usdw", f1: w }));
  const status = calendarStatusText(lo, hi, selEnd, hoverDay, granularity, format, labels);
  return {
    rows: {
      quickBtns0: labels.quick.map((lbl, i) => ({
        key: `cq-${i}`,
        kind: "0",
        f0: "button",
        f1: "cq",
        f2: String(i),
        f3: "",
        f4: lbl,
      })),
      weekdaysLeft0: makeWk("wl"),
      weekdaysRight0: makeWk("wr"),
      leftCells0: left.cells,
      rightCells0: right.cells,
    },
    text: { months0: left.title, months1: right.title, calStatus: status.calStatus },
    prevDisabled: leftYearMonth <= maxRightYm - 24,
    nextDisabled: leftYearMonth + 1 >= maxRightYm,
    confirmDisabled: status.confirmDisabled,
  };
}
