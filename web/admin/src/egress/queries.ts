import { adminApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { queryOptions } from "@tanstack/react-query";
import { retry } from "../monitor-queries";

// The egress ids of every credential that is not erased, whatever its status. Whether a proxy is in use is the question
// the removal asks (suppliers.md「出口代理」: erased credentials do not keep a proxy), and the proxy's own row does not
// answer it, so the credentials are read to the last page. The key sits under the credentials' own, so a change
// anywhere refreshes it.
export const credentialExitsQuery = queryOptions({
  queryKey: [accountKey, "supplier-credentials", "egress-ids"],
  queryFn: async ({ signal }) => {
    const exits: string[] = [];
    let cursor: string | undefined;
    do {
      const page = await unwrap(
        adminApi.GET("/api/admin/v1/supplier-credentials", { params: { query: { cursor, limit: 200 } }, signal }),
      );
      for (const credential of page.data) {
        if (credential.egress_id && credential.status !== "erased") exits.push(credential.egress_id);
      }
      cursor = page.next_cursor ?? undefined;
    } while (cursor);
    return exits;
  },
  retry,
});
