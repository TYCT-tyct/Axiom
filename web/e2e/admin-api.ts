import type { Page } from "@playwright/test";
import { catalogHandlers } from "./admin-catalog.ts";
import { type AdminState, emptyState, type Schemas, usualState, worstState } from "./admin-data.ts";
import { financeHandlers, startFinance } from "./admin-finance-api.ts";
import { operationsHandlers, startOperations } from "./admin-operations.ts";
import { opsControlHandlers, startOpsControl } from "./admin-ops-control-api.ts";
import { listPaidBatches } from "./admin-paid-batches-api.ts";
import { policyHandlers, startPolicies } from "./admin-policies-api.ts";
import { pricingHandlers } from "./admin-pricing.ts";
import { base, type Ctx, changes, reads, route } from "./admin-route.ts";
import { routingHandlers, startRouting } from "./admin-routing.ts";
import { siteExtraHandlers, startSiteExtras } from "./admin-site-api.ts";
import { startSubscriptions, subscriptionHandlers } from "./admin-subscriptions-api.ts";
import { supplyHandlers } from "./admin-supply.ts";
import { getTargets, putTargets, replaceTargets, targetComparison } from "./admin-targets-api.ts";
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
  const site = query.get("site_id");
  const model = query.get("model");
  const status = query.get("status");
  const calls = state.calls.filter(
    (call) =>
      (!account || call.account_id === account) &&
      (!request || call.id === request) &&
      (!site || site === state.siteId) &&
      (!model || call.model === model) &&
      (!status || (status === "error" ? Boolean(call.error_kind) : !call.error_kind)),
  );
  const page = paged(calls, query, ["since", "until"]);
  if (!page) {
    return fail(400, "invalid_request");
  }
  const { next_cursor, ...rest } = page;
  return ok({
    ...rest,
    ...(next_cursor ? { next_cursor } : {}),
    ...(model ? { model_filter: model } : {}),
    ...(status ? { status_filter: status as Schemas["AdminCallStatus"] } : {}),
    ...(site ? { site_filter: site } : {}),
    since: query.get("since") ?? "2026-10-29T00:00:00.000Z",
    until: query.get("until") ?? "2026-10-30T23:59:59.000Z",
  });
}

// The site settings the settings page reads besides its own; another site's are not found, as on the server.
function ofSite<T>(state: AdminState, siteId: string | undefined, body: T) {
  return siteId === state.siteId ? ok(body) : fail(404, "not_found");
}

const registration: Schemas["RegistrationPolicy"] = {
  mode: "invite",
  human_check: true,
  human_check_bits: 18,
  domain_burst: 20,
  domain_per_hour: 200,
  domain_allow: [],
  domain_deny: ["mailinator.com"],
};

// The overview sums the mocked calls, so the worst data's long slugs, many accounts and int64 prices reach the page; the
// cash comes back in two currencies, one of them negative, as a site with refunds in another currency would see.
function getOverview({ query, state }: Ctx<"/api/admin/v1/reports/overview", "get">) {
  const revenue = state.calls.reduce((sum, call) => sum + BigInt(call.price?.amount ?? 0), 0n);
  const rank = (key: (call: Schemas["AdminCallSummary"]) => string | undefined) =>
    [
      ...state.calls
        .reduce((groups, call) => {
          const name = key(call) ?? "";
          const group = groups.get(name) ?? { calls: 0, errors: 0 };
          groups.set(name, { calls: group.calls + 1, errors: group.errors + (call.error_kind ? 1 : 0) });
          return groups;
        }, new Map<string, { calls: number; errors: number }>())
        .entries(),
    ]
      .sort((a, b) => b[1].calls - a[1].calls)
      .slice(0, 10);
  const cost = (revenue * 6n) / 10n;
  const errors = state.calls.filter((call) => call.error_kind).length;
  const month = (query.get("until") ?? new Date().toISOString()).slice(0, 7);
  const stored = state.targets.get(month);
  const grossMargin = revenue > 0n ? Number(((revenue - cost) * 10000n) / revenue) : undefined;
  const retention = { ...state.retention, month };
  const targets = targetComparison(
    stored,
    state.calls.length,
    errors,
    revenue,
    cost,
    state.paidUsers,
    BigInt(state.monthlyRevenue.amount),
    grossMargin,
    retention,
  );
  return ok({
    site_id: state.siteId,
    since: query.get("since") ?? "2026-10-29T00:00:00.000Z",
    until: query.get("until") ?? "2026-10-30T23:59:59.000Z",
    calls: state.calls.length,
    errors,
    calls_incomplete: false,
    revenue: { amount: String(revenue), currency: "USD" },
    cost: { amount: String(cost), currency: "USD" },
    unknown_cost_calls: 0,
    paid_users: state.paidUsers,
    monthly_revenue: state.monthlyRevenue,
    ...(grossMargin !== undefined ? { gross_margin: grossMargin } : {}),
    retention,
    cash: [
      { amount: String(revenue / 2n), currency: "USD" },
      { amount: String(-revenue / 20n), currency: "CNY" },
    ],
    top_accounts: rank((call) => call.account_id).map(([account_id, n]) => ({
      account_id: account_id as Schemas["AccountId"],
      ...n,
    })),
    top_models: rank((call) => call.model).map(([model, n]) => ({ model, ...n })),
    targets,
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
  const filters = {
    account: query.get("account_id") ?? "",
    user: query.get("user_id") ?? "",
    merchant: query.get("merchant_id") ?? "",
    state: query.get("state") ?? "",
    since: query.get("since") ?? "",
    until: query.get("until") ?? "",
  };
  const orders = state.orders.filter((order) => orderMatches(order, filters));
  const page = paged(orders, query);
  return page ? ok(page) : fail(400, "invalid_request");
}

// orderMatches is the order list's filters as the server applies them: an empty filter does not narrow, and the date
// window is half-open at the end as the contract writes it.
function orderMatches(
  order: Schemas["PaymentOrder"],
  filters: { account: string; user: string; merchant: string; state: string; since: string; until: string },
) {
  return [
    !filters.account || order.account_id === filters.account,
    !filters.user || order.user_id === filters.user,
    !filters.merchant || order.merchant_id === filters.merchant,
    !filters.state || order.state === filters.state,
    !filters.since || (order.created_at ?? "") >= filters.since,
    !filters.until || (order.created_at ?? "") < filters.until,
  ].every(Boolean);
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
  route("get", `${base}/site`, none, ({ state }) => {
    const logo = state.logoUrl ?? state.logo.logo?.data_url;
    return ok({ name: state.brandName ?? "Axiom", ...(logo ? { logo_url: logo } : {}) });
  }),
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
  route("get", `${base}/sites/{site_id}/system-settings`, reads("sites.manage"), ({ params, state }) =>
    ofSite(state, params.site_id, { name: "Axiom", registration: "invite", name_editable: true }),
  ),
  route("get", `${base}/sites/{site_id}/system-settings/payments`, reads("sites.manage"), ({ params, state }) =>
    ofSite(state, params.site_id, {
      open: true,
      credits_min: { amount: "1000000000", currency: "USD" },
      credits_max: { amount: "10000000000000", currency: "USD" },
      top_up_buffer_ppm: 20000,
    }),
  ),
  route("get", `${base}/sites/{site_id}/registration`, reads("sites.manage"), ({ params, state }) =>
    ofSite(state, params.site_id, { policy: registration, defaults: registration }),
  ),
  route("get", `${base}/sites/{site_id}/sign-in`, reads("sites.manage"), ({ params, state }) =>
    ofSite(state, params.site_id, {
      login_domain: "login.example.com",
      passkeys: true,
      providers: [
        { provider: "github", client_id: "Iv1.0123456789abcdef", configured: true },
        { provider: "google", configured: false },
        { provider: "linuxdo", configured: false },
      ],
    }),
  ),
  route("get", `${base}/settings/mail`, reads("sites.manage"), () =>
    ok({
      enabled: true,
      host: "smtp.example.com",
      port: 465,
      username: "mailer@example.com",
      from: "Axiom <noreply@example.com>",
      has_password: true,
      restart_required: false,
      source: "database",
    }),
  ),
  route("get", `${base}/tiers`, reads("users.view"), ({ state }) => ok({ data: state.tiers })),
  route("get", `${base}/payments/merchants`, reads("sites.manage"), ({ query, state }) =>
    query.get("site_id") === state.siteId ? ok({ data: state.merchants }) : fail(404, "not_found"),
  ),
  route("get", `${base}/payments/orders`, reads("sites.manage"), listPaymentOrders),
  route("get", `${base}/payments/paid-batches`, reads("sites.manage"), listPaidBatches),
  route("get", `${base}/payments/refunds`, reads("sites.manage"), listPaymentRefunds),
  route("get", `${base}/payments/cash`, reads("sites.manage"), ({ query, state }) =>
    query.get("site_id") === state.siteId ? ok({ data: [], next_cursor: null }) : fail(404, "not_found"),
  ),
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
  route("get", `${base}/reports/overview`, reads("sites.manage"), getOverview),
  route("get", `${base}/reports/targets/{month}`, reads("sites.manage"), getTargets),
  route("put", `${base}/reports/targets/{month}`, changes("sites.manage"), putTargets),
  route("put", `${base}/reports/targets/{month}/all`, changes("sites.manage"), replaceTargets),
  route("get", `${base}/calls`, reads("calls.search"), listCalls),
  route("get", `${base}/calls/{call_id}/trace`, reads("calls.inspect"), getTrace),
  route("get", `${base}/audit`, reads("audit.view"), listAudit),
  route("get", `${base}/audit/{entry_id}`, reads("audit.view"), getAudit),
  route("post", `${base}/accounts/{account_id}/credit-grants`, changes("credits.grant"), grantCredit),
  ...siteExtraHandlers,
  ...supplyHandlers,
  ...catalogHandlers,
  ...pricingHandlers,
  ...financeHandlers,
  ...subscriptionHandlers,
  ...routingHandlers,
  ...operationsHandlers,
  ...opsControlHandlers,
  ...policyHandlers,
];

// mockAdmin answers the admin's API for a page. The returned state is live: a test reads what the page did to it
// (state.log, state.models) and sets up the session it needs before navigating (state.level = "mfa" is signed in).
export async function mockAdmin(page: Page, data: MockData = "usual", over: Partial<AdminState> = {}) {
  const state = { usual: usualState, empty: emptyState, worst: worstState }[data]();
  Object.assign(state, over);
  startFinance(state, data);
  startRouting(state, data);
  startOperations(state, data);
  startSubscriptions(state, data);
  startOpsControl(state, data);
  startSiteExtras(state);
  startPolicies(state, data);
  return mockApi(page, base, handlers, state);
}
