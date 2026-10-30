import type { Page } from "@playwright/test";
import { catalogHandlers } from "./admin-catalog.ts";
import { type AdminState, emptyState, type Schemas, usualState, worstState } from "./admin-data.ts";
import { pricingHandlers } from "./admin-pricing.ts";
import { base, type Ctx, changes, reads, route } from "./admin-route.ts";
import { supplyHandlers } from "./admin-supply.ts";
import { fail, type Handler, type MockData, mockApi, ok, paged } from "./mock.ts";
import { confirmTotp, regenerate, session, signIn, signOut, totpSetup, userId, verifyMfa } from "./mock-auth.ts";

// The admin API (api/admin.yaml), answered from memory (mock.ts) with the data of admin-data.ts. The Session tag, the
// site's settings, and users and their credit are here; suppliers, the catalog and pricing are in the files beside.

const none = { level: "none" } as const;
const signedIn = { level: "signed_in" } as const;

function listUsers({ query, state }: Ctx<"/api/admin/v1/users", "get">) {
  const prefix = (query.get("email_prefix") ?? "").toLowerCase();
  const users = state.users.filter((user) => user.email.toLowerCase().startsWith(prefix));
  const page = paged(users, query);
  return page ? ({ status: 200, json: page } as const) : fail(400, "invalid_request");
}

function getAccount({ params, state }: Ctx<"/api/admin/v1/accounts/{account_id}", "get">) {
  const account = state.accounts.get(params.account_id ?? "");
  return account ? ok(account) : fail(404, "not_found");
}

function putSuspension({ body, params, state }: Ctx<"/api/admin/v1/accounts/{account_id}/suspension", "put">) {
  const account = state.accounts.get(params.account_id ?? "");
  if (!account) {
    return fail(404, "not_found");
  }
  account.calls_suspended = body.suspended;
  account.status = body.suspended ? "disabled" : "active";
  const user = state.users.find((candidate) => candidate.account_id === account.id);
  if (user) {
    user.status = account.status;
  }
  return { status: 204 as const, json: undefined };
}

function putSignIn({ body, params, state }: Ctx<"/api/admin/v1/users/{user_id}/sign-in", "put">) {
  const user = state.users.find((candidate) => candidate.id === params.user_id);
  if (!user) {
    return fail(404, "not_found");
  }
  user.status = body.disabled ? "disabled" : "active";
  for (const account of state.accounts.values()) {
    if (account.owner.id === user.id) {
      account.sign_in_disabled = body.disabled;
    }
  }
  return { status: 204 as const, json: undefined };
}

function putTier({ body, params, state }: Ctx<"/api/admin/v1/accounts/{account_id}/tier", "put">) {
  const account = state.accounts.get(params.account_id ?? "");
  if (!account) {
    return fail(404, "not_found");
  }
  const tier = state.tiers.find((candidate) => candidate.id === body.tier_id);
  if (!tier) {
    return fail(422, "unprocessable", { "/tier_id": "is not a tier of this site" });
  }
  account.tier = tier;
  const user = state.users.find((candidate) => candidate.account_id === account.id);
  if (user) {
    user.tier = tier;
  }
  return { status: 204 as const, json: undefined };
}

function listCalls({ query, state }: Ctx<"/api/admin/v1/calls", "get">) {
  const account = query.get("account_id");
  const request = query.get("request_id");
  const calls = state.calls.filter(
    (call) => (!account || call.account_id === account) && (!request || call.id === request),
  );
  const page = paged(calls, query, ["since", "until"]);
  if (!page) {
    return fail(400, "invalid_request");
  }
  const { next_cursor, ...rest } = page;
  return ok({
    ...rest,
    ...(next_cursor ? { next_cursor } : {}),
    since: query.get("since") ?? "2026-10-29T00:00:00.000Z",
    until: query.get("until") ?? "2026-10-30T23:59:59.000Z",
  });
}

function getTrace({ params, state }: Ctx<"/api/admin/v1/calls/{call_id}/trace", "get">) {
  const trace = state.traces.get(params.call_id ?? "");
  return trace ? ok(trace) : fail(404, "not_found");
}

function listAudit({ query, state }: Ctx<"/api/admin/v1/audit", "get">) {
  const exact = [
    "action",
    "module",
    "actor_kind",
    "actor_id",
    "target_kind",
    "target_id",
    "account_id",
    "site_id",
    "request_id",
  ] as const;
  const audit = state.audit.filter((entry) =>
    exact.every((name) => {
      const value = query.get(name);
      return !value || entry[name] === value;
    }),
  );
  const page = paged(audit, query, ["since", "until"]);
  if (!page) {
    return fail(400, "invalid_request");
  }
  const { next_cursor, ...rest } = page;
  return ok({
    ...rest,
    ...(next_cursor ? { next_cursor } : {}),
    since: query.get("since") ?? "2026-10-29T00:00:00.000Z",
    until: query.get("until") ?? "2026-10-30T23:59:59.000Z",
  });
}

function getAudit({ params, state }: Ctx<"/api/admin/v1/audit/{entry_id}", "get">) {
  const entry = state.auditEntries.get(params.entry_id ?? "");
  return entry ? ok(entry) : fail(404, "not_found");
}

// The most one grant may be, in nano-units: 1,000,000 units (the control plane's money limit, here a constant).
const grantLimit = 1_000_000_000_000_000n;

function grantCredit({ body, params, state }: Ctx<"/api/admin/v1/accounts/{account_id}/credit-grants", "post">) {
  const account = state.accounts.get(params.account_id ?? "");
  if (!account) {
    return fail(404, "not_found");
  }
  // An offline payment buys what it was paid in the site's currency; the mock keeps no exchange rates.
  const credit = body.source === "offline_payment" ? body.payment?.amount : body.amount;
  const { currency } = account.balance.balance;
  const amount = BigInt(credit?.amount ?? "0");
  const expires = body.expires_at !== undefined; // refused: nothing writes off an expired batch yet
  if (amount <= 0n || amount > grantLimit || credit?.currency !== currency || expires) {
    return fail(422, "unprocessable");
  }
  const balance = BigInt(account.balance.balance.amount) + amount;
  const available = BigInt(account.balance.available.amount) + amount;
  account.balance = {
    ...account.balance,
    balance: { amount: String(balance), currency },
    available: { amount: String(available), currency },
  };
  return {
    status: 201,
    json: {
      id: `00000000-0000-4000-8000-${String(state.stored.size).padStart(12, "0")}`,
      account_id: account.id,
      source: body.source ?? "gift",
      amount: { amount: String(amount), currency },
      payment: body.payment,
      created_at: new Date().toISOString(),
      balance_after: account.balance,
    },
  } as const;
}

function listPaymentOrders({ query, state }: Ctx<"/api/admin/v1/payments/orders", "get">) {
  if (query.get("site_id") !== state.siteId) {
    return fail(404, "not_found");
  }
  const account = query.get("account_id");
  const user = query.get("user_id");
  const merchant = query.get("merchant_id");
  const wanted = query.get("state");
  const since = query.get("since");
  const until = query.get("until");
  const orders = state.orders.filter(
    (order) =>
      (!account || order.account_id === account) &&
      (!user || order.user_id === user) &&
      (!merchant || order.merchant_id === merchant) &&
      (!wanted || order.state === wanted) &&
      (!since || (order.created_at ?? "") >= since) &&
      (!until || (order.created_at ?? "") < until),
  );
  const page = paged(orders, query);
  return page ? ok(page) : fail(400, "invalid_request");
}

function listPaymentRefunds({ query, state }: Ctx<"/api/admin/v1/payments/refunds", "get">) {
  if (query.get("site_id") !== state.siteId) {
    return fail(404, "not_found");
  }
  const wanted = query.get("state");
  const page = paged(
    state.refunds.filter((refund) => !wanted || refund.state === wanted),
    query,
  );
  return page ? ok(page) : fail(400, "invalid_request");
}

// A decision changes the refund the page already has: a second person's approval, or a withdrawal that returns the
// credits. The mock keeps the idempotency the real server has by refusing a refund that is no longer frozen.
function decideRefund(
  kind: "approve" | "reject",
  { body, params, state }: { body: Schemas["ReasonRequest"]; params: { refund_id?: string }; state: AdminState },
) {
  const refund = state.refunds.find((candidate) => candidate.id === params.refund_id);
  if (!refund) {
    return fail(404, "not_found");
  }
  if (!body.reason?.trim()) {
    return fail(422, "unprocessable", { "/reason": "a reason is required" });
  }
  if (refund.state !== "frozen" || refund.approved_by) {
    return fail(409, "conflict");
  }
  if (kind === "approve" && refund.requested_by === userId(state.account)) {
    return fail(403, "forbidden");
  }
  refund.approved_by = userId(state.account);
  refund.state = kind === "approve" ? "succeeded" : "failed";
  refund.finished_at = new Date().toISOString();
  if (kind === "approve") {
    refund.provider_ref = `refund-${refund.id?.slice(-8) ?? "0"}`;
  }
  return ok(refund);
}

function getSettings({ params, state }: Ctx<"/api/admin/v1/sites/{site_id}/settings", "get">) {
  return params.site_id === state.siteId ? ok(state.settings) : fail(404, "not_found");
}

function putSettings({ body, params, state }: Ctx<"/api/admin/v1/sites/{site_id}/settings", "put">) {
  if (params.site_id !== state.siteId) {
    return fail(404, "not_found");
  }
  state.settings = { ...state.settings, settings: body.settings };
  return ok(state.settings);
}

function getSiteLogo({ params, state }: Ctx<"/api/admin/v1/sites/{site_id}/logo", "get">) {
  return params.site_id === state.siteId ? ok(state.logo) : fail(404, "not_found");
}

function putSiteLogo({ body, params, state }: Ctx<"/api/admin/v1/sites/{site_id}/logo", "put">) {
  if (params.site_id !== state.siteId) {
    return fail(404, "not_found");
  }
  if (!body.logo) {
    state.logo = {};
    return ok(state.logo);
  }
  const size = Buffer.from(body.logo.data_base64, "base64").length;
  if (size === 0 || size > 256 * 1024) {
    return fail(400, "invalid_request");
  }
  const logo: Schemas["SiteLogo"] = {
    content_type: body.logo.content_type,
    hash: "0".repeat(64),
    size,
    width: 20,
    height: 20,
    data_url: `data:${body.logo.content_type};base64,${body.logo.data_base64}`,
  };
  state.logo = { logo };
  const { data_url: _, ...shown } = logo;
  return ok({ logo: shown });
}

function getSiteKeyLimits({ params, state }: Ctx<"/api/admin/v1/sites/{site_id}/key-limits", "get">) {
  return params.site_id === state.siteId ? ok(state.keyLimits) : fail(404, "not_found");
}

// What a level without limits of its own falls back to: the site's, here the defaults of the usual platform.
const inherited = { max_keys: 100, creations_per_hour: 50 };

function bounded(limits: { max_keys?: number; creations_per_hour?: number }) {
  return Object.values(limits).every((n) => Number.isInteger(n) && n >= 1 && n <= 10_000);
}

function putSiteKeyLimits({ body, params, state }: Ctx<"/api/admin/v1/sites/{site_id}/key-limits", "put">) {
  if (params.site_id !== state.siteId) {
    return fail(404, "not_found");
  }
  if (!bounded(body.limits)) {
    return fail(400, "invalid_request");
  }
  state.keyLimits = { limits: body.limits, effective: { ...inherited, ...body.limits } };
  return ok(state.keyLimits);
}

function putAccountKeyLimits({ body, params, state }: Ctx<"/api/admin/v1/accounts/{account_id}/key-limits", "put">) {
  const account = state.accounts.get(params.account_id ?? "");
  if (!account) {
    return fail(404, "not_found");
  }
  if (!bounded(body.limits)) {
    return fail(400, "invalid_request");
  }
  account.key_limits = { limits: body.limits, effective: { ...state.keyLimits.effective, ...body.limits } };
  return ok(account.key_limits);
}

const handlers: Handler<AdminState>[] = [
  route("get", `${base}/version`, none, ({ state }) => ok({ version: state.version })),
  route("get", `${base}/site`, none, ({ state }) =>
    ok({ name: "Axiom", ...(state.logo.logo?.data_url ? { logo_url: state.logo.logo.data_url } : {}) }),
  ),
  route("post", `${base}/auth/sign-in`, none, signIn),
  route("post", `${base}/auth/mfa`, { level: "pending_mfa" }, verifyMfa),
  route("post", `${base}/auth/sign-out`, { level: "any" }, signOut),
  route("get", `${base}/session`, { level: "any" }, ({ state }) => ok(session(state))),
  route("post", `${base}/auth/totp/setup`, signedIn, totpSetup),
  route("post", `${base}/auth/totp/confirm`, signedIn, confirmTotp),
  route("post", `${base}/auth/recovery-codes`, { level: "mfa" }, regenerate),
  route("get", `${base}/sites/{site_id}/settings`, reads("sites.manage"), getSettings),
  route("put", `${base}/sites/{site_id}/settings`, changes("sites.manage"), putSettings),
  route("get", `${base}/sites/{site_id}/logo`, reads("sites.manage"), getSiteLogo),
  route("put", `${base}/sites/{site_id}/logo`, changes("sites.manage"), putSiteLogo),
  route("get", `${base}/sites/{site_id}/key-limits`, reads("sites.manage"), getSiteKeyLimits),
  route("put", `${base}/sites/{site_id}/key-limits`, changes("sites.manage"), putSiteKeyLimits),
  route("get", `${base}/tiers`, reads("users.view"), ({ state }) => ok({ data: state.tiers })),
  route("get", `${base}/payments/merchants`, reads("sites.manage"), ({ query, state }) =>
    query.get("site_id") === state.siteId ? ok({ data: state.merchants }) : fail(404, "not_found"),
  ),
  route("get", `${base}/payments/orders`, reads("sites.manage"), listPaymentOrders),
  route("get", `${base}/payments/refunds`, reads("sites.manage"), listPaymentRefunds),
  route("post", `${base}/payments/refunds/{refund_id}/approve`, changes("sites.manage"), (ctx) =>
    decideRefund("approve", ctx),
  ),
  route("post", `${base}/payments/refunds/{refund_id}/reject`, changes("sites.manage"), (ctx) =>
    decideRefund("reject", ctx),
  ),
  route("get", `${base}/users`, reads("users.view"), listUsers),
  route("get", `${base}/accounts/{account_id}`, reads("users.view"), getAccount),
  route("put", `${base}/accounts/{account_id}/key-limits`, changes("sites.manage"), putAccountKeyLimits),
  route("put", `${base}/accounts/{account_id}/suspension`, changes("users.manage"), putSuspension),
  route("put", `${base}/accounts/{account_id}/tier`, changes("users.manage"), putTier),
  route("put", `${base}/users/{user_id}/sign-in`, changes("users.manage"), putSignIn),
  route("get", `${base}/calls`, reads("calls.search"), listCalls),
  route("get", `${base}/calls/{call_id}/trace`, reads("calls.inspect"), getTrace),
  route("get", `${base}/audit`, reads("audit.view"), listAudit),
  route("get", `${base}/audit/{entry_id}`, reads("audit.view"), getAudit),
  route("post", `${base}/accounts/{account_id}/credit-grants`, changes("credits.grant"), grantCredit),
  ...supplyHandlers,
  ...catalogHandlers,
  ...pricingHandlers,
];

// mockAdmin answers the admin's API for a page. The returned state is live: a test reads what the page did to it
// (state.log, state.models) and sets up the session it needs before navigating (state.level = "mfa" is signed in).
export async function mockAdmin(page: Page, data: MockData = "usual", over: Partial<AdminState> = {}) {
  const state = { usual: usualState, empty: emptyState, worst: worstState }[data]();
  Object.assign(state, over);
  return mockApi(page, base, handlers, state);
}
