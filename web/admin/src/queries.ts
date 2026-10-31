import { type AdminSchemas, adminApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { parseJSONLossless } from "./lossless";

// The calls the pages make, one place for each so a page and the change that alters its data agree on the key.
// Account data is under accountKey, which the end of the session clears, and which a change invalidates as a whole: the
// admin's data is small, and a change that leaves a list stale is worse than one that asks again.

const pageSize = 50;
const base = "/api/admin/v1";

// A page of a list is asked for with the cursor the previous page gave, unchanged; the last page has none.
function nextCursor(page: { next_cursor: string | null }) {
  return page.next_cursor ?? undefined;
}

const firstPage = undefined as string | undefined;

// The site's public brand, for the sign-in pages and the shell. It is not account data: it must survive signing in,
// which clears accountKey, and it is needed before there is a session. A logo change invalidates it explicitly.
export const adminSiteQuery = queryOptions({
  queryKey: ["admin-site"],
  queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/site`, { signal })),
  staleTime: 30_000,
});

// The site's tiers, not the main site's: an account belongs to one site, and a tier of another site cannot be assigned.
export const tiersQuery = (site?: AdminSchemas["SiteId"]) =>
  queryOptions({
    queryKey: [accountKey, "tiers", site],
    queryFn: ({ signal }) =>
      unwrap(adminApi.GET(`${base}/tiers`, { params: site ? { query: { site_id: site } } : {}, signal })),
  });

export type UserFilters = { email_prefix?: string };

export const usersQuery = (filters: UserFilters) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "users", filters],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/users`, {
          params: { query: { ...filters, cursor: pageParam, limit: pageSize } },
          signal,
        }),
      ),
    initialPageParam: firstPage,
    getNextPageParam: nextCursor,
  });

export const accountQuery = (id: AdminSchemas["AccountId"]) =>
  queryOptions({
    queryKey: [accountKey, "account", id],
    queryFn: ({ signal }) =>
      unwrap(adminApi.GET(`${base}/accounts/{account_id}`, { params: { path: { account_id: id } }, signal })),
  });

// A window is the server's: the page a cursor leads to keeps the since/until of the answer it came from, so the end
// of a 31-day search does not silently become the default 24 hours.
type PageWindow = { cursor?: string; since?: string; until?: string };

export function nextWindow(page: { next_cursor?: string | null; since?: string; until?: string }) {
  return page.next_cursor ? { cursor: page.next_cursor, since: page.since, until: page.until } : undefined;
}

export type AdminCallFilters = {
  account_id?: string;
  request_id?: string;
  site_id?: string;
  model?: string;
  status?: "ok" | "error";
  since?: string;
  until?: string;
};

// The server's page carries the window the first page actually used; the next page sends it back so a default window
// does not move while someone pages through it.
const callPageQuery = (filters: AdminCallFilters, page: PageWindow) => ({
  ...filters,
  since: page.since ?? filters.since,
  until: page.until ?? filters.until,
  cursor: page.cursor,
  limit: pageSize,
});

export const adminCallsQuery = (filters: AdminCallFilters) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "admin-calls", filters],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/calls`, {
          params: { query: callPageQuery(filters, pageParam) },
          signal,
        }),
      ),
    initialPageParam: {} as PageWindow,
    getNextPageParam: nextWindow,
  });

// The operations dashboard: one request for the site's calls, revenue, cost, cash and busiest accounts and models. The
// window is absolute, so the figures and the links they open keep describing the same calls.
export const overviewQuery = (since: string, until: string) =>
  queryOptions({
    queryKey: [accountKey, "overview", since, until],
    queryFn: ({ signal }) =>
      unwrap(adminApi.GET(`${base}/reports/overview`, { params: { query: { since, until } }, signal })),
  });

// The route and the attempts are shown as JSON and may hold integers beyond JavaScript's exact range, so the trace is
// read as text and parsed without rounding those numbers.
export const adminCallQuery = (id: string) =>
  queryOptions({
    queryKey: [accountKey, "admin-call", id],
    queryFn: async ({ signal }) =>
      parseJSONLossless(
        await unwrap(
          adminApi.GET(`${base}/calls/{call_id}/trace`, {
            params: { path: { call_id: id } },
            parseAs: "text",
            signal,
          }),
        ),
      ) as AdminSchemas["AdminCallTrace"],
  });

export type AdminAuditFilters = AdminCallFilters & {
  actor_kind?: string;
  actor_id?: string;
  action?: string;
  module?: string;
  target_kind?: string;
  target_id?: string;
  site_id?: string;
};

export const adminAuditQuery = (filters: AdminAuditFilters) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "admin-audit", filters],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/audit`, {
          params: { query: callPageQuery(filters, pageParam) },
          signal,
        }),
      ),
    initialPageParam: {} as PageWindow,
    getNextPageParam: nextWindow,
  });

// The same for an audit entry's detail: its ratios and other integers are data, not display rounding.
export const auditEntryQuery = (id: string) =>
  queryOptions({
    queryKey: [accountKey, "audit-entry", id],
    queryFn: async ({ signal }) =>
      parseJSONLossless(
        await unwrap(
          adminApi.GET(`${base}/audit/{entry_id}`, {
            params: { path: { entry_id: id } },
            parseAs: "text",
            signal,
          }),
        ),
      ) as AdminSchemas["AuditEntry"],
  });

export const siteKeyLimitsQuery = (id: AdminSchemas["SiteId"]) =>
  queryOptions({
    queryKey: [accountKey, "site-key-limits", id],
    queryFn: ({ signal }) =>
      unwrap(adminApi.GET(`${base}/sites/{site_id}/key-limits`, { params: { path: { site_id: id } }, signal })),
  });

export const suppliersQuery = (status?: AdminSchemas["SupplierStatus"]) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "suppliers", status ?? ""],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/suppliers`, {
          params: { query: { status, cursor: pageParam, limit: pageSize } },
          signal,
        }),
      ),
    initialPageParam: firstPage,
    getNextPageParam: nextCursor,
  });

// A supplier as a node of any release still running answers it. During a rolling release a node from before endpoints
// were listed answers without them, which the page shows as not available rather than as none.
export type SupplierView = Omit<AdminSchemas["SupplierDetail"], "endpoints"> & {
  endpoints?: AdminSchemas["SupplierEndpoint"][];
};

// A supplier that is being connected changes by itself, as the sync works through it: the page asks again until it
// settles.
export const supplierQuery = (id: string) =>
  queryOptions({
    queryKey: [accountKey, "supplier", id],
    queryFn: ({ signal }): Promise<SupplierView> =>
      unwrap(adminApi.GET(`${base}/suppliers/{supplier_id}`, { params: { path: { supplier_id: id } }, signal })),
    refetchInterval: ({ state }) => {
      const status = state.data?.supplier.status;
      return status === "connecting" ? 3000 : false;
    },
  });

export const pendingMappingsQuery = (supplierId?: string) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "pending-mappings", supplierId ?? ""],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/pending-mappings`, {
          params: { query: { supplier_id: supplierId, cursor: pageParam, limit: pageSize } },
          signal,
        }),
      ),
    initialPageParam: firstPage,
    getNextPageParam: nextCursor,
  });

export const authorsQuery = queryOptions({
  queryKey: [accountKey, "authors"],
  queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/authors`, { signal })),
});

export type ModelFilters = {
  status?: AdminSchemas["ModelStatus"];
  author?: string;
  slug_prefix?: string;
};

export const modelsQuery = (filters: ModelFilters) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "models", filters],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/models`, {
          params: { query: { ...filters, cursor: pageParam, limit: pageSize } },
          signal,
        }),
      ),
    initialPageParam: firstPage,
    getNextPageParam: nextCursor,
  });

export const modelQuery = (id: string) =>
  queryOptions({
    queryKey: [accountKey, "model", id],
    queryFn: ({ signal }) =>
      unwrap(adminApi.GET(`${base}/models/{model_id}`, { params: { path: { model_id: id } }, signal })),
  });

// The models whose slug starts with what the person has typed, for choosing one by name.
export const modelLookupQuery = (prefix: string) =>
  queryOptions({
    queryKey: [accountKey, "model-lookup", prefix],
    queryFn: ({ signal }) =>
      unwrap(
        adminApi.GET(`${base}/models`, { params: { query: { slug_prefix: prefix || undefined, limit: 20 } }, signal }),
      ),
  });

// The most models one preview takes, and so the most a picker offers.
export const previewLimit = 100;

export const listedModelsQuery = queryOptions({
  queryKey: [accountKey, "listed-models"],
  queryFn: ({ signal }) =>
    unwrap(adminApi.GET(`${base}/models`, { params: { query: { status: "listed", limit: 200 } }, signal })),
});

export const rulesQuery = queryOptions({
  queryKey: [accountKey, "pricing-rules"],
  queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/pricing/rules`, { signal })),
});

export const siteRegistrationQuery = (siteId: AdminSchemas["SiteId"]) =>
  queryOptions({
    queryKey: [accountKey, "site-registration", siteId],
    queryFn: ({ signal }) =>
      unwrap(adminApi.GET(`${base}/sites/{site_id}/registration`, { params: { path: { site_id: siteId } }, signal })),
  });

export const siteSignInQuery = (siteId: AdminSchemas["SiteId"]) =>
  queryOptions({
    queryKey: [accountKey, "site-sign-in", siteId],
    queryFn: ({ signal }) =>
      unwrap(adminApi.GET(`${base}/sites/{site_id}/sign-in`, { params: { path: { site_id: siteId } }, signal })),
  });

export const siteLogoQuery = (siteId: AdminSchemas["SiteId"]) =>
  queryOptions({
    queryKey: [accountKey, "site-logo", siteId],
    queryFn: ({ signal }) =>
      unwrap(adminApi.GET(`${base}/sites/{site_id}/logo`, { params: { path: { site_id: siteId } }, signal })),
  });

export const siteSettingsQuery = (siteId: AdminSchemas["SiteId"]) =>
  queryOptions({
    queryKey: [accountKey, "site-settings", siteId],
    queryFn: ({ signal }) =>
      unwrap(adminApi.GET(`${base}/sites/{site_id}/settings`, { params: { path: { site_id: siteId } }, signal })),
  });

export const systemSettingsQuery = (siteId: AdminSchemas["SiteId"]) =>
  queryOptions({
    queryKey: [accountKey, "system-settings", siteId],
    queryFn: ({ signal }) =>
      unwrap(
        adminApi.GET(`${base}/sites/{site_id}/system-settings`, { params: { path: { site_id: siteId } }, signal }),
      ),
  });

export const mailSettingsQuery = queryOptions({
  queryKey: [accountKey, "mail-settings"],
  queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/settings/mail`, { signal })),
});

export const paymentSettingsQuery = (siteId: AdminSchemas["SiteId"]) =>
  queryOptions({
    queryKey: [accountKey, "payment-settings", siteId],
    queryFn: ({ signal }) =>
      unwrap(
        adminApi.GET(`${base}/sites/{site_id}/system-settings/payments`, {
          params: { path: { site_id: siteId } },
          signal,
        }),
      ),
  });

export const merchantsQuery = (siteId: AdminSchemas["SiteId"]) =>
  queryOptions({
    queryKey: [accountKey, "merchants", siteId],
    queryFn: ({ signal }) =>
      unwrap(adminApi.GET(`${base}/payments/merchants`, { params: { query: { site_id: siteId } }, signal })),
  });

export type PaymentOrderFilters = {
  account_id?: string;
  user_id?: string;
  state?: AdminSchemas["PaymentOrderState"];
  merchant_id?: string;
  since?: string;
  until?: string;
};

// A site's orders, newest first, each with its refunds. The filters are the address's, so a page and the query that
// changes its data agree on the key; the queue below asks for the state it decides.
export const paymentOrdersQuery = (siteId: AdminSchemas["SiteId"], filters: PaymentOrderFilters) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "payment-orders", siteId, filters],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/payments/orders`, {
          params: { query: { site_id: siteId, ...filters, cursor: pageParam, limit: pageSize } },
          signal,
        }),
      ),
    initialPageParam: firstPage,
    getNextPageParam: nextCursor,
  });

// The cash behind the dashboard figure: ledger time, not an order's, so a refund of an older order and an offline
// top-up are both here.
export const cashMovementsQuery = (siteId: AdminSchemas["SiteId"], window: { since?: string; until?: string }) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "cash-movements", siteId, window],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/payments/cash`, {
          params: {
            query: { site_id: siteId, since: window.since, until: window.until, cursor: pageParam, limit: pageSize },
          },
          signal,
        }),
      ),
    initialPageParam: firstPage,
    getNextPageParam: nextCursor,
  });

export const paymentRefundsQuery = (siteId: AdminSchemas["SiteId"], state?: AdminSchemas["PaymentRefundState"]) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "payment-refunds", siteId, state ?? ""],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/payments/refunds`, {
          params: { query: { site_id: siteId, state, cursor: pageParam, limit: pageSize } },
          signal,
        }),
      ),
    initialPageParam: firstPage,
    getNextPageParam: nextCursor,
  });
