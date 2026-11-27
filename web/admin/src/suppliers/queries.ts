import { type AdminSchemas, adminApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { retry } from "../monitor-queries";

// The reads of the upstream pages beyond a supplier and its list (queries.ts): the queue, the credentials across
// suppliers, a supplier's piles of names, its sync runs and reconciliation, and an endpoint's cost. A node of an
// earlier release answers 404 or 501 for them, and the section that shows one is then left out (notBuilt), so each
// gives up at once on that answer.

const base = "/api/admin/v1";
const firstPage = undefined as string | undefined;
const nextCursor = (page: { next_cursor: string | null }) => page.next_cursor ?? undefined;

export type CredentialFilters = { supplier_id?: string; mode?: AdminSchemas["SupplierMode"] };

export const credentialsListQuery = (filters: CredentialFilters) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "supplier-credentials", "list", filters],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/supplier-credentials`, {
          params: { query: { ...filters, cursor: pageParam, limit: 100 } },
          signal,
        }),
      ),
    initialPageParam: firstPage,
    getNextPageParam: nextCursor,
    retry,
  });

export const inboxQuery = infiniteQueryOptions({
  queryKey: [accountKey, "supplier-inbox"],
  queryFn: ({ pageParam, signal }) =>
    unwrap(adminApi.GET(`${base}/supplier-inbox`, { params: { query: { cursor: pageParam, limit: 100 } }, signal })),
  initialPageParam: firstPage,
  getNextPageParam: nextCursor,
  retry,
});

export const supplierModelsQuery = (supplierId: string, pile: AdminSchemas["SupplierModelPile"]) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "supplier-models", supplierId, pile],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/suppliers/{supplier_id}/models`, {
          params: { path: { supplier_id: supplierId }, query: { pile, cursor: pageParam, limit: 200 } },
          signal,
        }),
      ),
    initialPageParam: firstPage,
    getNextPageParam: nextCursor,
    retry,
  });

export const syncRunsQuery = (supplierId: string) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "supplier-sync-runs", supplierId],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/suppliers/{supplier_id}/sync-runs`, {
          params: { path: { supplier_id: supplierId }, query: { cursor: pageParam, limit: 20 } },
          signal,
        }),
      ),
    initialPageParam: firstPage,
    getNextPageParam: nextCursor,
    retry,
  });

export const reconciliationQuery = (supplierId: string) =>
  queryOptions({
    queryKey: [accountKey, "supplier-reconciliation", supplierId],
    queryFn: ({ signal }) =>
      unwrap(
        adminApi.GET(`${base}/suppliers/{supplier_id}/reconciliation`, {
          params: { path: { supplier_id: supplierId }, query: { days: 14 } },
          signal,
        }),
      ),
    retry,
  });

export const endpointCostQuery = (supplierId: string, endpointId: string) =>
  queryOptions({
    queryKey: [accountKey, "endpoint-cost", endpointId],
    queryFn: ({ signal }) =>
      unwrap(
        adminApi.GET(`${base}/suppliers/{supplier_id}/endpoints/{endpoint_id}/cost`, {
          params: { path: { supplier_id: supplierId, endpoint_id: endpointId } },
          signal,
        }),
      ),
    retry,
  });

// The channel types the node can connect (listSupplierTypes): the registry's, which change only with a release.
export const supplierTypesQuery = queryOptions({
  queryKey: [accountKey, "supplier-types"],
  queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/supplier-types`, { signal })),
  staleTime: 5 * 60_000,
  retry,
});

// The models on sale, for the view by model: every page, since the view compares them all.
export const listedModelsQuery = infiniteQueryOptions({
  queryKey: [accountKey, "models", { status: "listed" }, "all"],
  queryFn: ({ pageParam, signal }) =>
    unwrap(
      adminApi.GET(`${base}/models`, {
        params: { query: { status: "listed", cursor: pageParam, limit: 200 } },
        signal,
      }),
    ),
  initialPageParam: firstPage,
  getNextPageParam: nextCursor,
});
