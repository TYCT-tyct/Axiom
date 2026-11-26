import { type AdminSchemas, ApiError, adminApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";

const base = "/api/admin/v1";

export const subscriptionPlansQuery = (includeInactive = false) =>
  queryOptions({
    queryKey: [accountKey, "subscription-plans", includeInactive],
    queryFn: ({ signal }) =>
      unwrap(
        adminApi.GET(`${base}/subscription-plans`, {
          params: { query: { include_inactive: includeInactive } },
          signal,
        }),
      ),
  });

export const accountSubscriptionsQuery = (id: AdminSchemas["AccountId"]) =>
  queryOptions({
    queryKey: [accountKey, "account-subscriptions", id],
    queryFn: ({ signal }) =>
      unwrap(
        adminApi.GET(`${base}/accounts/{account_id}/subscriptions`, {
          params: { path: { account_id: id } },
          signal,
        }),
      ),
  });

// Every account's subscriptions, newest first, as 订阅管理 lists them. A state narrows on the server, and so does one
// account (the search holds a whole account ID). A cursor is good only for the filters it was given with, so the
// filters are part of the key.
export type PlatformSubscriptionFilters = {
  state?: AdminSchemas["SubscriptionStanding"];
  account_id?: AdminSchemas["AccountId"];
};

export const platformSubscriptionsQuery = (filters: PlatformSubscriptionFilters) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "platform-subscriptions", filters],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/subscriptions`, {
          params: { query: { ...filters, cursor: pageParam, limit: 50 } },
          signal,
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.next_cursor ?? undefined,
    retry: false,
  });

// What the site's subscriptions count and sold for this UTC month, which also needs sites.manage. A node whose backend
// has not landed answers 501, and the page leaves the figures out (isNotImplemented).
export const subscriptionSummaryQuery = queryOptions({
  queryKey: [accountKey, "subscription-summary"],
  queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/subscriptions/summary`, { signal })),
  retry: false,
});

// A new operation answers 501 until its backend lands: the part of a page that depends on it is then left out, not shown
// as a failure, and lights up when the answer comes.
export const isNotImplemented = (error: unknown) => error instanceof ApiError && error.status === 501;
