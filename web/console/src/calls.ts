import { consoleApi, unwrap } from "@axiom/api";
import type { CallFilters } from "./queries";

// callsCsv is the account's calls as the server gives them: comma-separated text with the same filters as the list,
// from one snapshot, at most 31 days and ten thousand rows.
export const callsCsv = (filters: CallFilters) =>
  unwrap(consoleApi.GET("/api/console/v1/calls/export", { params: { query: filters }, parseAs: "text" }));

// The filename uses today's UTC date, like the statement's export.
export function callsName(now = new Date()) {
  return `calls-${now.toISOString().slice(0, 10)}.csv`;
}
