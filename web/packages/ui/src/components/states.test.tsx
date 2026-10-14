import { isValidElement, type ReactNode } from "react";
import { expect, it } from "vitest";
import { DataState, EmptyState } from "./states";

const loaded = (data: string[]) => ({
  status: "success" as const,
  data,
  error: null,
  errorUpdatedAt: 0,
  refetch: () => {},
});
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
