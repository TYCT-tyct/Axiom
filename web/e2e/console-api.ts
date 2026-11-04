import type { Page } from "@playwright/test";
import type { paths } from "../packages/api/src/console.gen.ts";
import { aKey, emptyState, goodInvite, type MockState, type Schemas, usualState, worstState } from "./console-data.ts";
import { type Ctx as CoreCtx, createRoute, fail, type Handler, type MockData, mockApi, ok, paged } from "./mock.ts";
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
  const kinds = new Set(documents.map((document) => document.kind));
  if (chosen.size !== kinds.size) {
    return false;
  }
  return [...kinds].every((kind) => {
    const picked = chosen.get(kind);
    return (
      picked !== undefined &&
      documents.some(
        (document) =>
          document.kind === kind && document.locale === picked.locale && document.version === picked.version,
      )
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

// The filters the call list, its summary and its export share. status is a result, not the error kind: error means the
// call failed.
function matchingCalls(calls: Schemas["Call"][], query: URLSearchParams) {
  const same = (value: string, filter: string | null) => !filter || value === filter;
  const since = query.get("since");
  const until = query.get("until");
  const status = query.get("status") as Schemas["CallStatus"] | null;
  return calls.filter(
    (call) =>
      same(call.model, query.get("model")) &&
      same(call.client_app, query.get("client_app")) &&
      same(call.key_id, query.get("key_id")) &&
      same(call.workspace_id, query.get("workspace_id")) &&
      same(call.id, query.get("request_id")) &&
      (!since || call.at >= new Date(since).toISOString()) &&
      (!until || call.at < new Date(until).toISOString()) &&
      (!status || (call.error_kind !== "") === (status === "error")),
  );
}

function listCalls({ query, state }: Ctx<"/api/console/v1/calls", "get">) {
  const status = query.get("status") as Schemas["CallStatus"] | null;
  const page = paged(matchingCalls(state.calls, query), query);
  return page
    ? ({ status: 200, json: { ...page, status_filter: status ?? undefined } } as const)
    : fail(400, "invalid_request");
}

// The summary groups the same calls; the window is the one the filters asked for, or the whole fixture.
function aggregateCalls({ query, state }: Ctx<"/api/console/v1/calls/aggregate", "get">) {
  const group = (query.get("group_by") ?? "day") as Schemas["CallAggregates"]["group_by"];
  if (group !== "day" && group !== "model" && group !== "key") {
    return fail(400, "invalid_request");
  }
  const groups = new Map<string, { calls: number; amount: bigint; currency: string }>();
  for (const call of matchingCalls(state.calls, query)) {
    const bucket = group === "day" ? call.at.slice(0, 10) : group === "model" ? call.model : String(call.key_id);
    const entry = groups.get(bucket) ?? { calls: 0, amount: 0n, currency: call.price.currency };
    entry.calls += 1;
    entry.amount += BigInt(call.price.amount);
    groups.set(bucket, entry);
  }
  const data = [...groups.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([bucket, entry]) => ({
      bucket,
      calls: String(entry.calls),
      price: { amount: entry.amount.toString(), currency: entry.currency },
    }));
  return {
    status: 200,
    json: {
      data,
      group_by: group,
      since: query.get("since") ?? "2026-09-15T00:00:00Z",
      until: query.get("until") ?? "2026-10-15T00:00:00Z",
    },
  } as const;
}

function exportCalls({ query, state }: Ctx<"/api/console/v1/calls/export", "get">) {
  const cell = (value: string) => (/[",\n]/.test(value) ? `"${value.replaceAll('"', '""')}"` : value);
  const columns = (call: Schemas["Call"]) => [
    call.id,
    call.at,
    call.workspace_id,
    call.key_id,
    call.model,
    call.client_app,
    call.error_kind === "" ? "success" : "error",
    call.error_kind,
    call.price.amount,
    call.price.currency,
  ];
  const lines = [
    "request_id,at,workspace_id,key_id,model,client_app,status,error_kind,price,currency",
    ...matchingCalls(state.calls, query).map((call) => columns(call).map(cell).join(",")),
  ];
  return {
    status: 200,
    json: undefined,
    text: `${lines.join("\n")}\n`,
    headers: { "Content-Disposition": 'attachment; filename="calls-2026-10-14.csv"' },
  } as const;
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
  return { status: 200, json: { csrf_token, recovery_codes: state.mfaEnabled ? recoveryCodes : undefined } } as const;
}

function getCall({ params, state }: Ctx<"/api/console/v1/calls/{call_id}", "get">) {
  const call = state.calls.find((candidate) => candidate.id === params.call_id);
  return call ? ({ status: 200, json: call } as const) : fail(404, "not_found");
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
  route("post", `${base}/legal/accept`, signedIn, ({ body, state }) => {
    if (!acceptsCurrent(state.legal, body.consent)) {
      return fail(403, "consent_required");
    }
    state.legalAccepted = true;
    return { status: 204 as const, json: undefined };
  }),
  route("post", `${base}/auth/sign-out`, { level: "any" }, signOut),
  route("get", `${base}/session`, { level: "any" }, ({ state }) => ok(session(state))),
  route("post", `${base}/auth/totp/setup`, signedIn, totpSetup),
  route("post", `${base}/auth/totp/confirm`, signedIn, confirmTotp),
  route("post", `${base}/auth/recovery-codes`, { level: "mfa" }, regenerate),
  route("post", `${base}/auth/recovery-codes/take`, signedIn, () => ok({ recovery_codes: [] })),
  route("get", `${base}/auth/passkeys`, signedIn, ({ state }) => ok({ passkeys: state.passkeys })),
  route("delete", `${base}/auth/passkeys/{id}`, signedIn, deletePasskey),
  route("get", `${base}/auth/identities`, signedIn, () => ok({ identities: [] })),
  route("get", `${base}/keys`, { ...signedIn, permission: "api_keys.view" }, listKeys),
  route("post", `${base}/keys`, { ...signedIn, permission: "api_keys.manage" }, createKey),
  route("put", `${base}/keys/{key_id}`, { ...signedIn, permission: "api_keys.manage" }, updateKey),
  route("delete", `${base}/keys/{key_id}`, { ...signedIn, permission: "api_keys.manage" }, deleteKey),
  route("get", `${base}/balance`, { ...signedIn, permission: "balance.view" }, ({ state }) => ok(state.balance)),
  route("get", `${base}/models`, none, listPublicModels),
  route("get", `${base}/models/account`, signedIn, listAccountModels),
  route("get", `${base}/calls`, { ...signedIn, permission: "calls.view" }, listCalls),
  route("get", `${base}/calls/aggregate`, { ...signedIn, permission: "calls.view" }, aggregateCalls),
  route("get", `${base}/calls/export`, { ...signedIn, permission: "calls.view" }, exportCalls),
  route("get", `${base}/calls/{call_id}`, { ...signedIn, permission: "calls.view" }, getCall),
  route("get", `${base}/statement`, { ...signedIn, permission: "balance.view" }, listStatement),
  route("get", `${base}/orders`, { ...signedIn, permission: "balance.view" }, listOrders),
  route("get", `${base}/orders/{order_id}`, { ...signedIn, permission: "balance.view" }, getOrder),
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
