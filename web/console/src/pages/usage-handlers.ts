import { useNavigate } from "@tanstack/react-router";
import type { KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent } from "react";
import { useEffect } from "react";
import type { UsageGranularity, UsageGroupBy } from "../queries";
import {
  DAY_MS,
  defaultGranularity,
  presetDaySpan,
  type UsageMetricKey,
  type UsageRangePreset,
  type UsageSortCol,
  windowFromDays,
} from "./usage-model";

export type UsageSearch = {
  range?: UsageRangePreset;
  since?: string;
  until?: string;
  granularity?: UsageGranularity;
  metric?: UsageMetricKey;
  group_by?: UsageGroupBy;
  model?: string;
  key_id?: string;
  workspace_id?: string;
};

export type CalState = {
  open: boolean;
  leftYm: number;
  start: number | null;
  end: number | null;
  hover: number | null;
  quick: number | null;
};

const MAX_HOUR_DAYS = 7;

export function resolveRange(search: UsageSearch, now: Date) {
  const preset: UsageRangePreset = search.range ?? "month";
  if (preset !== "custom" || !search.since || !search.until) {
    const span = presetDaySpan(preset === "custom" ? "month" : preset, now);
    const { since, until } = windowFromDays(span.startDay, span.endDay);
    return {
      preset: preset === "custom" ? ("month" as const) : preset,
      since,
      until,
      startDay: span.startDay,
      endDay: span.endDay,
      days: span.endDay - span.startDay + 1,
    };
  }
  const sMs = Date.parse(search.since);
  const uMs = Date.parse(search.until);
  const days = Number.isFinite(sMs) && Number.isFinite(uMs) && uMs > sMs ? Math.ceil((uMs - sMs) / DAY_MS) : 0;
  return {
    preset,
    since: search.since,
    until: search.until,
    startDay: Number.isFinite(sMs) ? Math.floor(sMs / DAY_MS) : null,
    endDay: Number.isFinite(uMs) ? Math.floor((uMs - 1) / DAY_MS) : null,
    days,
  };
}

function quickPresetDays(idx: number, today: number): { start: number; end: number; quick: number } {
  if (idx === 0) return { start: today - 1, end: today - 1, quick: 0 };
  if (idx === 1) {
    const weekday = (new Date(today * DAY_MS).getUTCDay() + 6) % 7;
    const thisMon = today - weekday;
    return { start: thisMon - 7, end: thisMon - 1, quick: 1 };
  }
  if (idx === 2) {
    const d = new Date(today * DAY_MS);
    const firstCur = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1);
    const firstPrev = Date.UTC(d.getUTCFullYear(), d.getUTCMonth() - 1, 1);
    return { start: Math.floor(firstPrev / DAY_MS), end: Math.floor(firstCur / DAY_MS) - 1, quick: 2 };
  }
  return { start: today - 89, end: today, quick: 3 };
}

export function usePopEffects(params: {
  filterBarEl: Element | null;
  chartWidth: number;
  setChartWidth: (w: number) => void;
  openChip: "m" | "k" | null;
  setOpenChip: (v: "m" | "k" | null) => void;
  calOpen: boolean;
  closeCal: () => void;
  valid: boolean;
}) {
  const { filterBarEl, chartWidth, setChartWidth, openChip, setOpenChip, calOpen, closeCal, valid } = params;
  useEffect(() => {
    const svg = filterBarEl?.closest(".usv")?.querySelector(".usch svg") ?? null;
    if (!svg) return;
    const measure = () => {
      if (svg.clientWidth > 100 && Math.abs(svg.clientWidth - chartWidth) > 2) setChartWidth(svg.clientWidth);
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(svg);
    return () => ro.disconnect();
  }, [filterBarEl, chartWidth, setChartWidth, valid]);

  useEffect(() => {
    if (!openChip && !calOpen) return;
    const onPointerDown = (e: PointerEvent) => {
      if ((e.target as Element | null)?.closest('.uschip, .uscal, [data-a="r"][data-i="4"]')) return;
      setOpenChip(null);
      closeCal();
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setOpenChip(null);
      closeCal();
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [openChip, calOpen, setOpenChip, closeCal]);
}

function slotCallsSearch(
  slot: { sinceIso: string; untilIso: string },
  search: UsageSearch,
  focusGroup: string | null,
  groupBy: UsageGroupBy,
) {
  const focusParam =
    focusGroup === null
      ? {}
      : groupBy === "model"
        ? { model: focusGroup }
        : groupBy === "key"
          ? { key_id: focusGroup }
          : { workspace_id: focusGroup };
  return {
    since: slot.sinceIso,
    until: slot.untilIso,
    ...(search.model ? { model: search.model } : {}),
    ...(search.key_id ? { key_id: search.key_id } : {}),
    ...(search.workspace_id ? { workspace_id: search.workspace_id } : {}),
    ...focusParam,
  };
}

function handleCalButton(ctx: {
  a: string | undefined;
  i: number;
  todayDay: number;
  granularity: UsageGranularity;
  cal: CalState;
  setCal: (updater: CalState | ((c: CalState) => CalState)) => void;
  updateSearch: (patch: Partial<UsageSearch>) => void;
}) {
  const { a, i, todayDay, granularity, cal, setCal, updateSearch } = ctx;
  if (a === "cnav") setCal((c) => ({ ...c, leftYm: c.leftYm + i }));
  else if (a === "cd") {
    setCal((c) =>
      c.start === null || c.end !== null
        ? { ...c, start: i, end: null, hover: null, quick: null }
        : { ...c, end: i, hover: null, quick: null },
    );
  } else if (a === "cq") {
    const q = quickPresetDays(i, todayDay);
    const { since, until } = windowFromDays(q.start, q.end);
    const days = q.end - q.start + 1;
    const nextGran = granularity === "hour" && days > MAX_HOUR_DAYS ? (days > 62 ? "week" : "day") : granularity;
    setCal((c) => ({ ...c, open: false, start: q.start, end: q.end, hover: null, quick: q.quick }));
    updateSearch({ range: "custom", since, until, granularity: nextGran });
  } else if (a === "ccancel") setCal((c) => ({ ...c, open: false, hover: null }));
  else if (a === "cok" && cal.start !== null) {
    const { since, until } = windowFromDays(cal.start, cal.end ?? cal.start);
    setCal((c) => ({ ...c, open: false, hover: null }));
    updateSearch({ range: "custom", since, until });
  }
}

function handleBodyFilterAction(ctx: {
  a: string;
  val: string;
  search: UsageSearch;
  setOpenChip: (updater: "m" | "k" | null | ((c: "m" | "k" | null) => "m" | "k" | null)) => void;
  setSortCol: (v: UsageSortCol | null) => void;
  setCal: (updater: CalState | ((c: CalState) => CalState)) => void;
  updateSearch: (patch: Partial<UsageSearch>) => void;
}) {
  const { a, val, search, setOpenChip, setSortCol, setCal, updateSearch } = ctx;
  const sortCols: Record<string, UsageSortCol> = {
    "0": "spend",
    "1": "requests",
    "3": "input",
    "4": "output",
    "5": "avg",
  };
  const toggleChip = () => {
    setCal((c) => ({ ...c, open: false, hover: null }));
    setOpenChip((cur) => (cur === val ? null : (val as "m" | "k")));
  };
  const pickModel = (clear: boolean) => {
    updateSearch({ model: clear || search.model === val ? undefined : val });
    setOpenChip(null);
  };
  const pickScope = (kind: "k" | "w" | "clear") => {
    updateSearch({
      key_id: kind === "k" && search.key_id !== val ? val : undefined,
      workspace_id: kind === "w" && search.workspace_id !== val ? val : undefined,
    });
    setOpenChip(null);
  };
  const actions: Record<string, () => void> = {
    chip: toggleChip,
    pickm: () => pickModel(false),
    clrm: () => pickModel(true),
    pickk: () => pickScope("k"),
    pickw: () => pickScope("w"),
    clrk: () => pickScope("clear"),
    gran: () => updateSearch({ granularity: val as UsageGranularity }),
    met: () => {
      updateSearch({ metric: val as UsageMetricKey });
      setSortCol(null);
    },
    grp: () => updateSearch({ group_by: val as UsageGroupBy }),
    sort: () => sortCols[val] && setSortCol(sortCols[val] ?? null),
  };
  actions[a]?.();
}

function applyRangePresetAction(params: {
  idx: number;
  now: Date;
  resolved: ReturnType<typeof resolveRange>;
  setOpenChip: (v: "m" | "k" | null) => void;
  setCal: (updater: CalState | ((c: CalState) => CalState)) => void;
  updateSearch: (patch: Partial<UsageSearch>) => void;
}) {
  const { idx, now, resolved, setOpenChip, setCal, updateSearch } = params;
  if (idx === 4) {
    setOpenChip(null);
    const maxYm = now.getUTCFullYear() * 12 + now.getUTCMonth() - 1;
    const target = resolved.endDay !== null ? new Date(resolved.endDay * DAY_MS) : now;
    const clamped = Math.min(maxYm, Math.max(maxYm - 23, target.getUTCFullYear() * 12 + target.getUTCMonth() - 1));
    setCal((c) => ({
      open: !c.open,
      leftYm: clamped,
      start: resolved.startDay,
      end: resolved.endDay,
      hover: null,
      quick: null,
    }));
    return;
  }
  const order: Exclude<UsageRangePreset, "custom">[] = ["today", "d7", "d30", "month"];
  const nextPreset = order[idx] ?? "month";
  const span = presetDaySpan(nextPreset, now);
  setCal((c) => ({ ...c, open: false, hover: null }));
  updateSearch({
    range: nextPreset,
    since: undefined,
    until: undefined,
    granularity: defaultGranularity(nextPreset, span.endDay - span.startDay + 1),
  });
}

function makeChartInteractions(params: {
  slotsLength: number;
  hoverSlot: number | null;
  setHoverSlot: (updater: number | null | ((c: number | null) => number | null)) => void;
  setHoverGroup: (v: string | null) => void;
  openSlotCalls: (idx: number) => void;
}) {
  const { slotsLength, hoverSlot, setHoverSlot, setHoverGroup, openSlotCalls } = params;
  const onChartMove = (e: ReactMouseEvent<Element>) => {
    const row = (e.target as Element).closest<HTMLElement>('tr[data-a="row"], .usrw[data-a="row"]');
    if (row) setHoverGroup(row.dataset.i ?? null);
    const hit = (e.target as Element).closest<HTMLElement>("rect.ushit[data-i]");
    const next = hit ? Number(hit.dataset.i) : null;
    if (next !== hoverSlot) setHoverSlot(next);
  };
  const onChartKeyDown = (e: ReactKeyboardEvent<Element>) => {
    if (!slotsLength) return;
    const stepKey = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
    if (stepKey !== 0) {
      e.preventDefault();
      setHoverSlot((cur) =>
        cur === null ? (stepKey > 0 ? 0 : slotsLength - 1) : Math.max(0, Math.min(slotsLength - 1, cur + stepKey)),
      );
    } else if ((e.key === "Enter" || e.key === " ") && hoverSlot !== null) {
      e.preventDefault();
      openSlotCalls(hoverSlot);
    } else if (e.key === "Escape") setHoverSlot(null);
  };
  return { onChartMove, onChartKeyDown };
}

export function useUsageHandlers(params: {
  now: Date;
  todayDay: number;
  resolved: ReturnType<typeof resolveRange>;
  search: UsageSearch;
  granularity: UsageGranularity;
  focusGroup: string | null;
  setFocusGroup: (updater: string | null | ((c: string | null) => string | null)) => void;
  setOpenChip: (updater: "m" | "k" | null | ((c: "m" | "k" | null) => "m" | "k" | null)) => void;
  setSortCol: (v: UsageSortCol | null) => void;
  cal: CalState;
  setCal: (updater: CalState | ((c: CalState) => CalState)) => void;
  hoverSlot: number | null;
  setHoverSlot: (updater: number | null | ((c: number | null) => number | null)) => void;
  setHoverGroup: (v: string | null) => void;
  groupBy: UsageGroupBy;
  slots: readonly { sinceIso: string; untilIso: string }[];
  updateSearch: (patch: Partial<UsageSearch>) => void;
}) {
  const navigate = useNavigate();
  const {
    now,
    todayDay,
    resolved,
    search,
    granularity,
    focusGroup,
    setFocusGroup,
    setOpenChip,
    setSortCol,
    cal,
    setCal,
    hoverSlot,
    setHoverSlot,
    setHoverGroup,
    groupBy,
    slots,
    updateSearch,
  } = params;

  const openSlotCalls = (idx: number) => {
    const slot = slots[idx];
    if (slot) void navigate({ to: "/calls", search: slotCallsSearch(slot, search, focusGroup, groupBy) });
  };

  const onBodyClick = (e: ReactMouseEvent<Element>) => {
    const target = (e.target as Element).closest<HTMLElement>("[data-a]");
    if (!target) return void (focusGroup !== null && setFocusGroup(null));
    const a = target.dataset.a ?? "";
    const val = target.dataset.i ?? "";
    if (a === "r") applyRangePresetAction({ idx: Number(val), now, resolved, setOpenChip, setCal, updateSearch });
    else if (a === "row") setFocusGroup((cur) => (cur === val ? null : val));
    else if (a === "day") openSlotCalls(Number(val));
    else handleBodyFilterAction({ a, val, search, setOpenChip, setSortCol, setCal, updateSearch });
  };

  const onCalClick = (e: ReactMouseEvent<Element>) => {
    const btn = (e.target as Element).closest<HTMLElement>("[data-a]");
    if (btn)
      handleCalButton({ a: btn.dataset.a, i: Number(btn.dataset.i), todayDay, granularity, cal, setCal, updateSearch });
  };

  const onCalMove = (e: ReactMouseEvent<Element>) => {
    if (cal.start === null || cal.end !== null) return;
    const btn = (e.target as Element).closest<HTMLElement>('.usdd[data-a="cd"]');
    const nextHover = btn ? Number(btn.dataset.i) : null;
    if (nextHover !== cal.hover) setCal((c) => ({ ...c, hover: nextHover }));
  };

  const chart = makeChartInteractions({
    slotsLength: slots.length,
    hoverSlot,
    setHoverSlot,
    setHoverGroup,
    openSlotCalls,
  });
  return { onBodyClick, onCalClick, onCalMove, ...chart };
}
