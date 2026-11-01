import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import type { CallFilters } from "./queries";

// callsCsv is the account's calls as the server gives them: comma-separated text with the same filters as the list,
// from one snapshot, at most 31 days and ten thousand rows.
export const callsCsv = (filters: CallFilters, signal: AbortSignal) =>
  unwrap(consoleApi.GET("/api/console/v1/calls/export", { params: { query: filters }, parseAs: "text", signal }));

// The filename uses today's UTC date, like the statement's export.
export function callsName(now = new Date()) {
  return `calls-${now.toISOString().slice(0, 10)}.csv`;
}

// filtersUnsupported detects a node that ignores the filters added with the report endpoints. status has an echo on
// every page; key, workspace and request ID have none, so the rows themselves must prove the filter was applied.
// An empty page is not evidence of a wrong filter (and shows no wrong row), so it is left to the server.
export function filtersUnsupported(filters: CallFilters, pages: Schemas["CallList"][]) {
  const { status, key_id, workspace_id, request_id } = filters;
  if (!status && !key_id && !workspace_id && !request_id) {
    return false;
  }
  return pages.some((page) => {
    if (status && page.status_filter !== status) {
      return true;
    }
    return page.data.some(
      (call) =>
        (status !== undefined && (call.error_kind !== "") !== (status === "error")) ||
        (key_id !== undefined && call.key_id !== key_id) ||
        (workspace_id !== undefined && call.workspace_id !== workspace_id) ||
        (request_id !== undefined && call.id !== request_id),
    );
  });
}
