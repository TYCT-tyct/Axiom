import { type AdminSchemas, adminApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { parseJSONLossless } from "./lossless";

const base = "/api/admin/v1";

export const discrepancyStates = ["open", "resolved"] as const;
type DiscrepancyState = (typeof discrepancyStates)[number];

export const discrepanciesQuery = (state: DiscrepancyState | undefined) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "payment-discrepancies", state],
    queryFn: ({ signal, pageParam }) =>
      unwrap(
        adminApi.GET(`${base}/payments/discrepancies`, { signal, params: { query: { before: pageParam, state } } }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: ({ data }) => (data?.length === 100 ? data.at(-1)?.id : undefined),
  });

export const discrepancyQuery = (id: string) =>
  queryOptions({
    queryKey: [accountKey, "payment-discrepancy", id],
    // Reconciliation facts can carry int64 amounts in JSON. Keep the exact digits.
    queryFn: async ({ signal }) =>
      parseJSONLossless(
        await unwrap(
          adminApi.GET(`${base}/payments/discrepancies/{discrepancy_id}`, {
            signal,
            parseAs: "text",
            params: { path: { discrepancy_id: id } },
          }),
        ),
      ) as AdminSchemas["PaymentDiscrepancy"],
  });

export const exchangeRatesQuery = queryOptions({
  queryKey: [accountKey, "exchange-rates"],
  queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/pricing/exchange-rates`, { signal })),
});

export const exchangeHistoryQuery = (pair: { base: string; quote: string }) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "exchange-history", pair],
    queryFn: ({ signal, pageParam }) =>
      unwrap(
        adminApi.GET(`${base}/pricing/exchange-rates/{base}/{quote}`, {
          signal,
          params: { path: pair, query: { before_version: pageParam } },
        }),
      ),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: ({ data }) => {
      const version = data.at(-1)?.version;
      return data.length === 100 && version && version > 1 ? version : undefined;
    },
  });

export const platformGrantsQuery = (userId: string) =>
  queryOptions({
    queryKey: [accountKey, "platform-grants", userId],
    queryFn: ({ signal }) =>
      unwrap(
        adminApi.GET(`${base}/users/{user_id}/platform-grants`, { signal, params: { path: { user_id: userId } } }),
      ),
  });
