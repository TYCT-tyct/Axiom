import { consoleApi, unwrap } from "@axiom/api";
import { queryOptions } from "@tanstack/react-query";

// The public terms and privacy policy. It sits outside accountKey: the registration pages read it before an account
// exists, and a new publication must be visible on the next visit.
export const legalQuery = queryOptions({
  queryKey: ["legal"],
  queryFn: ({ signal }) =>
    unwrap(consoleApi.GET("/api/console/v1/legal", { params: { query: { locales: "all" } }, signal })),
});

export const siteQuery = queryOptions({
  queryKey: ["site"],
  queryFn: ({ signal }) => unwrap(consoleApi.GET("/api/console/v1/site", { signal })),
  staleTime: 5 * 60_000,
});
