// The permissions of the mocked admin session: every admin surface in one list, so a page test does not depend on a
// role's smaller subset unless it sets one itself.
export const allPermissions = [
  "users.view",
  "users.manage",
  "platform_grants.manage",
  "credits.grant",
  "calls.search",
  "calls.inspect",
  "audit.view",
  "suppliers.view",
  "suppliers.manage",
  "catalog.view",
  "catalog.manage",
  "pricing.view",
  "pricing.manage",
  "sites.manage",
  "operations.approve",
];
