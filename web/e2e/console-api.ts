import type { Page } from "@playwright/test";
import type { paths } from "../packages/api/src/console.gen.ts";
import { accountHandlers } from "./console-account-mock.ts";
import { cancelOrder, createOrder, requestRefund, verifyOrder } from "./console-bill-api.ts";
import { callHandlers } from "./console-calls-api.ts";
import {
  aKey,
  aPreset,
  defaultWorkspace,
  emptyState,
  goodInvite,
  type MockState,
  otherEmail,
  type Schemas,
  systemPreset,
  usualState,
  worstState,
} from "./console-data.ts";
import { usageStatistics } from "./console-usage-api.ts";
import {
  breaks,
  type Ctx as CoreCtx,
  createRoute,
  fail,
  type Handler,
  type MockData,
  mockApi,
  ok,
  paged,
} from "./mock.ts";
import {
  confirmTotp,
  grant,
  knownEmail,
  recoveryCodes,
  regenerate,
  session,
  signIn,
  signOut,
  totpSetup,
  verifyMfa,
} from "./mock-auth.ts";
import { checkOf, withSummary } from "./routing-check.ts";

// The console API (api/console.yaml), answered from memory (mock.ts) with the data of console-data.ts.

const base = "/api/console/v1";
const route = createRoute<paths, MockState>();
type Ctx<K extends keyof paths, M extends "get" | "post" | "put" | "delete"> = CoreCtx<paths, K, M, MockState>;
const none = { level: "none" } as const;
const signedIn = { level: "signed_in" } as const;

function acceptsCurrent(documents: Schemas["LegalDocument"][], consent: Schemas["LegalDocumentVersion"][]): boolean {
  const chosen = new Map<Schemas["LegalDocumentKind"], Schemas["LegalDocumentVersion"]>();
  for (const document of consent) {
    if (chosen.has(document.kind)) {
      return false;
    }
    chosen.set(document.kind, document);
  }
  const newest = new Map<Schemas["LegalDocumentKind"], number>();
  for (const document of documents) {
    newest.set(document.kind, Math.max(newest.get(document.kind) ?? 0, document.version));
  }
  if (chosen.size !== newest.size) {
    return false;
  }
  return [...newest].every(([kind, version]) => {
    const picked = chosen.get(kind);
    return (
      picked !== undefined &&
      picked.version === version &&
      (picked.locale === undefined ||
        documents.some(
          (document) => document.kind === kind && document.locale === picked.locale && document.version === version,
        ))
    );
  });
}

function register({ body, state }: Ctx<"/api/console/v1/auth/register", "post">) {
  if (state.legal.length > 0 && !acceptsCurrent(state.legal, body.consent ?? [])) {
    return fail(403, "consent_required");
  }
  if (body.invite_code !== goodInvite) {
    return fail(422, "invalid_invite");
  }
  if (body.email === knownEmail) {
    return fail(409, "email_taken");
  }
  state.user.email = body.email;
  state.user.password = body.password ?? "";
  state.legalAccepted = state.legal.length > 0;
  return { status: 201, json: { user_id: `user_${"1".repeat(26)}` as Schemas["UserId"] } } as const;
}

// A reset request as the server takes it: the contract's guard refuses an address outside 3 to 254 characters (400);
// the server refuses one that is not a plain address of printable ASCII with a 422 that names no field (emailAllowed,
// ErrBadEmail); any other gets the same empty 202, and a registered one a new 43-character link, which leaves the
// address's earlier one spent (internal/accounts password_change.go, password_recovery.go).
function requestReset({ body, state }: Ctx<"/api/console/v1/auth/password-reset", "post">) {
  const characters = [...body.email].length;
  if (characters < 3 || characters > 254) return breaks("/email");
  const email = body.email.trim().toLowerCase();
  if (!/^[\x21-\x3f\x41-\x7e]+@[\x21-\x3f\x41-\x7e]+$/.test(email)) return fail(422, "unprocessable");
  for (const [token, owner] of state.resetLinks) if (owner === email) state.resetLinks.delete(token);
  const token = String(state.log.length).padStart(43, "m");
  if ([state.user.email, otherEmail].includes(email)) state.resetLinks.set(token, email);
  return { status: 202 as const, json: undefined };
}

// A reset as the server does it: the contract's guard counts the password's characters (12 to 256) before the token is
// looked at; a link works once; the reset ends the sessions of the person it was for, here the mocked user's only when
// the link was theirs.
function completeReset({ body, state }: Ctx<"/api/console/v1/auth/password-reset/complete", "post">) {
  const characters = [...body.new_password].length;
  if (characters < 12 || characters > 256) return breaks("/new_password");
  // the server takes only a 43-byte token for a link it issued
  const owner = body.token.length === 43 ? state.resetLinks.get(body.token) : undefined;
  if (owner === undefined) return fail(422, "unprocessable");
  state.resetLinks.delete(body.token);
  if (owner === state.user.email) {
    state.user.password = body.new_password;
    state.level = "none";
  }
  return { status: 204 as const, json: undefined };
}

function matchingModels(models: Schemas["PublicModel"][], query: URLSearchParams) {
  const q = (query.get("q") ?? "").trim().toLowerCase();
  const author = query.get("author");
  const ability = query.get("ability");
  return models.filter(
    (model) =>
      (!q || model.id.toLowerCase().includes(q) || model.name.toLowerCase().includes(q)) &&
      (!author || model.author.toLowerCase() === author.toLowerCase()) &&
      (!ability || model.abilities.includes(ability as Schemas["ModelAbility"])),
  );
}

function listPublicModels({ query, state }: Ctx<"/api/console/v1/models", "get">) {
  return ok({ data: matchingModels(state.models, query) });
}

function listAccountModels({ query, state }: Ctx<"/api/console/v1/models/account", "get">) {
  return ok({ data: matchingModels(state.models, query) });
}

function listKeys({ query, state }: Ctx<"/api/console/v1/keys", "get">) {
  const page = paged(state.keys, query);
  return page ? ({ status: 200, json: page } as const) : fail(400, "invalid_request");
}

function createKey({ body, state }: Ctx<"/api/console/v1/keys", "post">) {
  const limit = body.spend_limit ?? { amount: "5000000000", currency: "USD" };
  const key = aKey(state.keys.length + 10, {
    name: body.name,
    expires_at: body.expires_at,
    spend_limit: limit,
    limit_remaining: limit,
    limit_reset: body.limit_reset ?? "daily",
    models: body.models,
    ip_allow: body.ip_allow,
  });
  state.keys.unshift(key);
  return { status: 201, json: { key, secret: `${key.prefix}${"x".repeat(45)}${key.last4}` } } as const;
}

function updateKey({ body, params, state }: Ctx<"/api/console/v1/keys/{key_id}", "put">) {
  const index = state.keys.findIndex((key) => key.id === params.key_id);
  const current = state.keys[index];
  if (!current) {
    return fail(404, "not_found");
  }
  const { name, disabled, spend_limit, limit_reset, models, ip_allow } = body;
  const changed = { ...current, name, disabled, spend_limit, limit_reset, models, ip_allow };
  state.keys[index] = changed;
  return { status: 200, json: changed } as const;
}

function deleteKey({ params, state }: Ctx<"/api/console/v1/keys/{key_id}", "delete">) {
  const before = state.keys.length;
  state.keys = state.keys.filter((key) => key.id !== params.key_id);
  return state.keys.length < before ? ({ status: 204, json: undefined } as const) : fail(404, "not_found");
}

// The routing presets an account's keys may use: the live ones are listed, and only the account's own change.
function createPreset({ body, state }: Ctx<"/api/console/v1/routing-presets", "post">) {
  if (state.presets.some((preset) => !preset.disabled && preset.slug === body.slug)) {
    return fail(409, "name_taken");
  }
  const preset = aPreset(state.presets.length + 100, { slug: body.slug, name: body.name, rules: body.rules });
  state.presets.push(preset);
  return { status: 201, json: preset } as const;
}

function ownPreset(state: MockState, id: string | undefined) {
  return state.presets.find((preset) => preset.id === id && preset.scope === "account" && !preset.disabled);
}

function updatePreset({ body, params, state }: Ctx<"/api/console/v1/routing-presets/{preset_id}", "put">) {
  const preset = ownPreset(state, params.preset_id);
  if (!preset) {
    return fail(404, "not_found");
  }
  if (preset.revision !== body.revision) {
    return fail(409, "stale_revision");
  }
  Object.assign(preset, {
    name: body.name,
    rules: body.rules,
    revision: preset.revision + 1,
    version: preset.version + 1,
  });
  return ok(preset);
}

function disablePreset({ params, state }: Ctx<"/api/console/v1/routing-presets/{preset_id}", "delete">) {
  const preset = ownPreset(state, params.preset_id);
  if (!preset) {
    return fail(404, "not_found");
  }
  if (state.routingDefault.preset_id === preset.id) {
    return fail(409, "preset_in_use");
  }
  preset.disabled = true;
  return { status: 204, json: undefined } as const;
}

function setDefault({ body, state }: Ctx<"/api/console/v1/routing-default", "put">) {
  if (body.version !== state.routingDefault.version) {
    return fail(409, "stale_revision");
  }
  // The mock has no site or platform default: with the account's cleared, new keys take the system preset.
  state.routingDefault = {
    preset_id: body.preset_id ?? undefined,
    version: state.routingDefault.version + 1,
    new_keys_preset_id: body.preset_id ?? systemPreset,
  };
  return ok(state.routingDefault);
}

// The account's own provider keys: one the provider took, and one it refused, which routing skips until a test passes.
const providerKeys: Schemas["ProviderKey"][] = [
  {
    id: "00000000-0000-4000-9100-000000000001",
    name: "Work account",
    provider: "openai",
    hint: "Qx7d",
    status: "active",
    enabled: true,
    rejected: false,
    fallback: true,
    created_at: "2026-10-02T09:00:00Z",
  },
  {
    id: "00000000-0000-4000-9100-000000000002",
    name: "Old key",
    provider: "openai",
    status: "rejected",
    enabled: true,
    rejected: true,
    fallback: false,
    created_at: "2026-09-14T09:00:00Z",
  },
];

const sourceClasses = [
  { slug: "official", name: "Official", name_zh: "官方直连" },
  { slug: "cloud", name: "Cloud", name_zh: "云厂商" },
  { slug: "relay", name: "Relay", name_zh: "中转站" },
];

// Removing a passkey raises the factor version and hands this browser a new session, with fresh codes when TOTP is on.
function deletePasskey({ body, params, state }: Ctx<"/api/console/v1/auth/passkeys/{id}", "delete">) {
  if (body.password !== state.user.password) {
    return fail(401, "invalid_credentials");
  }
  const before = state.passkeys.length;
  state.passkeys = state.passkeys.filter((passkey) => passkey.id !== params.id);
  if (state.passkeys.length === before) {
    return fail(404, "not_found");
  }
  const { csrf_token } = grant(state, "signed_in");
  const totp = state.totpEnabled ?? state.mfaEnabled;
  return { status: 200, json: { csrf_token, recovery_codes: totp ? recoveryCodes : undefined } } as const;
}

function listStatement({ query, state }: Ctx<"/api/console/v1/statement", "get">) {
  const page = paged(state.statement, query);
  return page ? ({ status: 200, json: page } as const) : fail(400, "invalid_request");
}

// The export is a file, not JSON: the newest rows of the same statement under a header, as the server writes them.
function exportStatement({ state }: Ctx<"/api/console/v1/statement.csv", "get">) {
  const columns = (row: Schemas["StatementRow"]) => [
    row.at,
    row.kind,
    row.amount.amount,
    row.balance_after?.amount ?? "",
    row.amount.currency,
    row.reference,
    row.frozen_change.amount,
    row.frozen_after.amount,
  ];
  const lines = [
    `at,kind,amount,balance_after,currency,reference,frozen_change,frozen_after`,
    ...state.statement.map((row) => columns(row).join(",")),
  ];
  return {
    status: 200,
    json: undefined,
    text: `${lines.join("\n")}\n`,
    headers: { "Content-Disposition": `attachment; filename="statement-2026-10-14.csv"` },
  } as const;
}

function listOrders({ query, state }: Ctx<"/api/console/v1/orders", "get">) {
  const page = paged(state.orders, query);
  return page ? ok(page) : fail(400, "invalid_request");
}

function getOrder({ params, state }: Ctx<"/api/console/v1/orders/{order_id}", "get">) {
  const order = state.orders.find((o) => o.id === params.order_id);
  return order ? ok(order) : fail(404, "not_found");
}

function listStatementDaily({ query, state }: Ctx<"/api/console/v1/statement/daily", "get">) {
  const page = paged(state.statementDaily, query, ["time_zone"]);
  return page ? ok(page) : fail(400, "invalid_request");
}

function listRedemptions({ query, state }: Ctx<"/api/console/v1/redeem-codes", "get">) {
  const page = paged(state.redemptions, query);
  return page ? ok({ data: page.data, next_cursor: page.next_cursor ?? undefined }) : fail(400, "invalid_request");
}

function redeemCode({ body, state }: Ctx<"/api/console/v1/redeem-codes", "post">) {
  const code = body.code.trim().toUpperCase();
  if (code === "USED-CODE") return fail(422, "redeem_code_used");
  if (code === "EXPIRED-CODE") return fail(422, "redeem_code_expired");
  if (code === "DISABLED-CODE") return fail(422, "redeem_code_disabled");
  if (!code || code.startsWith("BAD") || code === "INVALID") return fail(422, "redeem_code_invalid");
  const created: Schemas["RedeemCodeRedemption"] = {
    id: `rdm_${String(state.redemptions.length + 10).padStart(26, "0")}`,
    amount: { amount: "5000000000", currency: state.site.currency },
    code_hint: code.slice(-4),
    redeemed_at: new Date().toISOString(),
  };
  state.redemptions.unshift(created);
  return { status: 201 as const, json: created };
}

function setBillingPreference({ body, state }: Ctx<"/api/console/v1/billing-preference", "put">) {
  const preference = body.preference === "site" ? state.billingPreference.site_default : body.preference;
  state.billingPreference = {
    ...state.billingPreference,
    preference,
    source: body.preference === "site" ? "site" : "account",
  };
  return ok(state.billingPreference);
}

function setNotifications({ body, state }: Ctx<"/api/console/v1/notification-preferences", "put">) {
  state.notifications = { ...state.notifications, ...body };
  return ok(state.notifications);
}

const handlers: Handler<MockState>[] = [
  route("get", `${base}/version`, none, ({ state }) => ok({ version: state.version })),
  route("get", `${base}/site`, none, ({ state }) => ok(state.site)),
  route("get", `${base}/legal`, none, ({ state }) => ok({ documents: state.legal })),
  route("post", `${base}/auth/sign-in`, none, signIn),
  route("post", `${base}/auth/mfa`, { level: "pending_mfa" }, verifyMfa),
  route("post", `${base}/auth/register`, none, register),
  route("post", `${base}/auth/password-reset`, none, requestReset),
  route("post", `${base}/auth/password-reset/complete`, none, completeReset),
  route("post", `${base}/legal/accept`, signedIn, ({ body, state }) => {
    if (!acceptsCurrent(state.legal, body.consent)) {
      return fail(403, "consent_required");
    }
    state.legalAccepted = true;
    return { status: 204 as const, json: undefined };
  }),
  route("post", `${base}/auth/sign-out`, { level: "any" }, signOut),
  route("get", `${base}/session`, { level: "any" }, ({ state }) => ok(session(state))),
  ...accountHandlers<MockState>(),
  route("post", `${base}/auth/totp/setup`, signedIn, totpSetup),
  route("post", `${base}/auth/totp/confirm`, signedIn, (ctx) => {
    const res = confirmTotp(ctx);
    if (res.status === 200) ctx.state.sessions = ctx.state.sessions.filter((item) => item.current);
    return res;
  }),
  route("post", `${base}/auth/recovery-codes`, { level: "mfa" }, (ctx) => {
    const res = regenerate(ctx);
    if (res.status === 200) ctx.state.sessions = ctx.state.sessions.filter((item) => item.current);
    return res;
  }),
  route("post", `${base}/auth/recovery-codes/take`, signedIn, () => ok({ recovery_codes: [] })),
  route("get", `${base}/auth/passkeys`, signedIn, ({ state }) => ok({ passkeys: state.passkeys })),
  route("delete", `${base}/auth/passkeys/{id}`, signedIn, deletePasskey),
  route("get", `${base}/keys`, { ...signedIn, permission: "api_keys.view" }, listKeys),
  route("post", `${base}/keys`, { ...signedIn, permission: "api_keys.manage" }, createKey),
  route("put", `${base}/keys/{key_id}`, { ...signedIn, permission: "api_keys.manage" }, updateKey),
  route("delete", `${base}/keys/{key_id}`, { ...signedIn, permission: "api_keys.manage" }, deleteKey),
  route("get", `${base}/workspaces`, { ...signedIn, permission: "api_keys.view" }, () =>
    ok({ data: [defaultWorkspace] }),
  ),
  route("get", `${base}/provider-keys`, { ...signedIn, permission: "api_keys.view" }, () => ok({ data: providerKeys })),
  route("get", `${base}/routing-presets`, { ...signedIn, permission: "api_keys.view" }, ({ state }) =>
    ok({ data: state.presets.filter((preset) => !preset.disabled).map(withSummary) }),
  ),
  route("post", `${base}/routing-presets/checks`, { ...signedIn, permission: "api_keys.view" }, ({ body }) =>
    ok(checkOf(body.rules)),
  ),
  route("post", `${base}/routing-presets`, { ...signedIn, permission: "api_keys.manage" }, createPreset),
  route("put", `${base}/routing-presets/{preset_id}`, { ...signedIn, permission: "api_keys.manage" }, updatePreset),
  route("delete", `${base}/routing-presets/{preset_id}`, { ...signedIn, permission: "api_keys.manage" }, disablePreset),
  route("get", `${base}/routing-default`, { ...signedIn, permission: "api_keys.view" }, ({ state }) =>
    ok(state.routingDefault),
  ),
  route("put", `${base}/routing-default`, { ...signedIn, permission: "api_keys.manage" }, setDefault),
  route("get", `${base}/source-classes`, { ...signedIn, permission: "api_keys.view" }, () =>
    ok({ data: sourceClasses }),
  ),
  route("get", `${base}/balance`, { ...signedIn, permission: "balance.view" }, ({ state }) => ok(state.balance)),
  route("get", `${base}/models`, none, listPublicModels),
  route("get", `${base}/models/account`, signedIn, listAccountModels),
  ...callHandlers,
  route("get", `${base}/usage/statistics`, { ...signedIn, permission: "calls.view" }, usageStatistics),
  route("get", `${base}/statement`, { ...signedIn, permission: "balance.view" }, listStatement),
  route("get", `${base}/statement/daily`, { ...signedIn, permission: "balance.view" }, listStatementDaily),
  route("get", `${base}/subscriptions`, { ...signedIn, permission: "balance.view" }, ({ state }) =>
    ok({ data: state.subscriptions }),
  ),
  route("get", `${base}/billing-preference`, signedIn, ({ state }) => ok(state.billingPreference)),
  route("put", `${base}/billing-preference`, signedIn, setBillingPreference),
  route("get", `${base}/redeem-codes`, { ...signedIn, permission: "balance.view" }, listRedemptions),
  route("post", `${base}/redeem-codes`, { ...signedIn, permission: "balance.top_up" }, redeemCode),
  route("get", `${base}/orders`, { ...signedIn, permission: "balance.view" }, listOrders),
  route("post", `${base}/orders`, { ...signedIn, permission: "balance.top_up" }, createOrder),
  route("get", `${base}/orders/{order_id}`, { ...signedIn, permission: "balance.view" }, getOrder),
  route("post", `${base}/orders/{order_id}/verify`, { ...signedIn, permission: "balance.top_up" }, verifyOrder),
  route("post", `${base}/orders/{order_id}/cancel`, { ...signedIn, permission: "balance.top_up" }, cancelOrder),
  route("post", `${base}/orders/{order_id}/refunds`, { ...signedIn, permission: "balance.top_up" }, requestRefund),
  route("get", `${base}/payment-methods`, { ...signedIn, permission: "balance.top_up" }, ({ state }) =>
    ok({ data: state.methods }),
  ),
  route("get", `${base}/notification-preferences`, signedIn, ({ state }) => ok(state.notifications)),
  route("put", `${base}/notification-preferences`, signedIn, setNotifications),
  route("get", `${base}/statement.csv`, { ...signedIn, permission: "balance.view" }, exportStatement),
];

export type { MockData };

// mockConsole answers the console's API for a page. The returned state is live: a test reads what the page did to it
// (state.log, state.keys) and sets up the account it needs before navigating (state.level = "mfa" is signed in).
export async function mockConsole(page: Page, data: MockData = "usual", over: Partial<MockState> = {}) {
  const state = { usual: usualState, empty: emptyState, worst: worstState }[data]();
  Object.assign(state, over);
  return mockApi(page, base, handlers, state);
}

// mockConsoleTab answers another tab of the same browser from the same state: one server, one cookie for both.
export const mockConsoleTab = (page: Page, state: MockState) => mockApi(page, base, handlers, state);

export { signedInAs } from "./mock-auth.ts";

// The server answering the same call, once, with a failure.
export const failOnce = (page: Page, url: string) => {
  let failed = false;
  return page.route(url, (route) => {
    if (failed) {
      return route.fallback();
    }
    failed = true;
    return route.fulfill({ status: 500, json: { error: { code: "internal", message: "internal" } } });
  });
};
