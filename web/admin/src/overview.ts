// The dashboard's ranges and the links each figure opens: one place for the window arithmetic and for the filters the
// call list and the order list understand, so a figure and its drill-down cannot disagree.

export const overviewRanges = ["24h", "7d", "30d"] as const;
export type OverviewRange = (typeof overviewRanges)[number];

// A range in the address is one of the three, and anything else is the default.
export function overviewRange(value: unknown): OverviewRange {
  return overviewRanges.includes(value as OverviewRange) ? (value as OverviewRange) : "24h";
}

const span: Record<OverviewRange, number> = {
  "24h": 24 * 60 * 60 * 1000,
  "7d": 7 * 24 * 60 * 60 * 1000,
  "30d": 30 * 24 * 60 * 60 * 1000,
};

export type Window = { since: string; until: string };

// overviewWindow is the range as the API takes it, ending now. The page computes it once per visit, so the figures and
// every link they open describe the same window.
export function overviewWindow(range: OverviewRange, now: Date): Window {
  return { since: new Date(now.getTime() - span[range]).toISOString(), until: now.toISOString() };
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

export function ordersDrill(window: Window): OrderDrill {
  return { ...window, cash: "1" };
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
