// The dashboard's ranges and the links each figure opens: one place for the window arithmetic and for the filters the
// call list and the order list understand, so a figure and its drill-down cannot disagree.

export const overviewRanges = ["today", "7d", "month"] as const;
export type OverviewRange = (typeof overviewRanges)[number];

// A range in the address is one of the three; the rolling ranges of the earlier dashboard land on the nearest one, and
// anything else is today.
export function overviewRange(value: unknown): OverviewRange {
  if (value === "24h") {
    return "today";
  }
  if (value === "30d") {
    return "month";
  }
  return overviewRanges.includes(value as OverviewRange) ? (value as OverviewRange) : "today";
}

export type Window = { since: string; until: string };

const day = 24 * 60 * 60 * 1000;
const dayStart = (at: Date) => Date.UTC(at.getUTCFullYear(), at.getUTCMonth(), at.getUTCDate());
const iso = (since: number, until: number): Window => ({
  since: new Date(since).toISOString(),
  until: new Date(until).toISOString(),
});

// What the dashboard asks the API for. The figures cover `current`, which ends at the visit and begins on a UTC day
// boundary, the grid the API's days and the targets' months are on: today, the last 7 UTC days, or the UTC month so far.
// `previous` is the same stretch before it, as far into itself as `current` is into its own (the same hours of
// yesterday, the 7 days before, the same days of last month), so a half-gone day is compared with half a day.
// `trend` is what the trend lines and the charts draw: a single day has no trend, so today is drawn over the last 7 days.
export type OverviewWindows = { current: Window; previous: Window; trend: Window };

export function overviewWindows(range: OverviewRange, now: Date): OverviewWindows {
  const at = now.getTime();
  const today = dayStart(now);
  if (range === "month") {
    const from = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1);
    const before = Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - 1, 1);
    const current = iso(from, at);
    return { current, previous: iso(before, Math.min(before + (at - from), from)), trend: current };
  }
  const week = iso(today - 6 * day, at);
  if (range === "7d") {
    return { current: week, previous: iso(today - 13 * day, at - 7 * day), trend: week };
  }
  return { current: iso(today, at), previous: iso(today - day, at - day), trend: week };
}

// The call list takes a window and, since #318, the site, model and result filters the figures drill into. The site is
// what keeps a site's dashboard from opening every site's calls on a platform-wide admin.
export type CallDrill = Window & { site_id?: string; account_id?: string; model?: string; status?: "error" };

export function callsDrill(
  window: Window,
  filter: { site?: string; account?: string; model?: string; status?: "error" } = {},
): CallDrill {
  const search: CallDrill = { ...window };
  if (filter.site) {
    search.site_id = filter.site;
  }
  if (filter.account) {
    search.account_id = filter.account;
  }
  if (filter.model) {
    search.model = filter.model;
  }
  if (filter.status) {
    search.status = filter.status;
  }
  return search;
}

// The orders page has two list views beside the orders themselves: cash movements by ledger time, and paid batches by
// the batch's created_at. The paid view is the records behind paid users and monthly revenue. Both use the dashboard's
// window directly; neither filters orders by an order's creation time.
export type OrderDrill = Window & { cash?: "1"; paid?: "batches" };

// The router serializes the string "1" with JSON quotes in the address. Both the quoted form and a plain 1 mean the
// cash view; the old page wrote and read cash=1, so that spelling stays the contract.
export function cashView(value: unknown): "1" | undefined {
  return value === "1" || value === 1 || value === '"1"' ? "1" : undefined;
}

export function paidOrdersDrill(window: Window): OrderDrill {
  return { ...window, paid: "batches" };
}

// Retention compares last month's paying accounts with this month's payments. The list therefore starts at the first
// day of the UTC cohort month and ends at the dashboard's window end, so both payments it counts are visible.
export function retentionOrdersDrill(window: Window): OrderDrill {
  const end = new Date(Date.parse(window.until) - 1);
  const cohort = new Date(Date.UTC(end.getUTCFullYear(), end.getUTCMonth() - 1, 1));
  return { since: cohort.toISOString(), until: window.until, paid: "batches" };
}

// A result filter in the address is one of the two the contract has; anything else narrows nothing.
export function callStatus(value: unknown): "ok" | "error" | undefined {
  return value === "ok" || value === "error" ? value : undefined;
}

// The call list's coverage as its pages report it: false when every loaded page covers the window, true when one says
// calls before its available_since were archived, and undefined when an older node did not say. An unsure list must say
// it does not know rather than show an empty window as complete.
export function callsCoverage(pages: readonly { incomplete?: boolean }[]): boolean | undefined {
  if (pages.some((page) => page.incomplete === true)) {
    return true;
  }
  if (pages.some((page) => page.incomplete === undefined)) {
    return undefined;
  }
  return false;
}

// callsArchivedWhole reports whether every call the window could contain is before the oldest retained month. A node
// with no monthly partition left reports no boundary and has archived everything; a missing boundary on an old node is
// not that case, because callsCoverage already marks it unsure.
export function callsArchivedWhole(
  incomplete: boolean | undefined,
  availableSince: string | undefined,
  until: string | undefined,
): boolean {
  if (incomplete !== true) {
    return false;
  }
  if (availableSince === undefined) {
    return true;
  }
  if (until === undefined) {
    return false;
  }
  return Date.parse(until) <= Date.parse(availableSince);
}

// callsEmptyKind picks the explanation under an empty call list. A list that reaches archived calls may be only partly
// archived, so it must not say the whole window was archived; with a filter that matched nothing in the retained
// months, it cannot claim the missing calls were filtered away either.
export type CallsEmptyKind = "archived" | "retainedFiltered" | "retainedWindow" | "filtered" | "window";

export function callsEmptyKind(
  incomplete: boolean | undefined,
  wholeArchived: boolean,
  filtered: boolean,
): CallsEmptyKind {
  if (incomplete === true) {
    return wholeArchived ? "archived" : filtered ? "retainedFiltered" : "retainedWindow";
  }
  return filtered ? "filtered" : "window";
}
