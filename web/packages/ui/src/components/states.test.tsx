import { isValidElement, type ReactNode } from "react";
import { expect, it } from "vitest";
import { DataState, EmptyState, LoadingState } from "./states";

const loaded = (data: string[]) => ({
  status: "success" as const,
  data,
  error: null,
  errorUpdatedAt: 0,
  refetch: () => {},
});
const pending = { status: "pending" as const, data: undefined, error: null, errorUpdatedAt: 0, refetch: () => {} };
const show = (data: string[]) => `${data.length} rows`;
const isEmpty = (data: string[]) => data.length === 0;

it.each([
  { case: "empty data, default empty state", data: [], empty: undefined, want: "EmptyState" },
  { case: "empty data, custom empty state", data: [], empty: "nothing yet", want: "nothing yet" },
  { case: "data", data: ["a"], empty: "nothing yet", want: "1 rows" },
])("$case", ({ data, empty, want }) => {
  const shown: ReactNode = DataState({ query: loaded(data), isEmpty, empty, children: show });
  expect(isValidElement(shown) && shown.type === EmptyState ? "EmptyState" : shown).toBe(want);
});

// A state that replaces the page gives the page its heading, loading included; one inside a section leaves the heading
// to the page (docs/frontend.md「工程要求」, #363). What the heading is made of is the end-to-end test's to check.
it.each([
  { case: "loading", query: pending, state: LoadingState },
  { case: "empty", query: loaded([]), state: EmptyState },
])("a $case state takes its placement from where it stands", ({ query, state }) => {
  const page = (placement: { page?: boolean }) => {
    const shown: ReactNode = DataState({ query, isEmpty, children: show, ...placement });
    expect(isValidElement(shown) && shown.type).toBe(state);
    return isValidElement(shown) && (shown.props as { page?: boolean }).page;
  };
  expect(page({ page: true })).toBe(true);
  expect(page({})).toBeUndefined();
});
