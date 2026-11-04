import type { Ctx } from "./admin-route.ts";
import { fail, ok, paged } from "./mock.ts";

// The paid credit batches behind the dashboard's paid users and monthly revenue. The window is the batch's own
// created_at, so an order created earlier but credited inside the window is represented here.
export function listPaidBatches({ query, state }: Ctx<"/api/admin/v1/payments/paid-batches", "get">) {
  if (query.get("site_id") !== state.siteId) {
    return fail(404, "not_found");
  }
  const since = query.get("since");
  const until = query.get("until");
  const rows = state.paidBatches.filter(
    (batch) => (!since || batch.created_at >= since) && (!until || batch.created_at < until),
  );
  const page = paged(rows, query);
  return page ? ok(page) : fail(400, "invalid_request");
}
