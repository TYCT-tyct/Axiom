import { type AdminSchemas, adminApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { queryOptions } from "@tanstack/react-query";

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
