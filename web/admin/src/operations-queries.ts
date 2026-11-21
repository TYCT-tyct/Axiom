import { adminApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";

const pageSize = 50;
const base = "/api/admin/v1";
const firstPage = undefined as string | undefined;

export const operationsCredentialsQuery = infiniteQueryOptions({
  queryKey: [accountKey, "operations-credentials"],
  queryFn: ({ pageParam, signal }) =>
    unwrap(
      adminApi.GET(`${base}/operations/credentials`, {
        params: { query: { cursor: pageParam, limit: pageSize } },
        signal,
      }),
    ),
  initialPageParam: firstPage,
  getNextPageParam: (page) => page.next_cursor,
});

export const operationsCapPoliciesQuery = queryOptions({
  queryKey: [accountKey, "operations-cap-policies"],
  queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/operations/cap-policies`, { signal })),
});

export const operationsAgentLimitsQuery = queryOptions({
  queryKey: [accountKey, "operations-agent-limits"],
  queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/operations/agent-limits`, { signal })),
});

export const operationsPriceEnvelopesQuery = queryOptions({
  queryKey: [accountKey, "operations-price-envelopes"],
  queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/operations/price-envelopes`, { signal })),
});
