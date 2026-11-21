import { type AdminSchemas, adminApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { parseJSONLossless } from "./lossless";

const base = "/api/admin/v1";

export const alertEventsQuery = infiniteQueryOptions({
  queryKey: [accountKey, "alert-events"],
  queryFn: ({ signal, pageParam }) =>
    unwrap(adminApi.GET(`${base}/alerts/events`, { signal, params: { query: { before: pageParam } } })),
  initialPageParam: undefined as string | undefined,
  getNextPageParam: ({ data }) => (data?.length === 100 ? data.at(-1)?.id : undefined),
});

export const alertConfigurationQuery = queryOptions({
  queryKey: [accountKey, "alert-configuration"],
  // Spend detection's floor is an int64. Replacing the rules must preserve its exact value.
  queryFn: async ({ signal }) =>
    parseJSONLossless(
      await unwrap(adminApi.GET(`${base}/alerts/configuration`, { signal, parseAs: "text" })),
    ) as AdminSchemas["AlertConfiguration"],
});

export const legalDocumentQuery = (siteId: string) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "legal-documents", siteId],
    queryFn: ({ signal, pageParam }) =>
      unwrap(
        adminApi.GET(`${base}/sites/{site_id}/legal-documents`, {
          signal,
          params: { path: { site_id: siteId }, query: { cursor: pageParam, limit: 50 } },
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: ({ next_cursor }) => next_cursor ?? undefined,
  });

export const logRetentionQuery = queryOptions({
  queryKey: [accountKey, "log-retention"],
  queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/retention`, { signal })),
});

export const rateTargets = ["tier", "account", "key"] as const;
export type RateTarget = { target: (typeof rateTargets)[number]; id: string };

export const rateLimitQuery = (path: RateTarget) =>
  queryOptions({
    queryKey: [accountKey, "rate-limit", path],
    queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/rate-limits/{target}/{id}`, { signal, params: { path } })),
  });
