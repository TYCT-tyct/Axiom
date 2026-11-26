import { expect, it } from "vitest";
import { holds } from "./role-matrix";

it("gives the admin every capability, and each other role only what the baseline migration gave it", () => {
  expect(holds("admin", ["users.manage", "credits.grant", "sites.manage", "platform_grants.manage"])).toBe(true);
  expect(holds("support", ["users.view", "calls.search"])).toBe(true);
  expect(holds("support", ["credits.grant"])).toBe(false);
  expect(holds("finance", ["calls.search"])).toBe(false);
  expect(holds("viewer", ["suppliers.view", "catalog.view", "pricing.view"])).toBe(true);
  expect(holds("viewer", ["pricing.manage"])).toBe(false);
});
