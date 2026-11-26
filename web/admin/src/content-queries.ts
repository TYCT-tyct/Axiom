import { type AdminSchemas, adminApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";

const pageSize = 50;
const base = "/api/admin/v1";
const firstPage = undefined as string | undefined;

// How the two plugins were used in a window, newest hour last. The window is absolute, so the cards and the list of
// requests under them describe the same hours.
export const pluginStatsQuery = (since: string, until: string) =>
  queryOptions({
    queryKey: [accountKey, "plugin-stats", since, until],
    queryFn: ({ signal }) =>
      unwrap(adminApi.GET(`${base}/reports/plugins`, { params: { query: { since, until } }, signal })),
  });

// Every announcement of one site, newest start first, withdrawn and ended ones too.
export const announcementsQuery = (siteId: AdminSchemas["SiteId"]) =>
  infiniteQueryOptions({
    queryKey: [accountKey, "admin-announcements", siteId],
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        adminApi.GET(`${base}/announcements`, {
          params: { query: { site_id: siteId, cursor: pageParam, limit: pageSize } },
          signal,
        }),
      ),
    initialPageParam: firstPage,
    getNextPageParam: (page) => page.next_cursor,
  });
