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

// The call list takes a window and, since #318, the model and result filters the figures drill into.
export type CallDrill = Window & { account_id?: string; model?: string; status?: "error" };

export function callsDrill(
  window: Window,
  filter: { account?: string; model?: string; status?: "error" } = {},
): CallDrill {
  const search: CallDrill = { ...window };
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

// The order list takes the same window; cash is shown against the orders it came from.
export function ordersDrill(window: Window): Window {
  return { ...window };
}

// A result filter in the address is one of the two the contract has; anything else narrows nothing.
export function callStatus(value: unknown): "ok" | "error" | undefined {
  return value === "ok" || value === "error" ? value : undefined;
}
