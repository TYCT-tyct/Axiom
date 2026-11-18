import { ApiError } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { usageText } from "../locales/usage";
import {
  balanceQuery,
  keysQuery,
  modelsQuery,
  siteQuery,
  type UsageGranularity,
  type UsageGroupBy,
  usageStatisticsQuery,
  workspacesQuery,
} from "../queries";
import { save } from "../statement";
import type { UsageLanguage } from "./usage-chart-view";
import { type CalState, resolveRange, type UsageSearch, usePopEffects, useUsageHandlers } from "./usage-handlers";
import {
  aggregateGroups,
  buildBucketSlots,
  buildUsageCsv,
  currenciesOfUsage,
  defaultGranularity,
  hasCurrencyBuckets,
  hasNonEmptyTotals,
  hasUsageStatisticsPayload,
  previousWindow,
  selectCurrency,
  seriesColor,
  sumGroupTotals,
  toUsageTotals,
  type UsageMetricKey,
  type UsageSortCol,
  utcDayOf,
  validateUsageRange,
} from "./usage-model";
import { buildFilterChips, buildRangeErrorText, buildUsageProps } from "./usage-props";
import type { MoversPrevStatus } from "./usage-view";

const noClientRetry = (count: number, error: unknown) =>
  !(error instanceof ApiError && [400, 403, 404].includes(error.status)) && count < 2;

function resolvePrevStatus(
  prevRaw: readonly unknown[] | undefined,
  prevHasCalls: boolean,
  prevBreakdown: { isPending: boolean; isError: boolean; data?: Parameters<typeof hasUsageStatisticsPayload>[0] },
): MoversPrevStatus {
  if (prevRaw === undefined) return "missing";
  if (!prevHasCalls) return "ready";
  if (prevBreakdown.isPending) return "loading";
  if (prevBreakdown.isError) return "error";
  return hasUsageStatisticsPayload(prevBreakdown.data) ? "ready" : "missing";
}

function summarizeMainPayload(
  main: { isSuccess: boolean; data?: Parameters<typeof currenciesOfUsage>[0] },
  currencies: readonly string[],
  preferred: string | undefined,
) {
  const missingFields = main.isSuccess && !hasUsageStatisticsPayload(main.data);
  const hasPayload = main.isSuccess && !missingFields;
  const currency = selectCurrency(main.data?.totals, preferred, currencies) ?? preferred ?? "USD";
  const totals = hasPayload ? toUsageTotals(main.data?.totals, currency, true) : null;
  const hasUsage = Boolean(
    hasPayload && (hasNonEmptyTotals(totals) || hasCurrencyBuckets(main.data?.buckets, currency)),
  );
  const prevRaw = hasPayload ? main.data?.previous : undefined;
  const prevTotals = hasPayload ? toUsageTotals(prevRaw, currency, true) : null;
  const prevHasCalls = hasNonEmptyTotals(prevTotals);
  return { missingFields, currency, totals, hasUsage, prevRaw, prevTotals, prevHasCalls };
}

function useUsageQueries(params: {
  since: string;
  until: string;
  granularity: UsageGranularity;
  groupBy: UsageGroupBy;
  search: UsageSearch;
  valid: boolean;
  prevRange: { since: string; until: string } | null;
}) {
  const { since, until, granularity, groupBy, search, valid, prevRange } = params;
  const session = useSession();
  const base = {
    ...(search.model ? { model: search.model } : {}),
    ...(search.key_id ? { key_id: search.key_id } : {}),
    ...(search.workspace_id ? { workspace_id: search.workspace_id } : {}),
  };
  const main = useQuery({
    ...usageStatisticsQuery({ since, until, granularity, group_by: groupBy, compare: "previous", ...base }, valid),
    retry: noClientRetry,
  });
  const balance = useQuery({ ...balanceQuery, enabled: can(session, "balance.view"), retry: noClientRetry });
  const site = useQuery(siteQuery);
  const keys = useInfiniteQuery({ ...keysQuery(), enabled: can(session, "api_keys.view"), retry: noClientRetry });
  const workspaces = useQuery({ ...workspacesQuery, retry: noClientRetry });
  const models = useQuery({ ...modelsQuery({}), retry: noClientRetry });
  const currencies = useMemo(
    () => (main.isSuccess && hasUsageStatisticsPayload(main.data) ? currenciesOfUsage(main.data) : []),
    [main.isSuccess, main.data],
  );
  const preferred = search.currency ?? balance.data?.available.currency ?? site.data?.currency;
  const summary = summarizeMainPayload(main, currencies, preferred);
  const prevBreakdown = useQuery({
    ...usageStatisticsQuery(
      { since: prevRange?.since, until: prevRange?.until, granularity: "week", group_by: groupBy, ...base },
      Boolean(valid && prevRange && summary.hasUsage && summary.prevRaw !== undefined && summary.prevHasCalls),
    ),
    retry: noClientRetry,
  });
  const prevStatus = resolvePrevStatus(summary.prevRaw, summary.prevHasCalls, prevBreakdown);
  return { main, prevBreakdown, keys, workspaces, models, currencies, prevStatus, ...summary };
}

function useUsageState(now: Date, resolved: ReturnType<typeof resolveRange>) {
  const [openChip, setOpenChip] = useState<"m" | "k" | null>(null);
  const [sortCol, setSortCol] = useState<UsageSortCol | null>(null);
  const [focusGroup, setFocusGroup] = useState<string | null>(null);
  const [hoverSlot, setHoverSlot] = useState<number | null>(null);
  const [chartWidth, setChartWidth] = useState(964);
  const [filterBarEl, setFilterBarEl] = useState<Element | null>(null);
  const [keyPopEl, setKeyPopEl] = useState<Element | null>(null);
  const [chartSvgEl, setChartSvgEl] = useState<Element | null>(null);
  const [moversEl, setMoversEl] = useState<Element | null>(null);
  const [cal, setCal] = useState<CalState>(() => ({
    open: false,
    leftYm: now.getUTCFullYear() * 12 + now.getUTCMonth() - 1,
    start: resolved.startDay,
    end: resolved.endDay,
    hover: null,
  }));
  return {
    openChip,
    setOpenChip,
    sortCol,
    setSortCol,
    focusGroup,
    setFocusGroup,
    hoverSlot,
    setHoverSlot,
    chartWidth,
    setChartWidth,
    filterBarEl,
    setFilterBarEl,
    keyPopEl,
    setKeyPopEl,
    chartSvgEl,
    setChartSvgEl,
    moversEl,
    setMoversEl,
    cal,
    setCal,
  };
}

function useDerivedUsage(
  q: ReturnType<typeof useUsageQueries>,
  granularity: UsageGranularity,
  groupBy: UsageGroupBy,
  validation: ReturnType<typeof validateUsageRange>,
) {
  const currGroups = useMemo(
    () => aggregateGroups(q.main.data?.buckets, q.currency, granularity),
    [q.main.data?.buckets, q.currency, granularity],
  );
  const prevGroups = useMemo(() => {
    if (q.prevStatus !== "ready") return null;
    if (!q.prevHasCalls) return [];
    return aggregateGroups(q.prevBreakdown.data?.buckets, q.currency, "week");
  }, [q.prevStatus, q.prevHasCalls, q.prevBreakdown.data?.buckets, q.currency]);
  const effectiveTotals = q.totals ?? sumGroupTotals(currGroups, q.currency);
  const colorMap = useMemo(() => {
    const sorted = [...currGroups].sort((a, b) =>
      a.totals.spendNano !== b.totals.spendNano
        ? a.totals.spendNano > b.totals.spendNano
          ? -1
          : 1
        : a.id.localeCompare(b.id),
    );
    return new Map(sorted.map((g, i) => [g.id, seriesColor(i)]));
  }, [currGroups]);
  const keyNames = useMemo(
    () => new Map((q.keys.data?.pages.flatMap((p) => p.data) ?? []).map((k) => [k.id, k.name])),
    [q.keys.data],
  );
  const workspacesList = q.workspaces.data?.data ?? [];
  const workspaceNames = useMemo(() => new Map(workspacesList.map((w) => [w.id, w.name])), [workspacesList]);
  const groupNames =
    groupBy === "key" ? keyNames : groupBy === "workspace" ? workspaceNames : new Map<string, string>();
  const modelsList = useMemo(() => (q.models.data?.data ?? []).map((m) => m.id), [q.models.data]);
  const slots = useMemo(
    () => (validation.ok ? buildBucketSlots(validation.sinceMs, validation.untilMs, granularity) : []),
    [validation, granularity],
  );
  return {
    currGroups,
    prevGroups,
    effectiveTotals,
    prevTotals: q.prevTotals,
    colorMap,
    keyNames,
    workspacesList,
    workspaceNames,
    groupNames,
    modelsList,
    slots,
  };
}

const CSV_KEYS = [
  "csvHeaderBucket",
  "csvHeaderCurrency",
  "csvHeaderGroup",
  "csvHeaderRequests",
  "csvHeaderSuccesses",
  "csvHeaderSpend",
  "csvHeaderInput",
  "csvHeaderOutput",
  "csvHeaderCacheRead",
  "csvHeaderCacheWrite",
] as const;

function useUsageParams() {
  const { i18n } = useTranslation();
  const language: UsageLanguage = i18n.language === "en" ? "en" : "zh";
  const format = useFormat("UTC");
  const navigate = useNavigate();
  const search: UsageSearch = useSearch({ from: "/app/usage" });
  const [now, setNow] = useState(() => new Date());
  const todayDay = useMemo(() => utcDayOf(now), [now]);
  const resolved = resolveRange(search, now);
  const granularity: UsageGranularity = search.granularity ?? defaultGranularity(resolved.preset, resolved.days || 1);
  const metric: UsageMetricKey = search.metric ?? "spend";
  const groupBy: UsageGroupBy = search.group_by ?? "model";
  const validation = validateUsageRange(resolved.since, resolved.until, granularity);
  const prevRange = validation.ok ? previousWindow(validation.sinceMs, validation.untilMs) : null;
  const updateSearch = (patch: Partial<UsageSearch>) => {
    void navigate({ to: "/usage", search: (prev: UsageSearch) => ({ ...prev, ...patch }), replace: true });
  };
  return {
    language,
    format,
    search,
    now,
    setNow,
    todayDay,
    resolved,
    granularity,
    metric,
    groupBy,
    validation,
    prevRange,
    updateSearch,
  };
}

function buildUsageResult(
  p: ReturnType<typeof useUsageParams>,
  st: ReturnType<typeof useUsageState>,
  q: ReturnType<typeof useUsageQueries>,
  d: ReturnType<typeof useDerivedUsage>,
  handlers: ReturnType<typeof useUsageHandlers>,
  onExport: () => void,
) {
  const chips = buildFilterChips({
    ...d,
    language: p.language,
    groupBy: p.groupBy,
    modelFilter: p.search.model,
    keyFilter: p.search.key_id,
    workspaceFilter: p.search.workspace_id,
  });
  const unavailable = (q.main.error instanceof ApiError && q.main.error.status === 404) || q.missingFields;
  const showState = !p.validation.ok || unavailable || q.main.isPending || q.main.isError || !q.hasUsage;
  const built = buildUsageProps({
    ...p,
    ...st,
    ...d,
    currency: q.currency,
    currencies: q.currencies,
    prevStatus: q.prevStatus,
    prevHasCalls: q.prevHasCalls,
    chips,
    unavailable,
    showState,
    handlers,
    onExport,
  });
  return {
    language: p.language,
    english: p.language === "en",
    granularity: p.granularity,
    main: q.main,
    validation: p.validation,
    rangeErrorText: buildRangeErrorText(p.validation, p.granularity, p.language),
    onRecoverRange: () =>
      p.updateSearch(
        p.granularity === "hour" && !p.validation.ok && p.validation.reason === "limit"
          ? { granularity: "day" }
          : { range: "d7", since: undefined, until: undefined, granularity: "day" },
      ),
    unavailable,
    hasUsage: q.hasUsage,
    backfillIncomplete: Boolean(q.main.data && q.main.data.backfill_complete === false),
    currencies: q.currencies,
    currency: q.currency,
    onSelectCurrency: (code: string) => p.updateSearch({ currency: code }),
    hasMoreKeys: p.groupBy !== "workspace" && Boolean(q.keys.hasNextPage),
    loadingMoreKeys: q.keys.isFetchingNextPage,
    moreKeysError: q.keys.isFetchNextPageError,
    onLoadMoreKeys: () => void q.keys.fetchNextPage(),
    onRetryMovers: () => void q.prevBreakdown.refetch(),
    calOpen: st.cal.open,
    filterBarEl: st.filterBarEl,
    keyPopEl: st.keyPopEl,
    moversEl: st.moversEl,
    hoverTip: built.hoverTip,
    moversStatusNote: built.moversStatusNote,
    moversError: built.moversError,
    usageProps: built.usageProps,
    calendarProps: built.calendarProps,
  };
}

export function useUsagePage() {
  const p = useUsageParams();
  const st = useUsageState(p.now, p.resolved);
  const { setFocusGroup, setHoverSlot } = st;
  useEffect(() => {
    setFocusGroup(null);
    setHoverSlot(null);
  }, [
    p.groupBy,
    p.resolved.since,
    p.resolved.until,
    p.search.model,
    p.search.key_id,
    p.search.workspace_id,
    p.search.currency,
    setFocusGroup,
    setHoverSlot,
  ]);
  usePopEffects({
    chartSvgEl: st.chartSvgEl,
    setChartWidth: st.setChartWidth,
    setNow: p.setNow,
    openChip: st.openChip,
    setOpenChip: st.setOpenChip,
    calOpen: st.cal.open,
    closeCal: () => st.setCal((c) => (c.open ? { ...c, open: false, hover: null } : c)),
  });
  const q = useUsageQueries({
    since: p.resolved.since,
    until: p.resolved.until,
    granularity: p.granularity,
    groupBy: p.groupBy,
    search: p.search,
    valid: p.validation.ok,
    prevRange: p.prevRange,
  });
  const d = useDerivedUsage(q, p.granularity, p.groupBy, p.validation);
  const handlers = useUsageHandlers({
    ...p,
    ...st,
    slots: d.slots,
  });
  const onExport = () => {
    if (!q.hasUsage || q.missingFields || !p.validation.ok) return;
    const headers = CSV_KEYS.map((k) => usageText(p.language, k, { currency: q.currency }));
    const csv = buildUsageCsv(headers, d.slots, d.currGroups, d.groupNames);
    const name = `usage-${p.groupBy}-${q.currency}-${p.resolved.since.slice(0, 10)}.csv`;
    save(new File([csv], name, { type: "text/csv;charset=utf-8" }));
  };
  return buildUsageResult(p, st, q, d, handlers, onExport);
}
