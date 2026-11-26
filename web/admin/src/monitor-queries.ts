import { type AdminSchemas, ApiError, adminApi, isTransient, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { queryOptions } from "@tanstack/react-query";

const base = "/api/admin/v1";

// The reads of the operations pages whose backends come after the page: an operation a node does not have yet answers
// 501 (not built) or 404 (an older node), and a page section that depends on one is left out quietly, not shown as a
// failure. A section that is out has no data and shows nothing, so nothing is asked of it twice.
export function notBuilt(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 501 || error.status === 404);
}

const retry = (failures: number, error: unknown) => failures < 2 && !notBuilt(error) && isTransient(error);

export type MonitorWindow = AdminSchemas["MonitorWindow"];

// The platform's calls over the last hour (a point a minute) or day (a point an hour), as getOperationsMonitor answers.
export const monitorQuery = (window: MonitorWindow) =>
  queryOptions({
    queryKey: [accountKey, "operations-monitor", window],
    queryFn: ({ signal }) => unwrap(adminApi.GET(`${base}/reports/monitor`, { params: { query: { window } }, signal })),
    retry,
  });

// Every supplier's credentials with what each holds against its limits now: the monitor's lines show how full they are, and
// a score takes points off for a credential at its ceiling. The first page is the newest 100.
export const credentialsQuery = queryOptions({
  queryKey: [accountKey, "supplier-credentials"],
  queryFn: ({ signal }) =>
    unwrap(
      adminApi.GET(`${base}/supplier-credentials`, { params: { query: { status: "active", limit: 100 } }, signal }),
    ),
  retry,
});
