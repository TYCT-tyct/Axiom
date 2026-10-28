import { readFileSync } from "node:fs";
import { expect, test } from "@playwright/test";
import { base, identities } from "./admin-route.ts";
import { fingerprint } from "./mock.ts";

// The mock's idea of the same request under one key, held to the server's by the cases the Go tests check too
// (internal/platform/idem/same_request_test.go, cmd/axiom/admin_same_request_test.go).

type Case = { case: string; operation?: string; first: string; again: string; same: boolean };

const cases: Case[] = JSON.parse(
  readFileSync(new URL("../../internal/platform/idem/testdata/same-request.json", import.meta.url), "utf8"),
);

// The route of each operation the cases name.
const routes: Record<string, string> = {
  createSupplier: `post ${base}/suppliers`,
  replaceSupplierLogin: `put ${base}/suppliers/{supplier_id}/login`,
};

for (const c of cases) {
  test(`same request: ${c.case}`, () => {
    const identity = c.operation ? identities[routes[c.operation] ?? ""] : undefined;
    expect(c.operation === undefined || identity !== undefined).toBe(true);
    expect(fingerprint(c.first, identity) === fingerprint(c.again, identity)).toBe(c.same);
  });
}
