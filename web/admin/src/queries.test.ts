import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { nextWindow, recheck, tiersQuery, whileDecidable } from "./queries";

it.each<{ case: string; page: { next_cursor: string | null; since?: string; until?: string }; want: unknown }>([
  {
    case: "a following page keeps the window the server answered with",
    page: { next_cursor: "c2", since: "2026-10-01T00:00:00Z", until: "2026-10-02T00:00:00Z" },
    want: { cursor: "c2", since: "2026-10-01T00:00:00Z", until: "2026-10-02T00:00:00Z" },
  },
  {
    case: "an old node that omits the window leaves the filter to the first page",
    page: { next_cursor: "c2" },
    want: { cursor: "c2", since: undefined, until: undefined },
  },
  { case: "the last page asks for nothing", page: { next_cursor: null }, want: undefined },
])("$case", ({ page, want }) => {
  expect(nextWindow(page)).toEqual(want);
});

it("keeps the tiers of different sites apart", () => {
  const first = tiersQuery("site_00000000000000000000000001");
  const second = tiersQuery("site_00000000000000000000000002");
  expect(first.queryKey).not.toEqual(second.queryKey);
});

// The approval list asks again while any approval on it can still be decided, as the server said, and stops after.
it.each([
  [[], false],
  [[{ state: "pending", decidable: false }], false],
  [
    [
      { state: "approved", decidable: false },
      { state: "pending", decidable: true },
    ],
    recheck,
  ],
] as const)("asks again for %o: %s", (approvals, want) => {
  expect(whileDecidable(approvals as unknown as AdminSchemas["OperationsApproval"][])).toBe(want);
});
