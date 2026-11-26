import type { Ctx } from "./admin-route.ts";
import { fail, ok, paged } from "./mock.ts";

// The site's redeem codes, narrowed by state and by the start of their note as the server does, ignoring case.
export function listRedeemCodes({ query, state }: Ctx<"/api/admin/v1/payments/redeem-codes", "get">) {
  if (query.get("site_id") !== state.siteId) {
    return fail(404, "not_found");
  }
  const wanted = query.get("state");
  const note = (query.get("note") ?? "").toLowerCase();
  const codes = state.redeemCodes.filter(
    (code) => (!wanted || code.state === wanted) && code.note.toLowerCase().startsWith(note),
  );
  const page = paged(codes, query);
  // The list's cursor is absent on the last page, not null.
  return page ? ok({ data: page.data, next_cursor: page.next_cursor ?? undefined }) : fail(400, "invalid_request");
}
