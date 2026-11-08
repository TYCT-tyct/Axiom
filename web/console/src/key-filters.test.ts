import type { Schemas } from "@axiom/api";
import { expect, it } from "vitest";
import { keyFiltersApplied } from "./key-filters";
import type { KeyFilters } from "./queries";

type Echo = Schemas["KeyFilterEcho"];
const page = (filters?: Partial<Echo>) => ({ filters: filters as Echo | undefined });

it.each([
  { case: "no filters, an old page", filters: {}, pages: [page()], want: true },
  { case: "a filter, an old page", filters: { status: "expired" }, pages: [page()], want: false },
  {
    case: "a filter, a matching page",
    filters: { status: "expired" },
    pages: [page({ status: "expired" })],
    want: true,
  },
  { case: "a name, a matching page", filters: { name: "alpha" }, pages: [page({ name: "alpha" })], want: true },
  { case: "a name, a different echo", filters: { name: "alpha" }, pages: [page({ name: "beta" })], want: false },
  {
    case: "another status",
    filters: { status: "expired" },
    pages: [page({ status: "active" })],
    want: false,
  },
  {
    case: "mixed nodes",
    filters: { status: "expired" },
    pages: [page({ status: "expired" }), page()],
    want: false,
  },
  {
    case: "the same instant written differently",
    filters: { created_after: "2026-01-02T03:04:05Z" },
    pages: [page({ created_after: "2026-01-02T04:04:05+01:00" })],
    want: true,
  },
  {
    case: "a workspace, a matching page",
    filters: { workspace_id: "ws_1" },
    pages: [page({ workspace_id: "ws_1" })],
    want: true,
  },
  {
    case: "a workspace, a node that ignores it",
    filters: { workspace_id: "ws_1" },
    pages: [page({ status: undefined })],
    want: false,
  },
  {
    case: "a missing bound",
    filters: { created_after: "2026-01-02T03:04:05Z" },
    pages: [page()],
    want: false,
  },
] as { case: string; filters: KeyFilters; pages: { filters?: Echo }[]; want: boolean }[])(
  "$case",
  ({ filters, pages, want }) => {
    expect(keyFiltersApplied(filters, pages)).toBe(want);
  },
);
