import type { Page } from "@playwright/test";
import { catalogHandlers } from "./admin-catalog.ts";
import { type AdminState, emptyState, usualState, worstState } from "./admin-data.ts";
import { pricingHandlers } from "./admin-pricing.ts";
import { base, type Ctx, changes, reads, route } from "./admin-route.ts";
import { supplyHandlers } from "./admin-supply.ts";
import { fail, type Handler, type MockData, mockApi, ok, paged } from "./mock.ts";
import { confirmTotp, regenerate, session, signIn, signOut, totpSetup, verifyMfa } from "./mock-auth.ts";

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
  route("post", `${base}/auth/sign-in`, none, signIn),
  route("post", `${base}/auth/mfa`, { level: "pending_mfa" }, verifyMfa),
  route("post", `${base}/auth/sign-out`, { level: "any" }, signOut),
  route("get", `${base}/session`, { level: "any" }, ({ state }) => ok(session(state))),
  route("post", `${base}/auth/totp/setup`, signedIn, totpSetup),
  route("post", `${base}/auth/totp/confirm`, signedIn, confirmTotp),
  route("post", `${base}/auth/recovery-codes`, { level: "mfa" }, regenerate),
  route("get", `${base}/sites/{site_id}/settings`, reads("sites.manage"), getSettings),
  route("put", `${base}/sites/{site_id}/settings`, changes("sites.manage"), putSettings),
  route("get", `${base}/sites/{site_id}/key-limits`, reads("sites.manage"), getSiteKeyLimits),
  route("put", `${base}/sites/{site_id}/key-limits`, changes("sites.manage"), putSiteKeyLimits),
  route("get", `${base}/tiers`, reads("users.view"), ({ state }) => ok({ data: state.tiers })),
  route("get", `${base}/users`, reads("users.view"), listUsers),
  route("get", `${base}/accounts/{account_id}`, reads("users.view"), getAccount),
  route("put", `${base}/accounts/{account_id}/key-limits`, changes("sites.manage"), putAccountKeyLimits),
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
