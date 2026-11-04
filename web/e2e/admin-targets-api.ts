import type { Schemas } from "./admin-data.ts";
import { targetMetrics } from "./admin-fixtures.ts";
import type { Ctx } from "./admin-route.ts";
import { fail, ok } from "./mock.ts";

// The targets of a month, and the editor's write of them. The mock keeps one set per month and does not prorate it;
// showing the page's target and empty states is enough. `oldTargetRead` and `oldTargetWrite` stand in for a node before
// the commercial metrics, so a test can put the GET and the PUT on different versions.
export function getTargets({ params, state }: Ctx<"/api/admin/v1/reports/targets/{month}", "get">) {
  const month = params.month ?? "";
  const stored = state.targets.get(month);
  if (state.oldTargetRead) {
    return ok({
      site_id: state.siteId,
      month,
      rev: stored?.rev ?? 0,
      calls: stored?.calls,
      errors: stored?.errors,
      revenue: stored?.revenue,
      cost: stored?.cost,
    });
  }
  return ok(stored ?? { site_id: state.siteId, month, rev: 0, metrics: targetMetrics });
}

function writeTargets(
  body: Schemas["OperationsTargetsUpdate"],
  month: string,
  state: Parameters<typeof getTargets>[0]["state"],
  full: boolean,
) {
  const current = state.targets.get(month);
  if (body.rev !== (current?.rev ?? 0)) {
    return fail(409, "stale_revision");
  }
  const kept = full ? body : current;
  const stored: Schemas["OperationsTargets"] = {
    site_id: state.siteId,
    month,
    rev: (current?.rev ?? 0) + 1,
    metrics: targetMetrics,
    calls: body.calls,
    errors: body.errors,
    revenue: body.revenue,
    cost: body.cost,
    paid_users: kept?.paid_users,
    monthly_revenue: kept?.monthly_revenue,
    gross_margin: kept?.gross_margin,
    retention: kept?.retention,
  };
  state.targets.set(month, stored);
  return ok(stored);
}

// The legacy endpoint is the four-field write every node has. It refuses the commercial fields and both full-set
// markers, as the new node does; a page from before the commercial metrics sends none of them.
export function putTargets({ body, params, state }: Ctx<"/api/admin/v1/reports/targets/{month}", "put">) {
  const commercial = (["paid_users", "monthly_revenue", "gross_margin", "retention"] as const).some(
    (field) => body[field] !== undefined,
  );
  if (body.all_metrics !== undefined || commercial) {
    return fail(400, "invalid_request");
  }
  return writeTargets(body, params.month ?? "", state, false);
}

// The full-replacement route is new with the commercial metrics; a node before them has no such path and answers 404
// before it can mistake the request for a legacy four-field write.
export function replaceTargets({ body, params, state }: Ctx<"/api/admin/v1/reports/targets/{month}/all", "put">) {
  if (state.oldTargetWrite) {
    return fail(404, "not_found");
  }
  if (body.all_metrics !== true) {
    return fail(400, "invalid_request");
  }
  return writeTargets(body, params.month ?? "", state, true);
}

// targetComparison turns a stored month into the overview's prorated comparison. The mock states the month's targets
// as they are; it exists to show the page's target and gap, not to re-test the server's arithmetic.
export function targetComparison(
  stored: Schemas["OperationsTargets"] | undefined,
  calls: number,
  errors: number,
  revenue: bigint,
  cost: bigint,
  paidUsers: number,
  monthlyRevenue: bigint,
  grossMargin: number | undefined,
  retention: Schemas["OperationsRetention"],
) {
  if (!stored) {
    return {};
  }
  return {
    ...(stored.calls !== undefined ? { calls: { target: stored.calls, gap: calls - stored.calls } } : {}),
    ...(stored.errors !== undefined ? { errors: { target: stored.errors, gap: errors - stored.errors } } : {}),
    ...(stored.revenue
      ? {
          revenue: {
            target: stored.revenue,
            gap: { amount: String(revenue - BigInt(stored.revenue.amount)), currency: stored.revenue.currency },
          },
        }
      : {}),
    ...(stored.cost
      ? {
          cost: {
            target: stored.cost,
            gap: { amount: String(cost - BigInt(stored.cost.amount)), currency: stored.cost.currency },
          },
        }
      : {}),
    ...(stored.paid_users !== undefined
      ? { paid_users: { target: stored.paid_users, gap: paidUsers - stored.paid_users } }
      : {}),
    ...(stored.monthly_revenue
      ? {
          monthly_revenue: {
            target: stored.monthly_revenue,
            gap: {
              amount: String(monthlyRevenue - BigInt(stored.monthly_revenue.amount)),
              currency: stored.monthly_revenue.currency,
            },
          },
        }
      : {}),
    ...(stored.gross_margin !== undefined
      ? {
          gross_margin: {
            target: stored.gross_margin,
            ...(grossMargin !== undefined ? { gap: grossMargin - stored.gross_margin } : {}),
          },
        }
      : {}),
    ...(stored.retention !== undefined
      ? {
          retention: {
            target: stored.retention,
            ...(retention.rate !== undefined ? { gap: retention.rate - stored.retention } : {}),
          },
        }
      : {}),
  };
}
