import { consoleApi, unwrap } from "@axiom/api";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { accountKey } from "./session";

// The calls the pages make, one place for each so a page and the one that changes its data agree on the key. Everything
// of the signed-in account is under accountKey, which the end of a session clears.

export const siteQuery = queryOptions({
  queryKey: ["site"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/site", { signal })),
  staleTime: 5 * 60_000,
});

export const balanceQuery = queryOptions({
  queryKey: [accountKey, "balance"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/balance", { signal })),
});

const pageSize = 50;

// A page of a list is asked for with the cursor the previous page gave, unchanged; the last page has none.
function nextCursor(page: { next_cursor: string | null }) {
  return page.next_cursor ?? undefined;
}

export const keysQuery = infiniteQueryOptions({
  queryKey: [accountKey, "keys"],
  queryFn: ({ pageParam, signal }) =>
    unwrap(
      consoleApi.GET("/api/console/v1/keys", { params: { query: { cursor: pageParam, limit: pageSize } }, signal }),
    ),
  initialPageParam: undefined as string | undefined,
  getNextPageParam: nextCursor,
});

// What the call list can be narrowed by; an empty field does not narrow. The cursor is only good for the filters it was
// given with, so the filters are part of the key.
export type CallFilters = { since?: string; until?: string; model?: string; client_app?: string };

export const callsQuery = (filters: CallFilters) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "calls", filters],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        consoleApi.GET("/api/console/v1/calls", {
          params: { query: { ...filters, cursor: pageParam, limit: pageSize } },
          signal,
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: nextCursor,
  });

export const callQuery = (id: string) =>
  queryOptions({
    queryKey: [accountKey, "call", id],
    queryFn: ({ signal }) =>
      unwrap(consoleApi.GET("/api/console/v1/calls/{call_id}", { params: { path: { call_id: id } }, signal })),
  });
