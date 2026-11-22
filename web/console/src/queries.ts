import { consoleApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import type { ModelFilters } from "@axiom/ui";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";

// The calls the pages make, one place for each so a page and the one that changes its data agree on the key. Everything
// of the signed-in account is under accountKey, which the end of a session clears.

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
  workspace_id?: string;
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

// The routing presets a key of the account may be bound to, the account's default for new keys, and the source classes
// a rule can name. A change to a preset or the default makes the presets page and the key form ask again; the classes
// are the platform's and change rarely.
export const routingPresetsQuery = queryOptions({
  queryKey: [accountKey, "routing-presets"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/routing-presets", { signal })),
});

export const routingDefaultQuery = queryOptions({
  queryKey: [accountKey, "routing-presets", "default"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/routing-default", { signal })),
});

// The presets a key of one workspace may choose and the default a key made there takes. The account's default workspace
// is what the two reads above answer; another workspace has presets of its own and a default chain that starts at it,
// so it is asked for by name.
export function workspacePresetQueries(workspace: string | undefined) {
  if (!workspace) {
    return { presets: routingPresetsQuery, defaults: routingDefaultQuery };
  }
  return {
    presets: queryOptions({
      queryKey: [accountKey, "routing-presets", "workspace", workspace],
      queryFn: ({ signal }) =>
        unwrap(
          consoleApi.GET("/api/console/v1/routing-presets", { params: { query: { workspace_id: workspace } }, signal }),
        ),
    }),
    defaults: queryOptions({
      queryKey: [accountKey, "routing-presets", "default", workspace],
      queryFn: ({ signal }) =>
        unwrap(
          consoleApi.GET("/api/console/v1/workspaces/{workspace_id}/routing-default", {
            params: { path: { workspace_id: workspace } },
            signal,
          }),
        ),
    }),
  };
}

export const sourceClassesQuery = queryOptions({
  queryKey: [accountKey, "source-classes"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/source-classes", { signal })),
  staleTime: 5 * 60_000,
});

// The account's workspaces, the whole list (it has a limit and no pages). The keys page, the key form and the menu read
// it: workspaces show only once there is a second one (docs/domains/accounts.md「工作区」).
export const workspacesQuery = queryOptions({
  queryKey: [accountKey, "workspaces"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/workspaces", { signal })),
});

export const workspaceBudgetQuery = (id: string) =>
  queryOptions({
    queryKey: [accountKey, "workspaces", id, "budget"],
    queryFn: ({ signal }) =>
      unwrap(
        consoleApi.GET("/api/console/v1/workspaces/{workspace_id}/budget", {
          params: { path: { workspace_id: id } },
          signal,
        }),
      ),
  });

export const providerKeysQuery = queryOptions({
  queryKey: [accountKey, "provider-keys"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/provider-keys", { signal })),
});

export const redemptionsQuery = infiniteQueryOptions({
  queryKey: [accountKey, "redemptions"],
  queryFn: ({ pageParam, signal }) =>
    unwrap(
      consoleApi.GET("/api/console/v1/redeem-codes", {
        params: { query: { cursor: pageParam, limit: pageSize } },
        signal,
      }),
    ),
  initialPageParam: undefined as string | undefined,
  getNextPageParam: (page) => page.next_cursor ?? undefined,
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
  fell_back?: boolean;
  request_id?: string;
};
export type CallGroupBy = "day" | "model" | "key";
export type UsageGranularity = "hour" | "day" | "week";
export type UsageGroupBy = "model" | "key" | "workspace";
export type UsageFilters = {
  since?: string;
  until?: string;
  model?: string;
  key_id?: string;
  workspace_id?: string;
  granularity?: UsageGranularity;
  group_by?: UsageGroupBy;
  compare?: "previous";
};

// The summary of the same filters, grouped for the page. It covers at most 31 days and defaults to the last 30, so its
// window is shown beside the table.
export const callAggregateQuery = (filters: CallFilters, groupBy: CallGroupBy) => {
  const { fell_back: _, ...query } = filters;
  return queryOptions({
    queryKey: [accountKey, "calls", "aggregate", query, groupBy],
    queryFn: ({ signal }) =>
      unwrap(
        consoleApi.GET("/api/console/v1/calls/aggregate", {
          params: { query: { ...query, group_by: groupBy } },
          signal,
        }),
      ),
  });
};

// Hourly rollups for /usage: up to 90 days for day/week granularity, or 7 days for hour granularity, with UTC whole-hour bounds.
export const usageStatisticsQuery = (filters: UsageFilters, enabled = true) =>
  queryOptions({
    queryKey: [accountKey, "usage", "statistics", filters],
    queryFn: ({ signal }) =>
      unwrap(
        consoleApi.GET("/api/console/v1/usage/statistics", {
          params: { query: filters },
          signal,
        }),
      ),
    enabled,
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

type SessionPageParam = { cursor: string; since?: string; until?: string };

export const callSessionsQuery = (filters: CallFilters) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "calls", "sessions", filters],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        consoleApi.GET("/api/console/v1/calls/sessions", {
          params: {
            query: {
              ...filters,
              since: pageParam?.since ?? filters.since,
              until: pageParam?.until ?? filters.until,
              cursor: pageParam?.cursor,
              limit: pageSize,
            },
          },
          signal,
        }),
      ),
    initialPageParam: undefined as SessionPageParam | undefined,
    getNextPageParam: (last) =>
      last.next_cursor ? { cursor: last.next_cursor, since: last.since, until: last.until } : undefined,
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

// The order as its receipt shows it. It has its own cache entry and always reads: a mutation writes the answer it got
// into the order entry above, and that answer does not carry the refunds, which a receipt cannot do without.
export const receiptQuery = (id: string) =>
  queryOptions({
    queryKey: [accountKey, "receipt", id],
    queryFn: ({ signal }) =>
      unwrap(consoleApi.GET("/api/console/v1/orders/{order_id}", { params: { path: { order_id: id } }, signal })),
    staleTime: 0,
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

export const billingPreferenceQuery = queryOptions({
  queryKey: [accountKey, "billing-preference"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/billing-preference", { signal })),
});

export const notificationPreferencesQuery = queryOptions({
  queryKey: [accountKey, "notification-preferences"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/notification-preferences", { signal })),
});
