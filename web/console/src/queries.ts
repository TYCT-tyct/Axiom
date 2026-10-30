import { consoleApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import type { ModelFilters } from "@axiom/ui";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";

// The calls the pages make, one place for each so a page and the one that changes its data agree on the key. Everything
// of the signed-in account is under accountKey, which the end of a session clears.

export const siteQuery = queryOptions({
  queryKey: ["site"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/site", { signal })),
  staleTime: 5 * 60_000,
});

export const passkeysQuery = queryOptions({
  queryKey: [accountKey, "passkeys"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/auth/passkeys", { signal })),
});

export const identitiesQuery = queryOptions({
  queryKey: [accountKey, "identities"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/auth/identities", { signal })),
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

export const statementQuery = infiniteQueryOptions({
  queryKey: [accountKey, "statement"],
  queryFn: ({ pageParam, signal }) =>
    unwrap(
      consoleApi.GET("/api/console/v1/statement", {
        params: { query: { cursor: pageParam, limit: pageSize } },
        signal,
      }),
    ),
  initialPageParam: undefined as string | undefined,
  getNextPageParam: nextCursor,
});

// What the key list can be narrowed by; an empty field does not narrow. A cursor is only good for the filters it was
// given with, so the filters are part of the key.
export type KeyFilters = {
  name?: string;
  prefix?: string;
  status?: "active" | "disabled" | "expired";
  created_after?: string;
  created_before?: string;
};

export const keysQuery = (filters: KeyFilters = {}) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "keys", filters],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        consoleApi.GET("/api/console/v1/keys", {
          params: { query: { ...filters, cursor: pageParam, limit: pageSize } },
          signal,
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: nextCursor,
  });

// The public models and prices page. The console asks the account-priced projection, so a user sees the price a call
// would be charged at; the website asks the public one.
export type { ModelFilters } from "@axiom/ui";

export const modelsQuery = (filters: ModelFilters) =>
  queryOptions({
    queryKey: [accountKey, "models", filters],
    queryFn: ({ signal }) =>
      unwrap(consoleApi.GET("/api/console/v1/models/account", { params: { query: filters }, signal })),
  });

// What the call list can be narrowed by; an empty field does not narrow. The cursor is only good for the filters it was
// given with, so the filters are part of the key.
export type CallFilters = {
  since?: string;
  until?: string;
  model?: string;
  client_app?: string;
  key_id?: string;
  workspace_id?: string;
  status?: "success" | "error";
  request_id?: string;
};
export type CallGroupBy = "day" | "model" | "key";

// The summary of the same filters, grouped for the page. It covers at most 31 days and defaults to the last 30, so its
// window is shown beside the table.
export const callAggregateQuery = (filters: CallFilters, groupBy: CallGroupBy) =>
  queryOptions({
    queryKey: [accountKey, "calls", "aggregate", filters, groupBy],
    queryFn: ({ signal }) =>
      unwrap(
        consoleApi.GET("/api/console/v1/calls/aggregate", {
          params: { query: { ...filters, group_by: groupBy } },
          signal,
        }),
      ),
  });

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

export const recentErrorsQuery = queryOptions({
  queryKey: [accountKey, "recent-errors"],
  queryFn: ({ signal }) =>
    unwrap(consoleApi.GET("/api/console/v1/calls", { params: { query: { status: "error", limit: 5 } }, signal })),
});

export const callQuery = (id: string) =>
  queryOptions({
    queryKey: [accountKey, "call", id],
    queryFn: ({ signal }) =>
      unwrap(consoleApi.GET("/api/console/v1/calls/{call_id}", { params: { path: { call_id: id } }, signal })),
  });

// The top-up orders, newest first, with each one's refunds. The page that asks for refunds shares the key.
export const ordersQuery = infiniteQueryOptions({
  queryKey: [accountKey, "orders"],
  queryFn: ({ pageParam, signal }) =>
    unwrap(
      consoleApi.GET("/api/console/v1/orders", {
        params: { query: { cursor: pageParam, limit: pageSize } },
        signal,
      }),
    ),
  initialPageParam: undefined as string | undefined,
  getNextPageParam: nextCursor,
});

// One order, polled while it waits for payment so the balance page notices a payment that arrives without the browser
// coming back through the channel's return page.
export const orderQuery = (id: string) =>
  queryOptions({
    queryKey: [accountKey, "order", id],
    queryFn: ({ signal }) =>
      unwrap(consoleApi.GET("/api/console/v1/orders/{order_id}", { params: { path: { order_id: id } }, signal })),
    refetchInterval: (query) => (query.state.data?.state === "pending" ? 15_000 : false),
  });

// What an amount costs through each method. The amount is part of the key: a quote is only good for the amount it was
// asked for. No amount means no request.
export const paymentMethodsQuery = (credits: string) =>
  queryOptions({
    queryKey: [accountKey, "payment-methods", credits],
    queryFn: ({ signal }) =>
      unwrap(consoleApi.GET("/api/console/v1/payment-methods", { params: { query: { credits } }, signal })),
    enabled: credits !== "",
  });

export const notificationPreferencesQuery = queryOptions({
  queryKey: [accountKey, "notification-preferences"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/notification-preferences", { signal })),
});
