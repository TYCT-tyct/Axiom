import { expect, it } from "vitest";
import {
  callStatus,
  callsArchivedWhole,
  callsCoverage,
  callsDrill,
  callsEmptyKind,
  cashView,
  overviewRange,
  overviewWindows,
  paidOrdersDrill,
  retentionOrdersDrill,
} from "./overview";

it.each([
  { case: "today", value: "today", want: "today" },
  { case: "7d", value: "7d", want: "7d" },
  { case: "month", value: "month", want: "month" },
  { case: "the old day link lands on today", value: "24h", want: "today" },
  { case: "the old 30 days link lands on the month", value: "30d", want: "month" },
  { case: "an unknown range falls back to today", value: "90d", want: "today" },
  { case: "nothing yet", value: undefined, want: "today" },
])("$case", ({ value, want }) => {
  expect(overviewRange(value)).toBe(want);
});

// The figures start on a UTC day boundary and are compared with the same stretch into the period before them.
it.each([
  {
    case: "today against the same hours of yesterday, drawn over the week",
    range: "today" as const,
    current: ["2026-10-30T00:00:00.000Z", "2026-10-30T12:00:00.000Z"],
    previous: ["2026-10-29T00:00:00.000Z", "2026-10-29T12:00:00.000Z"],
    trend: ["2026-10-24T00:00:00.000Z", "2026-10-30T12:00:00.000Z"],
  },
  {
    case: "7 days against the 7 before",
    range: "7d" as const,
    current: ["2026-10-24T00:00:00.000Z", "2026-10-30T12:00:00.000Z"],
    previous: ["2026-10-17T00:00:00.000Z", "2026-10-23T12:00:00.000Z"],
    trend: ["2026-10-24T00:00:00.000Z", "2026-10-30T12:00:00.000Z"],
  },
  {
    case: "the month so far against as many days of the last",
    range: "month" as const,
    current: ["2026-10-01T00:00:00.000Z", "2026-10-30T12:00:00.000Z"],
    previous: ["2026-09-01T00:00:00.000Z", "2026-09-30T12:00:00.000Z"],
    trend: ["2026-10-01T00:00:00.000Z", "2026-10-30T12:00:00.000Z"],
  },
  {
    case: "a longer month against a shorter one stops where the shorter ends",
    range: "month" as const,
    now: "2026-03-31T18:00:00.000Z",
    current: ["2026-03-01T00:00:00.000Z", "2026-03-31T18:00:00.000Z"],
    previous: ["2026-02-01T00:00:00.000Z", "2026-03-01T00:00:00.000Z"],
    trend: ["2026-03-01T00:00:00.000Z", "2026-03-31T18:00:00.000Z"],
  },
])("$case", ({ range, now, current, previous, trend }) => {
  const windows = overviewWindows(range, new Date(now ?? "2026-10-30T12:00:00.000Z"));
  const pair = ([since, until]: string[]) => ({ since, until });
  expect(windows).toEqual({ current: pair(current ?? []), previous: pair(previous ?? []), trend: pair(trend ?? []) });
});

it("drills into the call list with the window and only the filters that narrow", () => {
  const window = { since: "2026-10-29T12:00:00.000Z", until: "2026-10-30T12:00:00.000Z" };
  expect(callsDrill(window)).toEqual(window);
  expect(callsDrill(window, { site: "site_1", account: "acct_1", model: "m", status: "error" })).toEqual({
    ...window,
    site_id: "site_1",
    account_id: "acct_1",
    model: "m",
    status: "error",
  });
  expect(cashView("1")).toBe("1");
  expect(cashView('"1"')).toBe("1");
  expect(cashView(1)).toBe("1");
  expect(cashView("ledger")).toBeUndefined();
  expect(paidOrdersDrill(window)).toEqual({ ...window, paid: "batches" });
});

it("opens retention at the previous UTC month's cohort", () => {
  expect(retentionOrdersDrill({ since: "2026-10-29T12:00:00.000Z", until: "2026-10-30T12:00:00.000Z" })).toEqual({
    since: "2026-09-01T00:00:00.000Z",
    until: "2026-10-30T12:00:00.000Z",
    paid: "batches",
  });
  // The window ends at midnight on the first: its last instant is still in the previous month.
  expect(retentionOrdersDrill({ since: "2026-10-25T00:00:00.000Z", until: "2026-11-01T00:00:00.000Z" })).toEqual({
    since: "2026-09-01T00:00:00.000Z",
    until: "2026-11-01T00:00:00.000Z",
    paid: "batches",
  });
});

it("reads only the results the contract has", () => {
  expect(callStatus("ok")).toBe("ok");
  expect(callStatus("error")).toBe("error");
  expect(callStatus("cancelled")).toBeUndefined();
  expect(callStatus(undefined)).toBeUndefined();
});

it("reads the call list's coverage across its pages", () => {
  expect(callsCoverage([{ incomplete: false }, { incomplete: false }])).toBe(false);
  expect(callsCoverage([{ incomplete: false }, { incomplete: true }])).toBe(true);
  // An older node does not answer the field, so a list it answered part of cannot be called complete.
  expect(callsCoverage([{ incomplete: false }, {}])).toBeUndefined();
  expect(callsCoverage([])).toBe(false);
});

it("calls a window wholly archived only when its end is at or before the boundary", () => {
  expect(callsArchivedWhole(true, "2026-11-01T00:00:00.000Z", "2026-11-01T00:00:00.000Z")).toBe(true);
  expect(callsArchivedWhole(true, "2026-11-01T00:00:00.000Z", "2026-10-31T00:00:00.000Z")).toBe(true);
  expect(callsArchivedWhole(true, "2026-11-01T00:00:00.000Z", "2026-12-15T00:00:00.000Z")).toBe(false);
  // No monthly partition left: the node has archived every call it could have answered.
  expect(callsArchivedWhole(true, undefined, "2026-12-15T00:00:00.000Z")).toBe(true);
  expect(callsArchivedWhole(false, "2026-11-01T00:00:00.000Z", "2026-12-15T00:00:00.000Z")).toBe(false);
  expect(callsArchivedWhole(undefined, "2026-11-01T00:00:00.000Z", "2026-12-15T00:00:00.000Z")).toBe(false);
});

it("does not describe a partly archived empty list as archived", () => {
  expect(callsEmptyKind(true, false, false)).toBe("retainedWindow");
  expect(callsEmptyKind(true, false, true)).toBe("retainedFiltered");
  expect(callsEmptyKind(true, true, true)).toBe("archived");
  expect(callsEmptyKind(false, false, true)).toBe("filtered");
  expect(callsEmptyKind(undefined, false, false)).toBe("window");
});
